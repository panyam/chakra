package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// modelReply is one scripted model answer: text, or a single tool call.
type modelReply struct {
	text     string
	tool     string
	toolArgs string
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
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}}})
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
	root, _ := newRoot()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(`{"model": {"baseUrl": %q, "model": "m"}}`, srv.URL)), 0o644); err != nil {
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

// TestExecDeniesWhatTheModeWouldAskAbout is the headless contract: nobody can
// answer an ask, so the call is refused, the run says so, and nothing blocks.
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
