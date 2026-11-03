package git

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

const findingsJSON = `{"findings":[{"title":"Second line lost its meaning","body":"two became TWO, so callers matching on two break.","file":"a.txt","line_start":2,"line_end":2,"priority":1,"confidence":0.8}],"verdict":"needs changes","summary":"One behaviour change."}`

// reviewed is a Source with review on, lent a stub provider whose child
// answers once and then finalizes into findingsJSON, recording the child's
// events.
func reviewed(t *testing.T, dir string) (*Source, *agent.StubProvider, *[]agent.SubAgentEvent) {
	t.Helper()
	s := source(t, Config{Root: dir, Review: &ReviewConfig{}})
	stub := agent.NewStubProvider(agent.StubTurn{Text: "one finding"}, agent.StubTurn{Text: findingsJSON})
	var events []agent.SubAgentEvent
	s.rev.host = host.SubAgentHost{Provider: stub, ReadOnlyTools: s, OnEvent: func(e agent.SubAgentEvent) { events = append(events, e) }}
	return s, stub, &events
}

// The review tool hands the target's diff to a fresh reviewer as its task,
// with the rubric as its instructions and read-only tools other than review
// itself, and returns the reviewer's structured findings intact.
func TestReviewHandsTheDiffToAFreshReviewer(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "one\nTWO\n")
	s, stub, events := reviewed(t, dir)
	res, err := s.Call(context.Background(), "review", map[string]any{"instructions": "watch the second line"})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	var got Review
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil || len(got.Findings) != 1 || got.Findings[0].Priority != 1 || got.Findings[0].File != "a.txt" || got.Verdict != "needs changes" {
		t.Errorf("findings came back as %s (%v)", res.Content[0].Text, err)
	}
	if res.StructuredContent == nil {
		t.Errorf("no structured content")
	}
	first := stub.Requests()[0]
	task := first.Messages[0].Text
	for _, want := range []string{"Review the uncommitted changes in the workspace.", "Also: watch the second line", "-two\n+TWO", "<diff>"} {
		if !strings.Contains(task, want) {
			t.Errorf("the reviewer's task lacks %q:\n%s", want, task)
		}
	}
	if len(first.Messages) != 1 || first.Instructions != DefaultRubric {
		t.Errorf("the reviewer didn't start fresh with the rubric: %d messages", len(first.Messages))
	}
	var offered []string
	for _, d := range first.Tools {
		offered = append(offered, d.Name)
	}
	if got := strings.Join(offered, " "); got != "git_status git_diff git_log" {
		t.Errorf("the reviewer was offered %s", got)
	}
	if fin := stub.Requests()[1]; fin.ResponseSchema.Len() == 0 {
		t.Errorf("the reviewer's answer wasn't held to the findings schema")
	}
	if len(*events) == 0 || (*events)[0].Scope != "review" {
		t.Errorf("the reviewer's events didn't reach the host as scope review: %+v", *events)
	}
}

// A base or commit target reaches the reviewer as that diff and is named in
// its task.
func TestReviewOfABaseAndOfACommit(t *testing.T) {
	dir := repo(t)
	sh(t, dir, "switch", "-q", "-c", "feature")
	write(t, dir, "lib/b.go", "package lib\n\nvar Feature = true\n")
	sh(t, dir, "commit", "-qam", "feature")
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"base": "main"}, "since it forked from main"},
		{map[string]any{"commit": "HEAD"}, "Review commit HEAD."},
	} {
		s, stub, _ := reviewed(t, dir)
		if res, err := s.Call(context.Background(), "review", c.args); err != nil || res.IsError {
			t.Fatalf("%v: %v %+v", c.args, err, res)
		}
		task := stub.Requests()[0].Messages[0].Text
		if !strings.Contains(task, c.want) || !strings.Contains(task, "+var Feature = true") {
			t.Errorf("%v: task\n%s", c.args, task)
		}
	}
}

// Nothing to review asks no model; no provider is an error the model reads;
// a reviewer that answers outside the schema is too.
func TestReviewWithNothingToReviewOrNoProvider(t *testing.T) {
	dir := repo(t)
	s, stub, _ := reviewed(t, dir)
	res, err := s.Call(context.Background(), "review", nil)
	if err != nil || res.IsError || !strings.Contains(res.Content[0].Text, "nothing to review") || len(stub.Requests()) != 0 {
		t.Errorf("a clean tree: %v %+v, %d model calls", err, res, len(stub.Requests()))
	}
	write(t, dir, "a.txt", "changed\n")
	s.rev.host = host.SubAgentHost{}
	if res, _ := s.Call(context.Background(), "review", nil); !res.IsError || !strings.Contains(res.Content[0].Text, "provider") {
		t.Errorf("no provider: %+v", res)
	}
	s.rev.host = host.SubAgentHost{Provider: agent.NewStubProvider(agent.StubTurn{Text: "fine"}, agent.StubTurn{Text: `{"verdict":"correct"`})}
	if _, _, err := s.rev.review(context.Background(), DiffTarget{}, ""); err == nil {
		t.Errorf("a broken answer was taken as a review")
	}
}

func TestReviewIsOffOrOnByConfig(t *testing.T) {
	dir := repo(t)
	got, _ := source(t, Config{Root: dir}).Tools(context.Background())
	if strings.Contains(names(got), "review") {
		t.Errorf("review offered without Config.Review")
	}
	x, err := New(Config{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range x.Commands() {
		if c.Name == "review" {
			t.Errorf("/review registered without Config.Review")
		}
	}
	got, _ = source(t, Config{Root: dir, Review: &ReviewConfig{}}).Tools(context.Background())
	if !strings.HasSuffix(names(got), "review") {
		t.Errorf("review not offered: %s", names(got))
	}
}

// /review takes base or commit and instructions after --, and shows the
// findings for a person.
func TestReviewCommand(t *testing.T) {
	for in, want := range map[string]DiffTarget{"": {}, "base main": {Base: "main"}, "commit HEAD~1 -- look at errors": {Commit: "HEAD~1"}} {
		got, _, err := parseReviewArgs(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	if _, instr, _ := parseReviewArgs("base main -- check the loop"); instr != "check the loop" {
		t.Errorf("instructions %q", instr)
	}
	if _, _, err := parseReviewArgs("main"); err == nil {
		t.Errorf("a bare ref was accepted")
	}
	dir := repo(t)
	write(t, dir, "a.txt", "one\nTWO\n")
	x, err := New(Config{Root: dir, Review: &ReviewConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	x.UseSubAgents(host.SubAgentHost{Provider: agent.NewStubProvider(agent.StubTurn{Text: "ok"}, agent.StubTurn{Text: findingsJSON})})
	res, err := x.reviewCommand(context.Background(), "")
	if err != nil || !strings.Contains(res.Message, "review: needs changes. One behaviour change.") || !strings.Contains(res.Message, "[P1] Second line lost its meaning (a.txt:2-2, confidence 0.80)") {
		t.Errorf("%v\n%s", err, res.Message)
	}
}
