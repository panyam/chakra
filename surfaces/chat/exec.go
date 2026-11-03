package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
			"Exit codes: 0 done, 1 the turn failed, 2 it hit the step limit, 3 --output-schema was set and no answer matched it, 64 bad usage.",
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
		return err
	}
	defer setup.close()
	app := setup.app

	if session := v.GetString("session"); session != "" {
		if setup.store == nil {
			return usage("--session needs --session-store")
		}
		if err := app.AttachRun(ctx, session); err != nil {
			return err
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

	switch {
	case errors.Is(turnErr, agent.ErrMaxSteps):
		return &exitError{code: exitMaxSteps, err: turnErr}
	case turnErr != nil:
		return &exitError{code: exitTurnFailed, err: turnErr}
	case schema.Len() > 0 && (result == nil || result.Structured.Len() == 0):
		return &exitError{code: exitSchemaInvalid, err: errors.New("no structured answer")}
	case schema.Len() > 0 && result.StructuredError != "":
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

// execOutput is exec's surface: it writes events as JSON lines when asked,
// remembers the turn's result, and denies every approval ask.
type execOutput struct {
	mu    sync.Mutex
	w     io.Writer
	jsonl bool
	last  *agent.TurnResult
}

// execFrame is one JSON line, the same shape agentweb's Watch frames take
// once decoded, so tools that read one read the other.
type execFrame struct {
	Kind    string `json:"kind"`
	Payload any    `json:"payload"`
}

func (o *execOutput) On(ev host.HostEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if ev.Kind == host.HostTurnDone && ev.Result != nil {
		o.last = ev.Result
	}
	if !o.jsonl {
		return
	}
	payload := any(ev)
	if _, err := json.Marshal(ev); err != nil {
		// Some events still carry live pointers that don't serialize (#994).
		payload = map[string]string{"Kind": string(ev.Kind), "_note": "non-serializable payload omitted"}
	}
	o.writeLocked(string(ev.Kind), payload)
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
