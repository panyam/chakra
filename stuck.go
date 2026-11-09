package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// StepRecord is one step of a turn, as NewStuckDetector reads it (#278). It
// holds the assistant message from that step's model call, then the
// messages that came back before the next call. Those are tool results for
// a step that called tools, or a TurnEndHook's Continue message for a step
// that answered and was refused. A session stage's note or a signal note can
// follow them too.
type StepRecord struct {
	// Step counts from one within the turn.
	Step int
	// Assistant is what the model sent on that step.
	Assistant Message
	// Results are the messages that followed it, oldest first.
	Results []Message
}

// StuckKey is what NewStuckDetector compares across steps. Two steps with
// the same Call and Result count as a repeat. Steps whose Error is set and
// whose Result is the same count toward the same-error pattern, whatever
// their Call.
type StuckKey struct {
	// Call says what the model asked for.
	Call string
	// Result says what came back.
	Result string
	// Error says the step failed.
	Error bool
	// Tools names the tools the step called, for StuckFinding.Tools. Leave
	// it empty for a step with no tool call.
	Tools []string
}

// StuckKeyFunc keys one step, or returns false to leave it out. A step left
// out breaks every pattern running through it, the same as a step that did
// something new.
//
// The host decides what "the same" means. A test run's output differs in its
// timings, and a query's rows can come back in a different order, so a key
// for those strips the timings or sorts the rows. A host whose model acts
// through text instead of tool calls reads the action from
// StepRecord.Assistant.Text.
type StuckKeyFunc func(StepRecord) (StuckKey, bool)

// DefaultStuckKey keys a step that called tools. The call is each tool's name
// with its arguments as canonical JSON, so key order doesn't matter. The
// result is a hash of the tool results' text. A step fails when any of its
// tool calls failed, was refused, or reported an error. A step with no tool
// call is left out.
func DefaultStuckKey(rec StepRecord) (StuckKey, bool) {
	if len(rec.Assistant.ToolCalls) == 0 {
		return StuckKey{}, false
	}
	var k StuckKey
	var calls []string
	for _, tc := range rec.Assistant.ToolCalls {
		calls = append(calls, tc.Name+" "+canonicalArgs(tc.Args.Raw()))
		if !slices.Contains(k.Tools, tc.Name) {
			k.Tools = append(k.Tools, tc.Name)
		}
	}
	k.Call = strings.Join(calls, "\n")
	h := sha256.New()
	for _, m := range rec.Results {
		if m.Role != RoleTool {
			continue
		}
		h.Write([]byte(m.Text))
		h.Write([]byte{0})
		if strings.HasPrefix(m.Text, toolErrorPrefix) || strings.HasPrefix(m.Text, toolFailedPrefix) || strings.HasPrefix(m.Text, toolDeniedPrefix) {
			k.Error = true
		}
	}
	k.Result = hex.EncodeToString(h.Sum(nil))
	return k, true
}

