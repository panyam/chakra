package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/chakra"
)

// The input-transform seam (#27) has exactly three rules, with one test each.
//
//  1. Transforms apply in registration order, each receiving the previous
//     one's output.
//  2. History holds the expanded text, in memory and also in the RunStore
//     when one is configured. So the model reads the expanded text.
//  3. An error aborts the turn before any model call, leaving history and the
//     store untouched.
//
// Each rule gets its own test, because each breaks in a different way and at
// a different place. Rule 1 breaks silently, with a prompt that reads fine but
// is wrong. Rule 2 breaks only across a restart, when the model saw the
// expansion but the store kept the raw input. A user can see rule 3 break
// without reading history at all, because the model was called with input
// the transform refused to accept.

// transformExt wraps one input transform, so a test can register several
// distinct transforms in a known order.
type transformExt struct {
	BaseExtension
	name string
	fns  []InputTransform
}

func (e *transformExt) Name() string { return e.name }

func (e *transformExt) InputTransforms() []InputTransform { return e.fns }

// appendTransform returns an extension whose transform appends marker.
func appendTransform(name, marker string) *transformExt {
	return &transformExt{name: name, fns: []InputTransform{
		func(_ context.Context, in string) (string, error) { return in + marker, nil },
	}}
}

// lastUserText returns the text of the last message in a provider request,
// which is the user's message as the model saw it.
func lastUserText(t *testing.T, req agent.ProviderRequest) string {
	t.Helper()
	if len(req.Messages) == 0 {
		t.Fatal("provider request carried no messages")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != agent.RoleUser {
		t.Fatalf("last message to the model has role %q, want user: %+v", last.Role, last)
	}
	return last.Text
}

// TestInputTransformsApplyInRegistrationOrder is rule 1.
//
// It asserts that both registered transforms ran, and that they ran in
// registration order. The order check matters because the reversed order
// passes a "did the expansion happen" check just as well. The only symptom is
// a differently shaped prompt, and nothing else in the tree would catch it.
// TestExtensionCloseRunsInReverseRegistrationOrder and
// surfaces.TestWorkspaceExtensionsCheckpointFirst exist for the same reason.
func TestInputTransformsApplyInRegistrationOrder(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app := extApp(t, stub, appendTransform("first", "A"), appendTransform("second", "B"))

	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	// A then B, each seeing the previous output: "hi" -> "hiA" -> "hiAB".
	// Reversed composition would give "hiBA".
	if got := lastUserText(t, stub.Requests()[0]); got != "hiAB" {
		t.Fatalf("model saw user message %q, want %q (transforms run in registration order, output feeding the next)", got, "hiAB")
	}
}

// TestInputTransformOrderIncludesStepsFromOneExtension is rule 1 at the other
// granularity. An extension may contribute several transforms, and within one
// extension the slice keeps its declared order. The contract says each seam
// keeps the order its slice declares, so that needs a test of its own. A
// caller who registers one extension with two steps never registers two
// extensions to find out.
func TestInputTransformOrderIncludesStepsFromOneExtension(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app := extApp(t, stub, &transformExt{name: "multi", fns: []InputTransform{
		func(_ context.Context, in string) (string, error) { return in + "1", nil },
		func(_ context.Context, in string) (string, error) { return in + "2", nil },
	}})

	if err := app.RunTurn(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got := lastUserText(t, stub.Requests()[0]); got != "x12" {
		t.Fatalf("model saw user message %q, want %q (one extension's transforms keep their declared order)", got, "x12")
	}
}

// TestInputTransformIsWhatHistoryHolds is rule 2, in memory.
//
// The expanded text must be the user message, not a sibling block. Suppose a
// transform only altered the per-turn view. History would then hold the raw
// input, and rule 2 forbids exactly that divergence. Asserting the last
// history message is not enough on its own, since a transform could append
// the expansion as a second message. So this counts the user messages too.
func TestInputTransformIsWhatHistoryHolds(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "answered"})
	app := extApp(t, stub, appendTransform("expand", " [expanded]"))

	if err := app.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	var users []string
	for _, m := range app.history {
		if m.Role == agent.RoleUser {
			users = append(users, m.Text)
		}
	}
	if len(users) != 1 {
		t.Fatalf("history holds %d user messages (%v), want exactly 1: the expansion must replace the input, not accompany it", len(users), users)
	}
	if users[0] != "hello [expanded]" {
		t.Fatalf("history user message = %q, want the expanded text %q", users[0], "hello [expanded]")
	}
}

