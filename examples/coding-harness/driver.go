package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/chakra/surfaces/web"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
	"github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1/webv1connect"
)

// prompt is the task, the same one the parity script gives codex exec.
const prompt = "The tests in this repository fail. Make them pass."

// steerText is sent while the first turn runs, after its first tool result.
// With steering (#149) it reaches the model inside that turn; without it, it
// waits and runs as a second turn.
const steerText = "Also: when you finish, say in one line which function you changed."

// runResult is everything a check reads.
type runResult struct {
	// events are the Watch frames' HostEvents in arrival order.
	events []host.HostEvent
	// frames are the raw frames, for the JSONL file.
	frames []*webv1.Frame
	// requests are the model requests the recorder saw.
	requests []recordedRequest
	// turn is the first turn's result, nil if it failed.
	turn *agent.TurnResult
	// turnErr is the first turn's failure, if any.
	turnErr string
	// steerTurnSeen reports whether the steer ran as a turn of its own.
	steerTurnSeen bool
	// testsPassed and testOutput are go test in the workspace after the run.
	testsPassed bool
	testOutput  string
	// testFilesChanged lists *_test.go files that differ from the fixture.
	testFilesChanged []string
	wall             time.Duration
}

// drive runs the task over agentweb's Connect API against an in-process
// server, the way the browser does: Watch first, then Submit, then the steer.
func drive(ctx context.Context, p profile) (*runResult, error) {
	mgr := web.NewSessionManager(p.build)
	srv := httptest.NewServer(web.HandlerWithSessions(mgr))
	defer srv.Close()
	client := webv1connect.NewHostServiceClient(http.DefaultClient, srv.URL)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	res := &runResult{}
	var mu sync.Mutex
	firstToolEnd := make(chan struct{})
	var firstToolOnce sync.Once
	turnsDone := make(chan struct{}, 4)

	created, err := client.CreateSession(ctx, connect.NewRequest(&webv1.CreateSessionRequest{}))
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	sid := created.Msg.GetSessionId()
	defer mgr.CloseAll()

	stream, err := client.Watch(ctx, connect.NewRequest(&webv1.WatchRequest{SessionId: sid}))
	if err != nil {
		return nil, fmt.Errorf("watch: %w", err)
	}
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for stream.Receive() {
			f := stream.Msg()
			var ev host.HostEvent
			_ = json.Unmarshal(f.GetPayload(), &ev)
			mu.Lock()
			res.frames = append(res.frames, f)
			res.events = append(res.events, ev)
			mu.Unlock()
			switch ev.Kind {
			case host.HostRunnerEvent:
				if ev.RunnerEvent.Kind == agent.EventToolEnd {
					firstToolOnce.Do(func() { close(firstToolEnd) })
				}
			case host.HostElicitRequest:
				// Headless, like codex exec with approvals off: decline, and
				// the JSONL keeps the ask for the escalation check.
				go respond(ctx, client, sid, ev.AskID)
			case host.HostTurnDone, host.HostTurnFailed:
				turnsDone <- struct{}{}
			}
		}
	}()

	start := time.Now()
	submitErr := make(chan error, 1)
	go func() {
		_, err := client.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{Input: prompt, SessionId: sid}))
		submitErr <- err
	}()

	steered := false
	select {
	case <-firstToolEnd:
		steered = true
		go func() {
			_, _ = client.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{Input: steerText, SessionId: sid}))
		}()
	case err := <-submitErr:
		if err != nil {
			return nil, fmt.Errorf("submit: %w", err)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	want := 1
	if steered {
		want = 2
	}
	for i := 0; i < want; i++ {
		select {
		case <-turnsDone:
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for turn %d: %w", i+1, ctx.Err())
		}
	}
	res.wall = time.Since(start)
	cancel()
	<-watchDone

	if p.recorder != nil {
		res.requests = p.recorder.requests()
	}
	res.summarizeTurns()
	return res, nil
}

// respond declines an ask.
func respond(ctx context.Context, client webv1connect.HostServiceClient, sid string, askID int64) {
	body, _ := json.Marshal(map[string]string{"action": "decline"})
	_, _ = client.RespondToAsk(ctx, connect.NewRequest(&webv1.RespondToAskRequest{AskId: askID, Result: body, By: "coding-harness", SessionId: sid}))
}

// summarizeTurns picks the first turn's result out of the events and notes
// whether the steer arrived as a turn of its own.
func (r *runResult) summarizeTurns() {
	inputs := 0
	for _, ev := range r.events {
		switch ev.Kind {
		case host.HostUserInput:
			inputs++
			if ev.Message == steerText {
				r.steerTurnSeen = true
			}
		case host.HostTurnDone:
			if inputs == 1 && r.turn == nil {
				r.turn = ev.Result
			}
		case host.HostTurnFailed:
			if inputs == 1 && r.turn == nil && r.turnErr == "" {
				r.turnErr = ev.Err
			}
		}
	}
}

// toolCalls returns every tool-begin call of the run, in order.
func (r *runResult) toolCalls() []agent.ToolCall {
	var out []agent.ToolCall
	for _, ev := range r.events {
		if ev.Kind == host.HostRunnerEvent && ev.RunnerEvent.Kind == agent.EventToolBegin && ev.RunnerEvent.ToolCall != nil {
			out = append(out, *ev.RunnerEvent.ToolCall)
		}
	}
	return out
}

// finish runs the fixture's tests in root and diffs its test files against
// the fixture, once the agent is done.
func (r *runResult) finish(root, fixture string) {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	r.testsPassed = err == nil
	r.testOutput = string(out)

	_ = filepath.WalkDir(fixture, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(fixture, path)
		want, _ := os.ReadFile(path)
		got, gerr := os.ReadFile(filepath.Join(root, rel))
		if gerr != nil || string(got) != string(want) {
			r.testFilesChanged = append(r.testFilesChanged, rel)
		}
		return nil
	})
}

// writeJSONL writes one {kind, payload} line per frame.
func (r *runResult) writeJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, f := range r.frames {
		if err := enc.Encode(struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}{f.GetKind(), json.RawMessage(f.GetPayload())}); err != nil {
			return err
		}
	}
	return nil
}

// copyTree copies the fixture into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
