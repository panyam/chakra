package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Exit codes for agentchat exec, so a script can tell the failures apart.
const (
	exitTurnFailed    = 1
	exitMaxSteps      = 2
	exitSchemaInvalid = 3
	exitTurnStopped   = 4
	exitStartFailed   = 5
	exitUsage         = 64
)

// exitError carries an exit code out of a cobra RunE.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// execApprovalModes are the modes exec accepts. The host falls back to "ask"
// on an unknown mode, which for a run nobody can answer means every gated
// call is denied, so a typo is refused here instead.
var execApprovalModes = map[string]bool{
	"ask": true, "read-only-auto": true, "reversible-auto": true, "allow": true,
}

// newExecCmd is `agentchat exec`: one task, no terminal, for CI jobs and
// scripts. It inherits the root's persistent flags (config, workspace,
// session store and the rest) and adds the run's own.
func newExecCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec [prompt]",
		Short: "run one task headless and exit",
		Long: "exec runs a single task without a terminal UI and exits. The prompt comes from -p, --prompt-file, the arguments, or stdin.\n" +
			"Nobody can answer an approval prompt, so --approve is required, and a call the mode would ask about is denied.\n" +
			"Every configured MCP server must be connected when the turn starts, or exec exits 5.\n" +
			"With --json, a run-start frame comes first, a cost frame follows each model call, and a summary frame comes last with the run's totals and exit code.\n" +
			"Exit codes: 0 done, 1 the turn failed, 2 it hit the step limit, 3 --output-schema was set and no answer matched it, 4 a hook stopped the turn, 5 the model connection or an MCP server could not start, 64 bad usage.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(cmd.Context(), v, args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	fl := cmd.Flags()
	fl.StringP("prompt", "p", "", "the task")
	fl.String("prompt-file", "", "read the task from this file")
	fl.Bool("json", false, "write every event to stdout as JSON lines ({kind, payload}, the frames agentweb's Watch sends) instead of only the final message")
	fl.String("output-schema", "", "a JSON Schema file: the final answer becomes a JSON document valid against it")
	fl.String("output-last-message", "", "also write the final message to this file")
	fl.String("approve", "", "approval mode for the run: ask (every gated call denied), read-only-auto, reversible-auto or allow (required)")
	for _, name := range []string{"prompt", "prompt-file", "json", "output-schema", "output-last-message", "approve"} {
		if err := v.BindPFlag("exec-"+name, fl.Lookup(name)); err != nil {
			panic(err)
		}
	}
	return cmd
}

func runExec(ctx context.Context, v *viper.Viper, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	usage := func(format string, a ...any) error {
		return &exitError{code: exitUsage, err: fmt.Errorf(format, a...)}
	}
	mode := v.GetString("exec-approve")
	if !execApprovalModes[mode] {
		return usage("exec needs --approve ask|read-only-auto|reversible-auto|allow (got %q)", mode)
	}
	prompt, err := execPrompt(v, args, stdin)
	if err != nil {
		return usage("%v", err)
	}
	var schema core.RawJSON
	if path := v.GetString("exec-output-schema"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return usage("--output-schema: %v", err)
		}
		if !json.Valid(b) {
			return usage("--output-schema %s is not JSON", path)
		}
		schema = core.NewRawJSON(json.RawMessage(b))
	}

	out := &execOutput{w: stdout, jsonl: v.GetBool("exec-json")}
	err = runExecTurn(ctx, v, mode, prompt, schema, out, stderr)
	out.writeSummary(err)
	return err
}

