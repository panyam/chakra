package main

import (
	"context"
	"io"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// TestExerciseMatchesExpected is the exercise in CI: every check on both wires,
// against expectedFailures. It fails when a check regresses and also when one
// starts passing without its entry being removed.
func TestExerciseMatchesExpected(t *testing.T) {
	results, err := runAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if n := report(&out, results); n != 0 {
		t.Fatalf("%d result(s) differ from expected.go:\n%s", n, out.String())
	}
	t.Log("\n" + out.String())
}

// TestChecksCanTellPassFromFail flips each check with a config change that
// should change its answer, so a check that always passes, or always fails,
// shows up here rather than as a quietly wrong row in the table.
func TestChecksCanTellPassFromFail(t *testing.T) {
	cases := []struct {
		check string
		tweak func(*host.Config)
		want  bool
	}{
		// A server that never connects sends no instructions.
		{"instructions", func(cfg *host.Config) {
			cfg.Servers[0] = host.ServerConfig{ID: "demo", URL: "http://127.0.0.1:1/mcp"}
		}, false},
		{"no-prefetch", func(cfg *host.Config) { cfg.Servers[0].SkillsMode = "catalog" }, true},
		// Without remember every load asks, so the second load asks too.
		{"skill-rotation", func(cfg *host.Config) { cfg.Approval.Remember = false }, true},
		// The same template from config instead of _meta does render.
		{"event-hint", func(cfg *host.Config) {
			cfg.Servers[0].Events[0].Hint = &agent.ContextHint{Template: demoHintTemplate}
		}, true},
		{"agent-delegable", func(cfg *host.Config) {
			off := false
			cfg.Servers[0].Agents = &off
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.check, func(t *testing.T) {
			c := findCheck(t, tc.check)
			pass, detail, err := runCheck(context.Background(), c, tc.tweak)
			if err != nil {
				t.Fatal(err)
			}
			if pass != tc.want {
				t.Fatalf("pass = %v, want %v (%s)", pass, tc.want, detail)
			}
		})
	}
}

func findCheck(t *testing.T, id string) check {
	t.Helper()
	for _, c := range checks {
		if c.id == id {
			return c
		}
	}
	t.Fatalf("no check %q", id)
	return check{}
}