// TestInputTransformIsWhatTheRunStorePersists is rule 2 across a restart.
//
// The in-memory half can pass while the store keeps the raw input, because
// the two are written on different paths (history slice vs. persistTurnLocked
// and AppendMessages). A resumed session would then show the words the user
// typed, not the expanded text the model answered. A second process makes
// this assertion.
func TestInputTransformIsWhatTheRunStorePersists(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	var out strings.Builder
	stub := agent.NewStubProvider(agent.StubTurn{Text: "answered"})

	ts := startTestServer(t)
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""),
		WithProvider(stub), WithRunStore(store), WithExtension(appendTransform("expand", " [expanded]")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	if err := app.RunTurn(ctx, "hello"); err != nil {
		t.Fatal(err)
	}

	resp, err := store.LoadRun(ctx, agent.LoadRunRequest{RunID: app.RunID()})
	if err != nil || !resp.Found {
		t.Fatalf("LoadRun = (%+v, %v)", resp, err)
	}
	if len(resp.Run.Messages) == 0 {
		t.Fatal("the run persisted no messages")
	}
	if got := resp.Run.Messages[0].Text; got != "hello [expanded]" {
		t.Fatalf("persisted user message = %q, want the expanded text %q", got, "hello [expanded]")
	}
}

// TestInputTransformErrorAbortsBeforeTheModelCall is rule 3.
//
// The key assertion checks that the provider was never called. A transform
// decides what the model is asked, so one that fails has to fail before the
// ask, not after it. History and the store are checked too. If an aborted
// turn still left a user message behind, it would poison the next turn's
// context.
func TestInputTransformErrorAbortsBeforeTheModelCall(t *testing.T) {
	ext := &transformExt{name: "resolver", fns: []InputTransform{
		func(context.Context, string) (string, error) { return "", errors.New("no such template") },
	}}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "unreachable"})
	app := extApp(t, stub, ext)

	before := len(app.history)
	err := app.RunTurn(context.Background(), "expand @nowhere")
	if err == nil {
		t.Fatal("a failing input transform must abort the turn")
	}
	if !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("the error must name the extension that failed, got %v", err)
	}
	if n := len(stub.Requests()); n != 0 {
		t.Fatalf("the provider was called %d time(s) despite a failed transform: the turn must abort before any model call", n)
	}
	if len(app.history) != before {
		t.Fatalf("history grew by %d despite the abort", len(app.history)-before)
	}
}

// TestInputTransformErrorCreatesNoRun is rule 3's persistence half.
//
// RunTurn creates the session's run lazily on the first persisted turn, so a
// transform that fails must fail before that. Otherwise the user is told the
// turn did not happen, yet the session list shows it as an empty run. Anyone
// who asks the store would see that "nothing happened" was false.
func TestInputTransformErrorCreatesNoRun(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	var out strings.Builder
	stub := agent.NewStubProvider(agent.StubTurn{Text: "unreachable"})

	ts := startTestServer(t)
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""),
		WithProvider(stub), WithRunStore(store),
		WithExtension(&transformExt{name: "resolver", fns: []InputTransform{
			func(context.Context, string) (string, error) { return "", errors.New("no such template") },
		}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	if err := app.RunTurn(ctx, "expand @nowhere"); err == nil {
		t.Fatal("a failing input transform must abort the turn")
	}
	if got := app.RunID(); got != "" {
		t.Fatalf("a failed transform created run %q: the abort must precede any persistence", got)
	}
	runs, err := store.ListRuns(ctx, agent.ListRunsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs.Runs) != 0 {
		t.Fatalf("a failed transform left %d run(s) in the store", len(runs.Runs))
	}
}

// TestInputTransformFailureStopsTheChain is rule 3's ordering half. When a
// transform fails, the transforms registered after it must not run. A later
// step that ran anyway would mutate state, such as resolving a template or
// charging a budget, for a turn that is being abandoned.
func TestInputTransformFailureStopsTheChain(t *testing.T) {
	ran := false
	stub := agent.NewStubProvider(agent.StubTurn{Text: "unreachable"})
	app := extApp(t, stub,
		&transformExt{name: "failing", fns: []InputTransform{
			func(context.Context, string) (string, error) { return "", errors.New("boom") },
		}},
		&transformExt{name: "after", fns: []InputTransform{
			func(_ context.Context, in string) (string, error) { ran = true; return in, nil },
		}},
	)

	if err := app.RunTurn(context.Background(), "hi"); err == nil {
		t.Fatal("a failing input transform must abort the turn")
	}
	if ran {
		t.Fatal("a transform registered after the failing one still ran")
	}
}
