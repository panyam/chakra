package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/panyam/chakra/ext/files"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// askFixture is a testdata/asks file. The web UI's tests (asks.test.ts) check
// that the UI builds Result from Schema; these check that Schema is what the
// host really asks and that Result really gets the call run.
type askFixture struct {
	Schema json.RawMessage        `json:"schema"`
	Result core.ElicitationResult `json:"result"`
}

func loadAsk(t *testing.T, name string) askFixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "asks", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx askFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

func sameJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

// answerAsWeb runs one turn on app and answers its single ask with result, as
// the web UI would over RespondToAsk. It returns the schema the ask carried.
func answerAsWeb(t *testing.T, app *host.App, result core.ElicitationResult) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	events := app.Subscribe(ctx)
	turn := make(chan error, 1)
	go func() { turn <- app.RunTurn(ctx, "go") }()

	var schema json.RawMessage
	for schema == nil {
		select {
		case ev := <-events:
			if ev.Kind == host.HostElicitRequest {
				schema = ev.Elicit.RequestedSchema
				if err := app.RespondToAsk(int(ev.AskID), result, "web"); err != nil {
					t.Fatalf("RespondToAsk: %v", err)
				}
			}
		case <-ctx.Done():
			t.Fatal("no ask was raised")
		}
	}
	if err := <-turn; err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	return schema
}

// TestWebApproveRunsAPlainApproval is #124: the web UI's Approve sent no
// confirm field, which the host reads as a refusal.
func TestWebApproveRunsAPlainApproval(t *testing.T) {
	fx := loadAsk(t, "confirm")
	app, root := askingApp(t)
	if schema := answerAsWeb(t, app, fx.Result); !sameJSON(t, schema, fx.Schema) {
		t.Errorf("the host asked with %s, but the fixture the UI is tested against has %s", schema, fx.Schema)
	}
	if b, err := os.ReadFile(filepath.Join(root, "note.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("an approved write did not land: %q, %v", b, err)
	}
}

// TestApproveWithoutConfirmStillDeclines pins the host's side of the contract
// the old web answer broke: accept with no confirm is a refusal, so the fix
// belongs in what the surface sends.
func TestApproveWithoutConfirmStillDeclines(t *testing.T) {
	app, root := askingApp(t)
	answerAsWeb(t, app, core.ElicitationResult{Action: "accept"})
	if _, err := os.Stat(filepath.Join(root, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("a write accepted without confirm ran: %v", err)
	}
}

func TestWebPerHunkAnswerNarrowsTheEdit(t *testing.T) {
	fx := loadAsk(t, "hunks")
	const content = "alpha\nbeta\ngamma\n"
	app, root := askingAppWith(t, fakeModelCalling(t, "edit_file",
		`{"path":"f.txt","expect_hash":"`+files.Hash(content)+`","edits":[{"old":"alpha","new":"ALPHA"},{"old":"beta","new":"BETA"},{"old":"gamma","new":"GAMMA"}]}`))
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if schema := answerAsWeb(t, app, fx.Result); !sameJSON(t, schema, fx.Schema) {
		t.Errorf("the host asked with %s, but the fixture the UI is tested against has %s", schema, fx.Schema)
	}
	b, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil || string(b) != "ALPHA\nbeta\nGAMMA\n" {
		t.Fatalf("f.txt = %q, %v; want changes 1 and 3 only", b, err)
	}
}
