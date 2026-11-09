package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/chakra"
)

// modelReply is one scripted model answer: text, or a single tool call.
type modelReply struct {
	text     string
	tool     string
	toolArgs string
	// usage, when set, is sent as the stream's final usage chunk:
	// prompt, cached and completion tokens.
	usage *[3]int
}

// fakeModel is an OpenAI-compatible endpoint that answers each request with
// the next reply, streamed or not as the request asks.
func fakeModel(t *testing.T, replies ...modelReply) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		bodiesMu.Lock()
		modelBodies[srvURL(r)] = append(modelBodies[srvURL(r)], string(body))
		bodiesMu.Unlock()
		if next >= len(replies) {
			mu.Unlock()
			http.Error(w, "no more scripted replies", http.StatusInternalServerError)
			return
		}
		rep := replies[next]
		next++
		mu.Unlock()
		if !req.Stream {
			msg := map[string]any{"role": "assistant", "content": rep.text}
			resp := map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}}}
			if u := rep.usage; u != nil {
				resp["usage"] = map[string]any{"prompt_tokens": u[0], "completion_tokens": u[2], "prompt_tokens_details": map[string]any{"cached_tokens": u[1]}}
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if rep.tool != "" {
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": rep.tool, "arguments": rep.toolArgs},
			}}}}}})
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		} else {
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": rep.text}}}})
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
		}
		if u := rep.usage; u != nil {
			send(map[string]any{"choices": []any{}, "usage": map[string]any{
				"prompt_tokens": u[0], "completion_tokens": u[2],
				"prompt_tokens_details": map[string]any{"cached_tokens": u[1]},
			}})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// modelBodies records each fake model's request bodies, by its address.
var (
	bodiesMu    sync.Mutex
	modelBodies = map[string][]string{}
)

func srvURL(r *http.Request) string { return "http://" + r.Host }

// runExecCmd runs `agentchat exec` with args against srv and returns stdout,
// stderr and the exit code main would use.
func runExecCmd(t *testing.T, srv *httptest.Server, stdin string, args ...string) (string, string, int) {
	t.Helper()
	return runExecConfig(t, fmt.Sprintf(`{"model": {"baseUrl": %q, "model": "m"}}`, srv.URL), stdin, args...)
}

// runExecConfig is runExecCmd with the config file's JSON given.
func runExecConfig(t *testing.T, config, stdin string, args ...string) (string, string, int) {
	t.Helper()
	root, _ := newRoot()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	root.SetArgs(append([]string{"exec", "--config", cfg}, args...))
	err := root.Execute()
	code := 0
	if err != nil {
		code = 1
		var ee *exitError
		if errors.As(err, &ee) {
			code = ee.code
		}
	}
	return out.String(), errOut.String(), code
}

func writeSchema(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(p, []byte(`{"type":"object","properties":{"status":{"enum":["fixed","not_fixed"]}},"required":["status"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func frameKinds(t *testing.T, out string) []string {
	t.Helper()
	var kinds []string
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var f struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			t.Fatalf("stdout line is not a JSON frame: %q (%v)", sc.Text(), err)
		}
		kinds = append(kinds, f.Kind)
	}
	return kinds
}

func TestExecWritesJSONLinesAndTheStructuredResult(t *testing.T) {
	srv := fakeModel(t, modelReply{text: "Fixed it."}, modelReply{text: `{"status":"fixed"}`})
	last := filepath.Join(t.TempDir(), "last.json")
	out, _, code := runExecCmd(t, srv, "", "-p", "fix it", "--approve", "read-only-auto",
		"--json", "--output-schema", writeSchema(t), "--output-last-message", last)
	if code != 0 {
		t.Fatalf("exit %d, stdout:\n%s", code, out)
	}
	kinds := strings.Join(frameKinds(t, out), " ")
	for _, want := range []string{"user-input", "runner-event", "turn-done"} {
		if !strings.Contains(kinds, want) {
			t.Errorf("no %s frame in %s", want, kinds)
		}
	}
	b, _ := os.ReadFile(last)
	if string(b) != `{"status":"fixed"}` {
		t.Errorf("--output-last-message wrote %q", b)
	}
}

func TestExecPrintsOnlyTheFinalMessageWithoutJSON(t *testing.T) {
	srv := fakeModel(t, modelReply{text: "All done."})
	out, _, code := runExecCmd(t, srv, "the task, from stdin", "--approve", "ask")
	if code != 0 || strings.TrimSpace(out) != "All done." {
		t.Errorf("exit %d, stdout %q", code, out)
	}
}

func TestExecExitsThreeWhenNoAnswerMatchesTheSchema(t *testing.T) {
	srv := fakeModel(t, modelReply{text: "done"}, modelReply{text: `{"status":"maybe"}`}, modelReply{text: `{"status":"perhaps"}`})
	_, _, code := runExecCmd(t, srv, "", "-p", "go", "--approve", "ask", "--output-schema", writeSchema(t))
	if code != exitSchemaInvalid {
		t.Errorf("exit %d, want %d", code, exitSchemaInvalid)
	}
}

func TestExecExitsTwoAtTheStepLimit(t *testing.T) {
	ws := t.TempDir()
	srv := fakeModel(t, modelReply{tool: "list_files", toolArgs: `{}`}, modelReply{tool: "list_files", toolArgs: `{}`})
	_, _, code := runExecCmd(t, srv, "", "-p", "go", "--approve", "allow", "--workspace", ws, "--max-steps", "1")
	if code != exitMaxSteps {
		t.Errorf("exit %d, want %d", code, exitMaxSteps)
	}
}

// No exec run can install a turn-end hook yet, so this checks the mapping
// rather than a run.
func TestExecExitsFourWhenAHookStopsTheTurn(t *testing.T) {
	stopped := fmt.Errorf("%w: same draft again", agent.ErrTurnStopped)
	var ee *exitError
	if err := execExit(stopped, &agent.TurnResult{}, false); !errors.As(err, &ee) || ee.code != exitTurnStopped {
		t.Fatalf("execExit = %v, want exit %d", err, exitTurnStopped)
	}
}

// TestExecDeniesWhatTheModeWouldAskAbout checks the headless contract. Nobody
// can answer an ask, so the call is refused, the run says so, and nothing
// blocks.
func TestExecDeniesWhatTheModeWouldAskAbout(t *testing.T) {
	ws := t.TempDir()
	srv := fakeModel(t,
		modelReply{tool: "write_file", toolArgs: `{"path":"note.txt","content":"hi"}`},
		modelReply{text: "Couldn't write it."})
	out, _, code := runExecCmd(t, srv, "", "-p", "write a note", "--approve", "read-only-auto", "--json", "--workspace", ws)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(strings.Join(frameKinds(t, out), " "), "ask-denied") {
		t.Errorf("no ask-denied frame:\n%s", out)
	}
	if s := lastSummary(t, out); s.ToolCalls != 1 || s.ToolCallsDenied != 1 {
		t.Errorf("summary toolCalls %d, toolCallsDenied %d; want 1, 1", s.ToolCalls, s.ToolCallsDenied)
	}
	if _, err := os.Stat(filepath.Join(ws, "note.txt")); err == nil {
		t.Error("the write ran although it needed an approval nobody gave")
	}
}

func TestExecRefusesBadUsage(t *testing.T) {
	srv := fakeModel(t)
	for _, args := range [][]string{
		{"-p", "go"},                       // no --approve
		{"-p", "go", "--approve", "yolo!"}, // unknown mode
		{"--approve", "ask"},               // no prompt anywhere
		{"-p", "go", "--approve", "ask", "--output-schema", "/no/such/file"},
	} {
		if _, _, code := runExecCmd(t, srv, "", args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestExecPrintsTheSessionToResume(t *testing.T) {
	srv := fakeModel(t, modelReply{text: "one"}, modelReply{text: "two"})
	store := "sqlite://" + filepath.Join(t.TempDir(), "s.db")
	_, errOut, code := runExecCmd(t, srv, "", "-p", "first", "--approve", "ask", "--session-store", store)
	if code != 0 || !strings.Contains(errOut, "agentchat exec: session ") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(errOut), "agentchat exec: session "))
	if _, _, code := runExecCmd(t, srv, "", "-p", "second", "--approve", "ask", "--session-store", store, "--session", id); code != 0 {
		t.Fatalf("resume: exit %d", code)
	}
	bodiesMu.Lock()
	bodies := modelBodies[srv.URL]
	bodiesMu.Unlock()
	if len(bodies) != 2 || !strings.Contains(bodies[1], "first") || !strings.Contains(bodies[1], "second") {
		t.Errorf("the resumed run's request didn't carry the first task: %q", bodies)
	}
}

// frames decodes stdout's JSON lines.
func frames(t *testing.T, out string) []execTestFrame {
	t.Helper()
	var fs []execTestFrame
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var f execTestFrame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			t.Fatalf("stdout line is not a JSON frame: %q (%v)", sc.Text(), err)
		}
		fs = append(fs, f)
	}
	return fs
}

type execTestFrame struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

func lastSummary(t *testing.T, out string) execTally {
	t.Helper()
	fs := frames(t, out)
	if len(fs) == 0 || fs[len(fs)-1].Kind != "summary" {
		t.Fatalf("the last frame is not a summary:\n%s", out)
	}
	var s execTally
	if err := json.Unmarshal(fs[len(fs)-1].Payload, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func costFrames(t *testing.T, out string) []execCost {
	t.Helper()
	var cs []execCost
	for _, f := range frames(t, out) {
		if f.Kind == "cost" {
			var c execCost
			if err := json.Unmarshal(f.Payload, &c); err != nil {
				t.Fatal(err)
			}
			cs = append(cs, c)
		}
	}
	return cs
}

// connConfig is a one-connection registry named "bench" on srv, with price
// as the connection's price JSON, or none when price is empty.
func connConfig(srv *httptest.Server, price string) string {
	priceField := ""
	if price != "" {
		priceField = `, "price": ` + price
	}
	return fmt.Sprintf(`{"connections": {"active": "bench", "connections": {"bench": {"type": "lmstudio", "baseUrl": %q, "model": "m"%s}}}}`, srv.URL, priceField)
}

// twoPricedSteps is a tool call and then an answer, each with usage.
func twoPricedSteps(t *testing.T) *httptest.Server {
	return fakeModel(t,
		modelReply{tool: "list_files", toolArgs: `{}`, usage: &[3]int{1000, 200, 100}},
		modelReply{text: "Two files.", usage: &[3]int{2000, 1000, 50}})
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestExecWritesCostPerStepFromTheConnectionPrice(t *testing.T) {
	srv := twoPricedSteps(t)
	out, _, code := runExecConfig(t, connConfig(srv, `{"input": 2, "cachedInput": 0.5, "output": 10}`), "",
		"-p", "list", "--approve", "read-only-auto", "--json", "--workspace", t.TempDir())
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	costs := costFrames(t, out)
	// (800*2 + 200*0.5 + 100*10) / 1e6, then (1000*2 + 1000*0.5 + 50*10) / 1e6.
	want := []float64{0.0027, 0.003}
	if len(costs) != len(want) {
		t.Fatalf("got %d cost frames, want %d:\n%s", len(costs), len(want), out)
	}
	for i, c := range costs {
		if c.Connection != "bench" || c.USD == nil || !near(*c.USD, want[i]) {
			t.Errorf("cost frame %d = %+v (usd %v), want bench at %v", i, c, c.USD, want[i])
		}
	}
	s := lastSummary(t, out)
	if s.USD == nil || !near(*s.USD, 0.0057) || s.UnpricedCalls != 0 {
		t.Errorf("summary usd %v, unpriced %d; want 0.0057, 0", s.USD, s.UnpricedCalls)
	}
	wantUsage := agent.Usage{InputTokens: 3000, CachedInputTokens: 1200, OutputTokens: 150}
	if s.Usage != wantUsage || s.UsageByConnection["bench"] != wantUsage {
		t.Errorf("summary usage %+v by connection %+v, want %+v", s.Usage, s.UsageByConnection, wantUsage)
	}
	if s.ModelCalls != 2 || s.Steps != 2 || s.ToolCalls != 1 || s.ToolCallsByName["list_files"] != 1 || s.ExitCode != 0 {
		t.Errorf("summary = %+v", s)
	}
}

func TestExecWritesNoCostForAnUnpricedConnection(t *testing.T) {
	srv := twoPricedSteps(t)
	out, _, code := runExecConfig(t, connConfig(srv, ""), "",
		"-p", "list", "--approve", "read-only-auto", "--json", "--workspace", t.TempDir())
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, c := range costFrames(t, out) {
		if c.USD != nil {
			t.Errorf("an unpriced connection reported usd %v", *c.USD)
		}
	}
	s := lastSummary(t, out)
	if s.USD != nil || s.UnpricedCalls != 2 || s.Usage.InputTokens != 3000 {
		t.Errorf("summary usd %v, unpriced %d, usage %+v; want null, 2, 3000 in", s.USD, s.UnpricedCalls, s.Usage)
	}
}

func TestExecSummaryIsTheLastFrameOnEveryExit(t *testing.T) {
	cases := []struct {
		name    string
		config  func(*httptest.Server) string
		replies []modelReply
		args    []string
		want    int
	}{
		{"done", nil, []modelReply{{text: "ok"}}, nil, 0},
		{"model error", func(*httptest.Server) string {
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
			}))
			t.Cleanup(bad.Close)
			return connConfig(bad, "")
		}, nil, nil, exitTurnFailed},
		{"step limit", nil, []modelReply{{tool: "list_files", toolArgs: `{}`}, {tool: "list_files", toolArgs: `{}`}}, []string{"--max-steps", "1"}, exitMaxSteps},
		{"no such connection", func(s *httptest.Server) string {
			return fmt.Sprintf(`{"connections": {"active": "gone", "connections": {"bench": {"type": "lmstudio", "baseUrl": %q, "model": "m"}}}}`, s.URL)
		}, nil, nil, exitStartFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := fakeModel(t, c.replies...)
			config := connConfig(srv, "")
			if c.config != nil {
				config = c.config(srv)
			}
			args := append([]string{"-p", "go", "--approve", "allow", "--json", "--workspace", t.TempDir()}, c.args...)
			out, _, code := runExecConfig(t, config, "", args...)
			if code != c.want {
				t.Fatalf("exit %d, want %d:\n%s", code, c.want, out)
			}
			if s := lastSummary(t, out); s.ExitCode != c.want {
				t.Errorf("summary exitCode %d, want %d", s.ExitCode, c.want)
			}
		})
	}
}

func TestExecStreamsToolBeginAndEndWithResults(t *testing.T) {
	srv := twoPricedSteps(t)
	out, _, code := runExecConfig(t, connConfig(srv, ""), "",
		"-p", "list", "--approve", "read-only-auto", "--json", "--workspace", t.TempDir())
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var begin, end bool
	for _, f := range frames(t, out) {
		if f.Kind != "runner-event" {
			continue
		}
		var p struct {
			RunnerEvent agent.Event
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			t.Fatal(err)
		}
		ev := p.RunnerEvent
		switch {
		case ev.Kind == agent.EventToolBegin && ev.ToolCall != nil && ev.ToolCall.Name == "list_files":
			begin = true
		case ev.Kind == agent.EventToolEnd && ev.ToolResult != nil && len(ev.ToolResult.Content) > 0:
			end = true
		}
	}
	if !begin || !end {
		t.Errorf("tool-begin %v, tool-end with a result %v:\n%s", begin, end, out)
	}
}

func TestExecRunStartNamesTheConnectionAndAgentchatEnv(t *testing.T) {
	t.Setenv("AGENTCHAT_BENCH_TAG", "x")
	srv := fakeModel(t, modelReply{text: "ok"})
	out, _, code := runExecConfig(t, connConfig(srv, ""), "", "-p", "go", "--approve", "ask", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	fs := frames(t, out)
	if len(fs) == 0 || fs[0].Kind != "run-start" {
		t.Fatalf("the first frame is not run-start:\n%s", out)
	}
	var p struct {
		Connection string   `json:"connection"`
		Env        []string `json:"env"`
	}
	if err := json.Unmarshal(fs[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Connection != "bench" || !slices.Contains(p.Env, "AGENTCHAT_BENCH_TAG") {
		t.Errorf("run-start = %s", fs[0].Payload)
	}
}

func TestExecExitsFiveWhenAServerIsDown(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	srv := fakeModel(t, modelReply{text: "answered without the server"})
	config := fmt.Sprintf(`{"model": {"baseUrl": %q, "model": "m"}, "servers": [{"id": "declaire", "url": %q}]}`, srv.URL, down.URL+"/mcp")
	out, errOut, code := runExecConfig(t, config, "", "-p", "go", "--approve", "ask", "--json")
	if code != exitStartFailed {
		t.Fatalf("exit %d, want %d; stderr %q", code, exitStartFailed, errOut)
	}
	if s := lastSummary(t, out); s.ExitCode != exitStartFailed || s.ModelCalls != 0 {
		t.Errorf("summary = %+v", s)
	}
}

// The structured finalize call emits no step event, so the summary takes
// its tokens from TurnResult and prices them on the active connection.
func TestExecSummaryCountsTheStructuredFinalizeCall(t *testing.T) {
	srv := fakeModel(t,
		modelReply{text: "Fixed it.", usage: &[3]int{1000, 0, 100}},
		modelReply{text: `{"status":"fixed"}`, usage: &[3]int{500, 0, 20}})
	out, _, code := runExecConfig(t, connConfig(srv, `{"input": 1, "output": 10}`), "",
		"-p", "fix", "--approve", "ask", "--json", "--output-schema", writeSchema(t))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	s := lastSummary(t, out)
	if s.ModelCalls != 2 || s.Usage.InputTokens != 1500 || s.Usage.OutputTokens != 120 {
		t.Errorf("summary calls %d usage %+v, want 2 calls, 1500 in, 120 out", s.ModelCalls, s.Usage)
	}
	if s.USD == nil || !near(*s.USD, (1500*1+120*10)/1e6) {
		t.Errorf("summary usd %v", s.USD)
	}
}