// runExecTurn builds the App and runs the turn. Its error carries the exit
// code, and runExec reports that code in the summary frame.
func runExecTurn(ctx context.Context, v *viper.Viper, mode, prompt string, schema core.RawJSON, out *execOutput, stderr io.Writer) error {
	stdout := out.w
	surface := []host.AppOption{
		host.WithObserver(out),
		host.WithElicitationUI(out.deny),
	}
	setup, err := buildApp(ctx, v, surface, func(cfg *host.Config) error {
		if cfg.Approval == nil {
			cfg.Approval = &host.ApprovalConfig{}
		}
		cfg.Approval.Mode = mode
		return nil
	}, io.Discard, strings.NewReader(""))
	if err != nil {
		return &exitError{code: exitStartFailed, err: err}
	}
	defer setup.close()
	app := setup.app
	if err := serversReady(ctx, app, setup.cfg.Servers); err != nil {
		return &exitError{code: exitStartFailed, err: err}
	}
	out.start(app, setup.cfg, mode)

	if session := v.GetString("session"); session != "" {
		if setup.store == nil {
			return &exitError{code: exitUsage, err: errors.New("--session needs --session-store")}
		}
		if err := app.AttachRun(ctx, session); err != nil {
			return &exitError{code: exitStartFailed, err: err}
		}
	}

	turnErr := app.RunTurnWith(ctx, prompt, host.TurnOptions{ResponseSchema: schema})
	if id := app.RunID(); id != "" {
		fmt.Fprintf(stderr, "agentchat exec: session %s\n", id)
	}
	result := out.result()

	final := ""
	if result != nil {
		final = result.Text
		if schema.Len() > 0 {
			final = string(result.Structured.Raw())
		}
	}
	if !out.jsonl && final != "" {
		fmt.Fprintln(stdout, final)
	}
	if path := v.GetString("exec-output-last-message"); path != "" && result != nil {
		if err := os.WriteFile(path, []byte(final), 0o644); err != nil {
			return &exitError{code: exitTurnFailed, err: err}
		}
	}

	return execExit(turnErr, result, schema.Len() > 0)
}

// serversReady fails when a configured MCP server is not connected once the
// App is built. NewApp waits for the first connect round, so a server that
// is down has failed by then. A server not marked required would connect in
// the background, and the run would answer without its tools. A benchmark
// would then score a run that never saw them.
func serversReady(ctx context.Context, app *host.App, servers []host.ServerConfig) error {
	var down []string
	for _, sc := range servers {
		if _, found, _ := app.ServerTools(ctx, sc.ID); !found {
			down = append(down, sc.ID)
		}
	}
	if len(down) > 0 {
		return fmt.Errorf("MCP server(s) not connected: %s", strings.Join(down, ", "))
	}
	return nil
}

// execExit maps how the turn ended to exec's exit code, or nil for 0.
func execExit(turnErr error, result *agent.TurnResult, wantSchema bool) error {
	switch {
	case errors.Is(turnErr, agent.ErrMaxSteps):
		return &exitError{code: exitMaxSteps, err: turnErr}
	case errors.Is(turnErr, agent.ErrTurnStopped):
		return &exitError{code: exitTurnStopped, err: turnErr}
	case turnErr != nil:
		return &exitError{code: exitTurnFailed, err: turnErr}
	case wantSchema && (result == nil || result.Structured.Len() == 0):
		return &exitError{code: exitSchemaInvalid, err: errors.New("no structured answer")}
	case wantSchema && result.StructuredError != "":
		return &exitError{code: exitSchemaInvalid, err: fmt.Errorf("the answer doesn't match --output-schema: %s", result.StructuredError)}
	}
	return nil
}

// execPrompt reads the task from -p, --prompt-file, the arguments or stdin,
// in that order.
func execPrompt(v *viper.Viper, args []string, stdin io.Reader) (string, error) {
	var p string
	switch {
	case v.GetString("exec-prompt") != "":
		p = v.GetString("exec-prompt")
	case v.GetString("exec-prompt-file") != "":
		b, err := os.ReadFile(v.GetString("exec-prompt-file"))
		if err != nil {
			return "", fmt.Errorf("--prompt-file: %w", err)
		}
		p = string(b)
	case len(args) > 0:
		p = strings.Join(args, " ")
	default:
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading the prompt from stdin: %w", err)
		}
		p = string(b)
	}
	if strings.TrimSpace(p) == "" {
		return "", errors.New("no prompt: pass -p, --prompt-file, an argument, or pipe it on stdin")
	}
	return p, nil
}