func canonicalArgs(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// The patterns NewStuckDetector looks for, as StuckFinding.Pattern names
// them.
const (
	// StuckRepeat is the same call with the same result, step after step.
	StuckRepeat = "repeat"
	// StuckAlternate is two calls taking turns, A B A B, each with its own
	// unchanging result.
	StuckAlternate = "alternate"
	// StuckSameError is a run of failed steps with the same error, whatever
	// was called.
	StuckSameError = "same-error"
	// StuckWindow is the latest step's call and result coming back again
	// within the last few steps, whatever came between (#296). It catches a
	// cycle of three or more calls, which the other patterns miss.
	StuckWindow = "window"
)

// StuckFinding says what NewStuckDetector found. It travels on EventStuck,
// so every field is wire-safe.
type StuckFinding struct {
	// Pattern is StuckRepeat, StuckAlternate, StuckSameError or
	// StuckWindow.
	Pattern string `json:"pattern"`
	// Count is how many times it repeated: steps for a repeat, an error or
	// a window, and A B pairs for an alternation.
	Count int `json:"count"`
	// Window is how many recent steps a StuckWindow finding looked at. It is
	// zero for the other patterns.
	Window int `json:"window,omitempty"`
	// Steps are the turn's step numbers that make up the run, oldest first.
	// For a window they are the matching steps only, not the ones between.
	Steps []int `json:"steps"`
	// Tools names the tools in the run, from StuckKey.Tools.
	Tools []string `json:"tools,omitempty"`
	// Call is the latest step's StuckKey.Call, cut to 200 bytes.
	Call string `json:"call,omitempty"`
}

// String says what repeated, in a form the model and a log can both read.
func (f StuckFinding) String() string {
	what := "the same step"
	if len(f.Tools) > 0 {
		what = strings.Join(f.Tools, ", ")
	}
	switch f.Pattern {
	case StuckAlternate:
		return fmt.Sprintf("alternated between the same two calls (%s) %d times, with the same results each time", what, f.Count)
	case StuckSameError:
		return fmt.Sprintf("got the same error %d times in a row (%s)", f.Count, what)
	case StuckWindow:
		return fmt.Sprintf("tried %s %d times in the last %d steps, with the same result each time", what, f.Count, f.Window)
	default:
		return fmt.Sprintf("repeated %s %d times with the same result", what, f.Count)
	}
}

// StuckPolicy decides what to do about a finding, for the model call the
// detector runs before. It returns the view to send, or an error to end the
// turn. A ScopeRequest stage runs it, so a change to the view reaches that
// one call only.
type StuckPolicy func(ctx context.Context, info StepInfo, view StepView, f StuckFinding) (StepView, error)

// StuckConfig configures NewStuckDetector. The zero value is usable.
type StuckConfig struct {
	// Key keys each step. Nil means DefaultStuckKey.
	Key StuckKeyFunc
	// Repeats is how many steps in a row with the same key trip
	// StuckRepeat. Zero means 3, and a negative value turns it off.
	Repeats int
	// Alternations is how many A B pairs in a row trip StuckAlternate. Zero
	// means 3, and a negative value turns it off.
	Alternations int
	// Errors is how many failed steps in a row with the same Result trip
	// StuckSameError. Zero means 3, and a negative value turns it off.
	Errors int
	// Window is how many recent steps StuckWindow looks at. Zero leaves it
	// off, so a config that predates it behaves the same.
	Window int
	// WindowRepeats is how many times the latest step's key has to come up
	// within Window to trip StuckWindow, counting the latest step. Zero
	// means 3.
	WindowRepeats int
	// Policy acts on a finding. Nil means StuckNote().
	Policy StuckPolicy
}

// NewStuckDetector returns a ScopeRequest stage that finds a turn repeating
// itself (#278). Imagine a model that sends the same edit, gets the same
// failing test, and runs out of steps. Before each model call, the stage
// splits StepInfo.Turn into StepRecords and keys each one. Then it checks
// whether the most recent steps form a repeat, a run of the same error, an
// alternation, or with Window set the latest step coming back within the
// window, in that order. On a match it emits EventStuck through
// StepInfo.Emit and runs the policy.
//
// It reads only this turn, and only the steps at the end of it. Once the
// model does something new, the pattern breaks and the detector goes quiet,
// with no state to reset. While the pattern holds, it fires before every
// call, so a policy can escalate on StuckFinding.Count.
func NewStuckDetector(cfg StuckConfig) StepStage {
	key := cfg.Key
	if key == nil {
		key = DefaultStuckKey
	}
	policy := cfg.Policy
	if policy == nil {
		policy = StuckNote()
	}
	threshold := func(n int) int {
		if n == 0 {
			return 3
		}
		return n
	}
	repeats, alternations, errs := threshold(cfg.Repeats), threshold(cfg.Alternations), threshold(cfg.Errors)
	win := stuckWindow{size: cfg.Window, repeats: threshold(cfg.WindowRepeats)}
	return StepStage{
		Name:  "agent.stuck",
		Scope: ScopeRequest,
		Run: func(ctx context.Context, info StepInfo, view StepView) (StepView, error) {
			f, ok := detectStuck(stepRecords(info.Turn), key, repeats, alternations, errs, win)
			if !ok {
				return view, nil
			}
			info.Emit(Event{Kind: EventStuck, Step: info.Step, Stuck: &f})
			return policy(ctx, info, view, f)
		},
	}
}

// stepRecords splits a turn's record at each assistant message. Messages
// before the first one belong to no step and are dropped.
func stepRecords(turn []Message) []StepRecord {
	var out []StepRecord
	for _, m := range turn {
		if m.Role == RoleAssistant {
			out = append(out, StepRecord{Step: len(out) + 1, Assistant: m})
			continue
		}
		if len(out) > 0 {
			last := &out[len(out)-1]
			last.Results = append(last.Results, m)
		}
	}
	return out
}

type keyedStep struct {
	step int
	key  StuckKey
}

func (k keyedStep) same(o keyedStep) bool {
	return k.key.Call == o.key.Call && k.key.Result == o.key.Result
}

// stuckWindow is StuckConfig's Window and WindowRepeats. A size of zero or
// less leaves the pattern off.
type stuckWindow struct{ size, repeats int }

func detectStuck(recs []StepRecord, keyOf StuckKeyFunc, repeats, alternations, errs int, win stuckWindow) (StuckFinding, bool) {
	// Only the steps after the last one left out can form a pattern.
	var seg []keyedStep
	for _, r := range recs {
		k, ok := keyOf(r)
		if !ok {
			seg = seg[:0]
			continue
		}
		seg = append(seg, keyedStep{r.Step, k})
	}
	n := len(seg)
	if n == 0 {
		return StuckFinding{}, false
	}
	last := seg[n-1]
	tail := func(length int) []keyedStep { return seg[n-length:] }

	run := 1
	for run < n && seg[n-1-run].same(last) {
		run++
	}
	if repeats > 0 && run >= repeats {
		return finding(StuckRepeat, run, tail(run)), true
	}

	if errs > 0 && last.key.Error {
		run := 1
		for run < n && seg[n-1-run].key.Error && seg[n-1-run].key.Result == last.key.Result {
			run++
		}
		if run >= errs {
			return finding(StuckSameError, run, tail(run)), true
		}
	}

	if alternations > 0 && n >= 2 && !seg[n-2].same(last) {
		length := 2
		for length < n && seg[n-1-length].same(seg[n-1-length+2]) {
			length++
		}
		if pairs := length / 2; pairs >= alternations {
			return finding(StuckAlternate, pairs, tail(2*pairs)), true
		}
	}

	if win.size > 0 {
		var hits []keyedStep
		for _, s := range tail(min(win.size, n)) {
			if s.same(last) {
				hits = append(hits, s)
			}
		}
		if len(hits) >= win.repeats {
			f := finding(StuckWindow, len(hits), hits)
			f.Window = win.size
			return f, true
		}
	}
	return StuckFinding{}, false
}

func finding(pattern string, count int, steps []keyedStep) StuckFinding {
	f := StuckFinding{Pattern: pattern, Count: count}
	for _, s := range steps {
		f.Steps = append(f.Steps, s.step)
		for _, t := range s.key.Tools {
			if !slices.Contains(f.Tools, t) {
				f.Tools = append(f.Tools, t)
			}
		}
	}
	f.Call = steps[len(steps)-1].key.Call
	if len(f.Call) > 200 {
		f.Call = f.Call[:200]
	}
	return f
}

// StuckNote adds a system note to the call, naming what repeated and asking
// the model to change course. The note reaches that one call. EventStuck is
// what keeps a trace of it.
func StuckNote() StuckPolicy {
	return func(_ context.Context, _ StepInfo, v StepView, f StuckFinding) (StepView, error) {
		text := "You have " + f.String() + ". Doing it again will give the same result. Try a different approach, or stop and say what is blocking you."
		v.Messages = append(slices.Clip(v.Messages), Message{Role: RoleSystem, Text: text})
		return v, nil
	}
}

// StuckGeneration merges over onto the call's generation params, field by
// field, as TurnRequest.Generation merges onto RunnerConfig.Generation. Use
// it to raise the temperature or a repeat penalty for one call. At
// temperature 0 a new seed alone draws the same reply, so set Temperature or
// Penalties too.
func StuckGeneration(over GenerationParams) StuckPolicy {
	return func(_ context.Context, _ StepInfo, v StepView, _ StuckFinding) (StepView, error) {
		v.Generation = v.Generation.merge(over)
		return v, nil
	}
}

// StuckDropTools takes the finding's tools away for the call. A finding with
// no Tools, from a host key over text actions, leaves the tool set alone.
func StuckDropTools() StuckPolicy {
	return func(_ context.Context, _ StepInfo, v StepView, f StuckFinding) (StepView, error) {
		v.Tools = slices.DeleteFunc(slices.Clone(v.Tools), func(td core.ToolDef) bool { return slices.Contains(f.Tools, td.Name) })
		return v, nil
	}
}

// StuckRoute sends the call to p, named name in EventStep and
// TurnResult.UsageByModel. See StepView.Provider.
func StuckRoute(p Provider, name string) StuckPolicy {
	return func(_ context.Context, _ StepInfo, v StepView, _ StuckFinding) (StepView, error) {
		v.Provider, v.ProviderName = p, name
		return v, nil
	}
}

// StuckStop ends the turn before the call, with an error wrapping
// ErrTurnStopped that says what repeated, and a partial TurnResult.
func StuckStop() StuckPolicy {
	return func(_ context.Context, _ StepInfo, _ StepView, f StuckFinding) (StepView, error) {
		return StepView{}, fmt.Errorf("%w: the model %s", ErrTurnStopped, f)
	}
}

// StuckAll runs the policies in order, each on the view the last returned,
// and stops at the first error.
func StuckAll(policies ...StuckPolicy) StuckPolicy {
	return func(ctx context.Context, info StepInfo, v StepView, f StuckFinding) (StepView, error) {
		for _, p := range policies {
			var err error
			if v, err = p(ctx, info, v, f); err != nil {
				return StepView{}, err
			}
		}
		return v, nil
	}
}

// CollapseStuckSteps is a StuckPolicy that removes repeats from the view
// (#330). Imagine a small model that sends the same draft three times. Each
// copy and its result is still in the history, so on the next call the model
// copies its last turn again, whatever note follows. This policy keeps the
// first of those steps and drops the later ones whose key matches an earlier
// step in the finding, so the model sees what it tried once.
//
// key must be the detector's key, so "the same" means the same thing to
// both. Nil means DefaultStuckKey. A same-error finding over different calls
// drops nothing, since each call there is a different attempt.
//
// It changes the view only. The record keeps every step, and so do
// TurnResult and the RunStore. A dropped step goes with its results, so no
// tool result is left without its call. A step the view no longer holds,
// such as one compaction summarized, is skipped. The policy adds no note.
// Pair it with one, as the graph-query preset's FirstStuck does.
//
// Removing messages from the middle of the history ends a prompt cache at
// that point. llama.cpp then re-reads the rest of the prompt, and a
// Responses connection sends the full history for that call.
func CollapseStuckSteps(key StuckKeyFunc) StuckPolicy {
	if key == nil {
		key = DefaultStuckKey
	}
	return func(_ context.Context, info StepInfo, v StepView, f StuckFinding) (StepView, error) {
		recs := stepRecords(info.Turn)
		drop := map[int]bool{}
		var kept []StuckKey
		for _, s := range f.Steps {
			if s < 1 || s > len(recs) {
				continue
			}
			k, ok := key(recs[s-1])
			if !ok {
				continue
			}
			if slices.ContainsFunc(kept, func(o StuckKey) bool { return o.Call == k.Call && o.Result == k.Result }) {
				drop[s] = true
				continue
			}
			kept = append(kept, k)
		}
		if len(drop) == 0 {
			return v, nil
		}
		remove := make([]bool, len(v.Messages))
		removed := false
		// Match the turn's steps from the end of the view, since an earlier
		// turn can hold the same text as one of this turn's steps.
		i := len(v.Messages) - 1
		for s := len(recs); s >= 1 && i >= 0; s-- {
			rec := recs[s-1]
			at := -1
			for j := i; j >= 0; j-- {
				if sameSentMessage(v.Messages[j], rec.Assistant) {
					at = j
					break
				}
			}
			if at < 0 {
				continue
			}
			i = at - 1
			if !drop[s] || at+len(rec.Results) >= len(v.Messages) {
				continue
			}
			match := true
			for r, m := range rec.Results {
				if !sameSentMessage(v.Messages[at+1+r], m) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			for j := at; j <= at+len(rec.Results); j++ {
				remove[j] = true
			}
			removed = true
		}
		if !removed {
			return v, nil
		}
		msgs := make([]Message, 0, len(v.Messages))
		for j, m := range v.Messages {
			if !remove[j] {
				msgs = append(msgs, m)
			}
		}
		v.Messages = msgs
		return v, nil
	}
}

// sameSentMessage compares what a provider would send for two messages,
// tool call IDs and arguments included, leaving out the timestamp.
func sameSentMessage(a, b Message) bool {
	if a.Role != b.Role || a.Text != b.Text || a.ToolCallID != b.ToolCallID || len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.ToolCalls {
		x, y := a.ToolCalls[i], b.ToolCalls[i]
		if x.ID != y.ID || x.Name != y.Name || string(x.Args.Raw()) != string(y.Args.Raw()) {
			return false
		}
	}
	return true
}
