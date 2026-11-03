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

// prompt is the task. The parity script gives codex exec the same one.
const prompt = "The tests in this repository fail. Make them pass."

// steerText is sent while the first turn runs, after its first tool result.
// With steering (#149) it reaches the model inside that turn; without it, it
// waits and runs as a second turn.
const steerText = "Also: when you finish, say in one line which function you changed."

// runResult holds the run's outcomes, and every check reads from it.
type runResult struct {
	// events are the Watch frames' HostEvents in arrival order.
	events []host.HostEvent
	// frames are the raw frames, for the JSONL file.
	frames []*webv1.Frame
	// requests are the model requests, as the recorder saw them.
	requests []recordedRequest
	// turn is the first turn's result, nil if it failed.
	turn *agent.TurnResult
	// turnErr is the first turn's failure, if any.
	turnErr string
	// steerTurnSeen reports whether the steer ran as a turn of its own.
	steerTurnSeen bool
	// testsPassed and testOutput come from go test in the workspace after the
	// run.
	testsPassed bool
	testOutput  string
	// testFilesChanged lists *_test.go files that differ from the fixture.
	testFilesChanged []string
	wall             time.Duration
	// review is /review's result after the run, as Dispatch returned it.
	review string
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

	// The task asks for the result in result.schema.json's shape, as the
	// parity run asks codex exec with --output-schema.
	schema, err := os.ReadFile(filepath.Join(p.dir, "result.schema.json"))
	if err != nil {
		return nil, err
	}
	start := time.Now()
	submitErr := make(chan error, 1)
	go func() {
		_, err := client.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{Input: prompt, SessionId: sid, ResponseSchema: string(schema)}))
		submitErr <- err
	}()

	// The steer goes in after the first tool result. A host that steers
	// queues it for the running turn and returns at once; one that doesn't
	// runs it as a turn of its own. Either way the run is over once both
	// Submits have returned.
	steerErr := make(chan error, 1)
	select {
	case <-firstToolEnd:
		go func() {
			_, err := client.Submit(ctx, connect.NewRequest(&webv1.SubmitRequest{Input: steerText, SessionId: sid}))
			steerErr <- err
		}()
		if err := <-submitErr; err != nil {
			return nil, fmt.Errorf("submit: %w", err)
		}
		if err := <-steerErr; err != nil {
			return nil, fmt.Errorf("steer: %w", err)
		}
	case err := <-submitErr:
		if err != nil {
			return nil, fmt.Errorf("submit: %w", err)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Every turn-done was emitted before its Submit returned, but the Watch
	// may still be delivering them. Take the first, then any more until the
	// stream goes quiet (a late steer runs as a second turn).
	select {
	case <-turnsDone:
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for the turn to finish: %w", ctx.Err())
	}
	for quiet := false; !quiet; {
		select {
		case <-turnsDone:
		case <-time.After(300 * time.Millisecond):
			quiet = true
		}
	}
	res.wall = time.Since(start)
	// The checks read the main agent's requests, so take them before the
	// review runs. The review adds its sub-agent's requests, and their
	// instructions are its rubric.
	if p.recorder != nil {
		res.requests = p.recorder.requests()
	}

	// Then ask for a review of the uncommitted changes, as a person would
	// before committing (#152). It runs outside any turn, and its
	// sub-agent's events arrive on the same Watch.
	review, err := client.Dispatch(ctx, connect.NewRequest(&webv1.DispatchRequest{Line: "/review", SessionId: sid}))
	if err != nil {
		return nil, fmt.Errorf("review: %w", err)
	}
	res.review = string(review.Msg.GetPayload())
	time.Sleep(300 * time.Millisecond) // let the Watch deliver the reviewer's last events
	cancel()
	<-watchDone

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
	inputs, turns := 0, 0
	for _, ev := range r.events {
		switch ev.Kind {
		case host.HostUserInput:
			if ev.Label == "steer" {
				continue // queued into a running turn, not a turn of its own
			}
			inputs++
			if ev.Message == steerText {
				r.steerTurnSeen = true
			}
		case host.HostTurnDone:
			turns++
			if turns > 1 {
				// A steer that arrives after the turn's last model call
				// runs next, without a user-input event of its own.
				r.steerTurnSeen = true
			}
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
