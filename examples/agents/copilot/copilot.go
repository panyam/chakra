// Package copilot is a co-pilot inside a small kanban web app (#359). The
// user works on the board, and the agent sees what they do, reacts, and
// moves cards itself. It runs inside a host.App, as agentweb would serve
// it, so the user's actions reach it as events through App.Deliver, and a
// trigger can start a turn nobody typed. The page is a fake in Go, and a
// case is a user session played against it: messages, actions between
// turns, and actions timed to land while the agent works.
package copilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/chakra/host"
)

// Instructions is the agent's system prompt on every step.
const Instructions = "You are the co-pilot in a kanban board app. The board has three columns: To do, Doing and Done. Help the user with their board, and keep replies short."

// PageRules is what the page-tools step adds to the prompt. The browser
// page in web/ uses it too.
const PageRules = `
You can read the board and change it with your tools. Read the board before you change it.`

// DoneCheck is the trigger's instructions, the proactive turn's prompt.
// The browser page in web/ uses it too.
const DoneCheck = "A card just landed in Done. Read the board and check that card's checklist. If any item is still open, move the card back to Doing and highlight it, and tell the user which items are open. If every item is done, leave the card where it is."

// session is one case: what the user says and does, and what the board
// should look like afterwards.
type session struct {
	name  string
	beats []beat
	timed []timed
	want  string
	check func(*board) bool
	// trigger says the case needs the done-check trigger to fire.
	trigger bool
}

// beat is one thing the user does between turns: say something, which runs
// a turn, or act on the page.
type beat struct {
	say string
	do  *action
}

func say(s string) beat { return beat{say: s} }

func move(id, to string) beat { return beat{do: &action{Kind: "move", Card: id, To: to}} }

func rename(id, to string) beat { return beat{do: &action{Kind: "rename", Card: id, To: to}} }

func in(b *board, id, col string) bool { c, ok := b.get(id); return ok && c.Column == col }

var sessions = []session{
	{name: `Move "Plan offsite" to Doing.`, beats: []beat{say(`Move "Plan offsite" to Doing.`)},
		want: "C5 in Doing", check: func(b *board) bool { return in(b, "C5", "Doing") }},
	{name: `Add a card "Write release notes" to To do.`, beats: []beat{say(`Add a card "Write release notes" to To do.`)},
		want: `a "Write release notes" card in To do`, check: func(b *board) bool {
			b.mu.Lock()
			defer b.mu.Unlock()
			return slices.ContainsFunc(b.cards, func(c *card) bool {
				return strings.EqualFold(c.Title, "Write release notes") && c.Column == "To do"
			})
		}},
	{name: "(moves C3 to Done) Highlight the card I just finished.", beats: []beat{move("C3", "Done"), say("Highlight the card I just finished.")},
		want: "C3 highlighted", check: func(b *board) bool { return b.highlighted("C3") }},
	{name: "(renames C5) Put the card I just renamed into Doing.", beats: []beat{rename("C5", "Plan team offsite"), say("Put the card I just renamed into Doing.")},
		want: "C5 in Doing", check: func(b *board) bool { return in(b, "C5", "Doing") }},
	{name: "(moves C2, with open items, to Done)", beats: []beat{move("C2", "Done")}, trigger: true,
		want: "C2 back in Doing and highlighted", check: func(b *board) bool { return in(b, "C2", "Doing") && b.highlighted("C2") }},
	{name: "(moves C1, all items done, to Done)", beats: []beat{move("C1", "Done")}, trigger: true,
		want: "C1 left in Done", check: func(b *board) bool { return in(b, "C1", "Done") }},
	{name: "Move every card in Doing to Done. (the user takes C2 back mid-turn)", beats: []beat{say("Move every card in Doing to Done.")},
		timed: []timed{{After: "read_board", N: 1, Do: action{Kind: "move", Card: "C2", To: "To do"}}},
		want:  "C1 and C3 in Done, C2 left in To do", check: func(b *board) bool {
			return in(b, "C1", "Done") && in(b, "C3", "Done") && in(b, "C2", "To do")
		}},
	{name: `Rename C7 to "Q3 launch plan". (the user renames it as the agent does)`, beats: []beat{say(`Rename C7 to "Q3 launch plan".`)},
		timed: []timed{{Before: "rename_card", N: 1, Do: action{Kind: "rename", Card: "C7", To: "Launch plan v2"}}},
		want:  `C7 keeps the user's title "Launch plan v2"`, check: func(b *board) bool { c, _ := b.get("C7"); return c.Title == "Launch plan v2" }},
}