// execOutput is exec's surface. It writes events as JSON lines when asked,
// remembers the turn's result, and denies every approval ask.
type execOutput struct {
	mu    sync.Mutex
	w     io.Writer
	jsonl bool
	last  *agent.TurnResult

	// conns and active price each model call. They are set once the App
	// exists, before the turn starts.
	conns  map[string]host.ConnectionConfig
	active string
	tally  execTally
	// mainUsage sums the main agent's step events, so the summary can add
	// what TurnResult counted beyond them: the structured finalize call.
	mainUsage agent.Usage
}

// execTally is the summary frame's payload: the run's totals for a
// benchmark driver, which reads one line instead of folding the stream.
type execTally struct {
	// ModelCalls counts every model call in the run, sub-agents included.
	ModelCalls int `json:"modelCalls"`
	// Steps is the main agent's TurnResult.Steps.
	Steps int `json:"steps"`
	// ToolCalls counts the tool calls the model made, sub-agents included,
	// and ToolCallsByName splits them by tool name. Each one emits
	// tool-begin before the approval gate, so these counts include the
	// calls the gate refused. ToolCallsDenied counts those.
	ToolCalls       int            `json:"toolCalls"`
	ToolCallsByName map[string]int `json:"toolCallsByName,omitempty"`
	ToolCallsDenied int            `json:"toolCallsDenied"`
	// Usage sums every model call's tokens, and UsageByConnection splits
	// them by the connection that answered.
	Usage             agent.Usage            `json:"usage"`
	UsageByConnection map[string]agent.Usage `json:"usageByConnection,omitempty"`
	// USD sums the cost of the priced calls. It is null when no call was
	// priced, and UnpricedCalls counts the calls it leaves out.
	USD           *float64 `json:"usd"`
	UnpricedCalls int      `json:"unpricedCalls"`
	ExitCode      int      `json:"exitCode"`
}

// execCost is the cost frame's payload, one per model call.
type execCost struct {
	Step       int    `json:"step"`
	Scope      string `json:"scope,omitempty"`
	Connection string `json:"connection,omitempty"`
	// USD is null when the connection has no price or the provider
	// reported no usage.
	USD *float64 `json:"usd"`
}

// start records what prices the run, and writes the run-start frame. That
// frame names what the run used, so a benchmark log shows it beside the
// results, including any AGENTCHAT_ variable the environment set.
func (o *execOutput) start(app *host.App, cfg *host.Config, mode string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cfg.Connections != nil {
		o.conns = cfg.Connections.Connections
	}
	_, o.active = app.Providers()
	if !o.jsonl {
		return
	}
	var servers []string
	for _, sc := range cfg.Servers {
		servers = append(servers, sc.ID)
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "AGENTCHAT_") {
			env = append(env, name)
		}
	}
	sort.Strings(env)
	o.writeLocked("run-start", map[string]any{
		"connection": o.active,
		"model":      app.ModelLabel(),
		"servers":    servers,
		"maxSteps":   cfg.MaxSteps,
		"approve":    mode,
		"env":        env,
	})
}

// countLocked folds one runner event, from the main agent (scope "") or a
// sub-agent, into the tally, and writes a cost frame after each model call.
func (o *execOutput) countLocked(scope string, ev agent.Event) {
	switch ev.Kind {
	case agent.EventToolBegin:
		o.tally.ToolCalls++
		if ev.ToolCall != nil {
			if o.tally.ToolCallsByName == nil {
				o.tally.ToolCallsByName = map[string]int{}
			}
			o.tally.ToolCallsByName[ev.ToolCall.Name]++
		}
	case agent.EventToolDenied:
		o.tally.ToolCallsDenied++
	case agent.EventStep:
		o.tally.ModelCalls++
		if scope == "" && ev.Usage != nil {
			o.mainUsage = addUsage(o.mainUsage, *ev.Usage)
		}
		usd := o.priceLocked(ev.Model, ev.Usage)
		if o.jsonl {
			o.writeLocked("cost", execCost{Step: ev.Step, Scope: scope, Connection: o.connName(ev.Model), USD: usd})
		}
	}
}