// wiring is what a step turns on in the host, beyond the Runner's config.
type wiring struct {
	// events sends the user's page actions to the App as events.
	events bool
	// triggers adds the done-check trigger.
	triggers bool
	// critical marks the page's events priority "critical", so the host
	// passes one the user sends mid-turn before the agent's next model call.
	critical bool
}

// Ladder is copilot's six steps over its eight sessions.
func Ladder(context.Context) (eval.Ladder, func(), error) {
	b := &board{}
	b.reset(nil)
	tools, err := b.tools()
	if err != nil {
		return eval.Ladder{}, nil, err
	}
	var cs []eval.SuiteCase
	for _, s := range sessions {
		s := s
		cs = append(cs, eval.SuiteCase{
			Scenario: eval.Scenario{Name: s.name},
			Scorers: func(agent.Provider) []eval.Scorer {
				return []eval.Scorer{boardScore(b, s), keptScore(b), triggerScore(b, s)}
			},
		})
	}
	return eval.Ladder{
		Name: "copilot",
		Steps: []eval.Step{
			{Name: "chat only", Driver: b.driver(wiring{})},
			{Name: "+ page tools", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.Tools = tools
				cfg.Instructions += "\n" + PageRules
				return cfg, nil
			}},
			{Name: "+ page events", Driver: b.driver(wiring{events: true})},
			{Name: "+ trigger", Driver: b.driver(wiring{events: true, triggers: true})},
			{Name: "+ critical events", Driver: b.driver(wiring{events: true, triggers: true, critical: true})},
			{Name: "+ stale-edit guard", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.ToolMiddleware = append(slices.Clip(cfg.ToolMiddleware), b.staleGuard)
				return cfg, nil
			}},
		},
		Cases: cs,
	}, func() {}, nil
}

// runnerConfig hands a step's RunnerConfig to the App as an extension:
// its tools, middleware, step stages and turn-end hooks. The page clock
// goes first in the middleware, so a timed user action lands before any
// step's middleware sees the call.
type runnerConfig struct {
	host.BaseExtension
	cfg   agent.RunnerConfig
	clock agent.ToolMiddleware
}

func (runnerConfig) Name() string { return "copilot" }

func (r runnerConfig) Tools() (agent.ToolSource, error) { return r.cfg.Tools, nil }

func (r runnerConfig) Middleware() []agent.ToolMiddleware {
	return append([]agent.ToolMiddleware{r.clock}, r.cfg.ToolMiddleware...)
}

func (r runnerConfig) StepStages() []agent.StepStage { return r.cfg.StepStages }

func (r runnerConfig) TurnEndHooks() []agent.TurnEndHook { return r.cfg.TurnEnd }

// driver runs a session inside a host.App built from the step's config and
// w. Each turn the App runs, typed or started by the trigger, becomes one
// Result, in order.
func (b *board) driver(w wiring) eval.Driver {
	return func(ctx context.Context, cfg agent.RunnerConfig, sc eval.Scenario) ([]eval.Result, error) {
		i := slices.IndexFunc(sessions, func(s session) bool { return s.name == sc.Name })
		if i < 0 {
			return nil, fmt.Errorf("copilot: no session %q", sc.Name)
		}
		s := sessions[i]
		b.reset(s.timed)
		hc := &host.Config{Instructions: cfg.Instructions, MaxSteps: cfg.MaxSteps, Servers: []host.ServerConfig{}}
		if sc.Instructions != "" {
			hc.Instructions = sc.Instructions
		}
		if w.triggers {
			hc.Triggers = []host.TriggerConfig{{Server: "page", Event: "card.moved", Filter: map[string]string{"to": "Done"},
				Instructions: DoneCheck, Label: "done-check"}}
		}
		rec := &recorder{name: sc.Name, b: b}
		opts := []host.AppOption{host.WithProvider(cfg.Provider), host.WithObserver(rec)}
		if cfg.Tools != nil || len(cfg.ToolMiddleware) > 0 || len(cfg.StepStages) > 0 || len(cfg.TurnEnd) > 0 {
			opts = append(opts, host.WithExtension(runnerConfig{cfg: cfg, clock: b.clock}))
		}
		app, err := host.NewApp(hc, io.Discard, strings.NewReader(""), opts...)
		if err != nil {
			return nil, fmt.Errorf("copilot: %w", err)
		}
		defer app.Close()
		if w.events {
			b.mu.Lock()
			// deliver waits until the App has taken the event in. Deliver only
			// queues it, so without the wait a timed action could miss the
			// model call it was timed for. A timed move to Done would fire the
			// trigger, whose turn waits for the running one, so the wait has
			// a cap.
			b.deliver = func(a action, before card) {
				_ = app.Deliver(ctx, event(a, before, w.critical))
				wait, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				_ = app.Settle(wait)
			}
			b.mu.Unlock()
		}
		for _, bt := range s.beats {
			if bt.do != nil {
				b.userDo(*bt.do)
			}
			if bt.say != "" {
				// A failed turn is in its Result, from HostTurnFailed.
				_ = app.RunTurn(ctx, bt.say)
			}
			// Events the user's action or the turn sent may start a
			// proactive turn. Settle waits for it, so the next beat sees
			// the board it left.
			if err := app.Settle(ctx); err != nil {
				return rec.results(), err
			}
		}
		return rec.results(), nil
	}
}

// recorder turns the App's events into one eval.Result per turn, and tells
// the board when a turn starts and ends.
type recorder struct {
	name string
	b    *board

	mu   sync.Mutex
	done []eval.Result
	cur  *eval.Result
}

func (r *recorder) On(e host.HostEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e.Kind {
	case host.HostUserInput:
		if e.Label == "steer" {
			return
		}
		r.begin(e.Message)
	case host.HostTriggerFired:
		r.b.mu.Lock()
		r.b.fired++
		r.b.mu.Unlock()
		r.begin("(trigger: " + e.Label + ")")
	case host.HostRunnerEvent:
		if r.cur != nil {
			r.cur.Events = append(r.cur.Events, e.RunnerEvent)
		}
	case host.HostTurnDone:
		if r.cur != nil {
			r.cur.Turn = e.Result
		}
		r.end()
	case host.HostTurnFailed:
		if r.cur != nil {
			r.cur.Err = errors.New(e.Err)
		}
		r.end()
	}
}

func (r *recorder) begin(input string) {
	r.cur = &eval.Result{Case: eval.Case{Name: fmt.Sprintf("%s#%d", r.name, len(r.done)+1), Input: input}}
	r.b.turnStarted()
}

func (r *recorder) end() {
	if r.cur != nil {
		r.done = append(r.done, *r.cur)
		r.cur = nil
	}
	r.b.turnEnded()
}

// results is every turn, in order. A session with no turns, such as one
// whose trigger never fired, gets one empty Result, so the scorers still
// see the case.
func (r *recorder) results() []eval.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.done)
	if len(out) == 0 {
		out = append(out, eval.Result{Case: eval.Case{Name: r.name + "#0"}, Turn: &agent.TurnResult{}})
	}
	return out
}

type scorer func(eval.Result) eval.Score

func (f scorer) Score(r eval.Result) eval.Score { return f(r) }

func pass(name string, ok bool, detail, want string) eval.Score {
	s := eval.Score{Name: name, Pass: ok, Detail: detail, Want: want}
	if ok {
		s.Value = 1
	}
	return s
}

// boardScore grades the board the session left.
func boardScore(b *board, s session) eval.Scorer {
	return scorer(func(eval.Result) eval.Score {
		return pass("Board", s.check(b), b.state(), s.want)
	})
}

// keptScore fails when the agent edited a field the user changed while the
// agent was working.
func keptScore(b *board) eval.Scorer {
	return scorer(func(eval.Result) eval.Score {
		b.mu.Lock()
		over := slices.Clone(b.overwrites)
		b.mu.Unlock()
		detail := "no user edit overwritten"
		if len(over) > 0 {
			detail = "overwrote the user's change to " + strings.Join(over, ", ")
		}
		return pass("KeptUserEdits", len(over) == 0, detail, "no user edit overwritten")
	})
}

// triggerScore fails when a session that needs the trigger didn't get it,
// or when it fired more often than the user moved a card to Done.
func triggerScore(b *board, s session) eval.Scorer {
	moves := 0
	for _, bt := range s.beats {
		if bt.do != nil && bt.do.Kind == "move" && bt.do.To == "Done" {
			moves++
		}
	}
	want := fmt.Sprintf("at most %d firings", moves)
	if s.trigger {
		want = "exactly 1 firing"
	}
	return scorer(func(eval.Result) eval.Score {
		b.mu.Lock()
		fired := b.fired
		b.mu.Unlock()
		ok := fired <= moves && (!s.trigger || fired == 1)
		return pass("Triggers", ok, fmt.Sprintf("%d firings", fired), want)
	})
}