// priceLocked adds one call's usage to the totals and returns its cost, or
// nil when the cost is unknown. A step that no stage named ran on the
// active connection.
func (o *execOutput) priceLocked(model string, u *agent.Usage) *float64 {
	name := o.connName(model)
	if u != nil {
		o.tally.Usage = addUsage(o.tally.Usage, *u)
		if name != "" {
			if o.tally.UsageByConnection == nil {
				o.tally.UsageByConnection = map[string]agent.Usage{}
			}
			o.tally.UsageByConnection[name] = addUsage(o.tally.UsageByConnection[name], *u)
		}
	}
	usd, ok := o.conns[name].Cost(u)
	if !ok {
		o.tally.UnpricedCalls++
		return nil
	}
	if o.tally.USD == nil {
		o.tally.USD = new(float64)
	}
	*o.tally.USD += usd
	return &usd
}

func (o *execOutput) connName(model string) string {
	if model != "" {
		return model
	}
	return o.active
}

// finishLocked adds the tokens TurnResult counted beyond the main agent's
// step events. That is the structured finalize call, which emits no step
// event and runs on the active connection.
func (o *execOutput) finishLocked(res *agent.TurnResult) {
	o.tally.Steps = res.Steps
	extra := agent.Usage{
		InputTokens:       res.Usage.InputTokens - o.mainUsage.InputTokens,
		OutputTokens:      res.Usage.OutputTokens - o.mainUsage.OutputTokens,
		CachedInputTokens: res.Usage.CachedInputTokens - o.mainUsage.CachedInputTokens,
	}
	o.mainUsage = res.Usage
	if extra.InputTokens > 0 || extra.OutputTokens > 0 {
		o.tally.ModelCalls++
		o.priceLocked("", &extra)
	}
}

// writeSummary writes the summary frame with the exit code err maps to.
func (o *execOutput) writeSummary(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.jsonl {
		return
	}
	o.tally.ExitCode = 0
	if err != nil {
		o.tally.ExitCode = exitTurnFailed
		var ee *exitError
		if errors.As(err, &ee) {
			o.tally.ExitCode = ee.code
		}
	}
	o.writeLocked("summary", o.tally)
}

func addUsage(a, b agent.Usage) agent.Usage {
	return agent.Usage{
		InputTokens:       a.InputTokens + b.InputTokens,
		OutputTokens:      a.OutputTokens + b.OutputTokens,
		CachedInputTokens: a.CachedInputTokens + b.CachedInputTokens,
	}
}

// execFrame is one JSON line. A decoded agentweb Watch frame has the same
// shape, so a tool that reads one can read the other.
type execFrame struct {
	Kind    string `json:"kind"`
	Payload any    `json:"payload"`
}

func (o *execOutput) On(ev host.HostEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if ev.Kind == host.HostTurnDone && ev.Result != nil {
		o.last = ev.Result
		o.finishLocked(ev.Result)
	}
	if o.jsonl {
		payload := any(ev)
		if _, err := json.Marshal(ev); err != nil {
			// Some events still carry live pointers that don't serialize (#994).
			payload = map[string]string{"Kind": string(ev.Kind), "_note": "non-serializable payload omitted"}
		}
		o.writeLocked(string(ev.Kind), payload)
	}
	switch ev.Kind {
	case host.HostRunnerEvent:
		o.countLocked("", ev.RunnerEvent)
	case host.HostSubAgentEvent:
		o.countLocked(ev.SubAgent.Scope, ev.SubAgent.Event)
	}
}

func (o *execOutput) writeLocked(kind string, payload any) {
	b, err := json.Marshal(execFrame{Kind: kind, Payload: payload})
	if err != nil {
		return
	}
	o.w.Write(append(b, '\n'))
}

// deny answers every ask. Nobody is there to approve a call, so a call the
// approval mode would ask about is refused, and the run says so instead of
// waiting forever.
func (o *execOutput) deny(_ context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.jsonl {
		o.writeLocked("ask-denied", map[string]string{"message": req.Message, "reason": "exec runs headless; the approval mode asks about this call"})
	}
	return core.ElicitationResult{Action: "decline"}, nil
}

func (o *execOutput) result() *agent.TurnResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.last
}
