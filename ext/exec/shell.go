package exec

import (
	"context"
	"encoding/json"
	"fmt"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// ShellTool is the general command tool's name.
const ShellTool = "shell"

// ShellConfig turns on the general command tool: the model passes an argv,
// it runs inside the sandbox, and Rules decide per command prefix whether the
// approval gate lets it run, asks, or refuses. It is the alternative to
// listing every command up front in Config.Commands, and the two can be used
// together.
type ShellConfig struct {
	// Rules decide per prefix. A command no rule matches is asked about,
	// keyed by its first two words when the user approves it for the session.
	Rules []PrefixRule

	// Timeout bounds one run. Zero uses Config.Timeout; the model can ask for
	// less, never more.
	Timeout time.Duration

	// AllowNetwork lets sandboxed runs use the network. Off by default, so a
	// command that needs it fails, is recognized as a likely sandbox denial,
	// and can be retried with escalation.
	AllowNetwork bool
}

// shell is the resolved ShellConfig.
type shell struct {
	rules        *Rules
	timeout      time.Duration
	allowNetwork bool
}

// shellArgs is the tool's input.
type shellArgs struct {
	Command       []string `json:"command"`
	Workdir       string   `json:"workdir"`
	TimeoutMS     int      `json:"timeout_ms"`
	Escalate      bool     `json:"escalate"`
	Justification string   `json:"justification"`
}

// sandboxed reports whether commands run confined. Without a sandbox
// (exec.Unconfined(), as inside a container that is itself the sandbox)
// there is nothing to escalate out of, so the shell tool neither offers
// escalation nor treats a call that asks for it differently (#203).
func (s *Source) sandboxed() bool {
	_, none := s.sandbox.(unconfined)
	return !none
}

// parseShellArgs reads a shell call's arguments. Without a sandbox, escalate
// and its justification are dropped: the call runs like any other.
func parseShellArgs(raw []byte, sandboxed bool) (shellArgs, error) {
	var a shellArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fmt.Errorf("shell: %v", err)
	}
	if !sandboxed {
		a.Escalate, a.Justification = false, ""
	}
	if len(a.Command) == 0 || a.Command[0] == "" {
		return a, fmt.Errorf("shell: command is empty; pass an argv such as [\"go\", \"test\", \"./...\"]")
	}
	if a.Escalate && strings.TrimSpace(a.Justification) == "" {
		return a, fmt.Errorf("shell: escalate needs a justification saying why the command must run outside the sandbox")
	}
	return a, nil
}

func (s *Source) shellDef() core.ToolDef {
	if !s.sandboxed() {
		return core.ToolDef{
			Name:  ShellTool,
			Title: "Run a command",
			Description: "Run a command in the workspace. Commands run without a sandbox, with the access the process has. " +
				"Pass command as an argv, e.g. [\"go\", \"test\", \"./...\"]; use [\"bash\", \"-lc\", \"...\"] only when you need && or a pipe. " +
				"Some commands run straight away, some ask the user first, and some are refused.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"command"},
				"properties": map[string]any{
					"command":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "description": "The argv to run."},
					"workdir":    map[string]any{"type": "string", "description": "Directory to run in, relative to the workspace root. Defaults to the root."},
					"timeout_ms": map[string]any{"type": "integer", "description": "Stop the command after this long. Capped by the configured timeout."},
				},
			},
		}
	}
	return core.ToolDef{
		Name:  ShellTool,
		Title: "Run a command",
		Description: fmt.Sprintf("Run a command in the workspace, inside the %s sandbox: it can write only inside the workspace "+
			"and a few caches, and has no network unless configured. Pass command as an argv, e.g. [\"go\", \"test\", \"./...\"]; "+
			"use [\"bash\", \"-lc\", \"...\"] only when you need && or a pipe. Some commands run straight away, some ask the user "+
			"first, and some are refused. If a command fails because the sandbox blocked it, you can call again with escalate "+
			"set and a justification; that runs it outside the sandbox if the user agrees.", s.sandbox.Name()),
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]any{
				"command":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "description": "The argv to run."},
				"workdir":       map[string]any{"type": "string", "description": "Directory to run in, relative to the workspace root. Defaults to the root."},
				"timeout_ms":    map[string]any{"type": "integer", "description": "Stop the command after this long. Capped by the configured timeout."},
				"escalate":      map[string]any{"type": "boolean", "description": "Run outside the sandbox. Always asks the user. Use only after a sandbox denial, or for a command that needs the network or paths outside the workspace."},
				"justification": map[string]any{"type": "string", "description": "Required with escalate: one sentence the user reads before deciding."},
			},
		},
		// Absent destructiveHint reads as destructive: a command no rule
		// covers is asked about.
	}
}

// callShell runs one shell call. The gate has already decided whether it may.
func (s *Source) callShell(ctx context.Context, args map[string]any) *core.ToolResult {
	raw, _ := json.Marshal(args)
	a, err := parseShellArgs(raw, s.sandboxed())
	if err != nil {
		return toolError(err.Error())
	}
	dir := s.roots[0]
	if a.Workdir != "" {
		if dir, err = s.confine(a.Workdir); err != nil {
			return toolError("shell: workdir: " + err.Error())
		}
	}
	path, err := osexec.LookPath(a.Command[0])
	if err != nil {
		return toolError(fmt.Sprintf("shell: %v", err))
	}
	timeout := s.shell.timeout
	if a.TimeoutMS > 0 && time.Duration(a.TimeoutMS)*time.Millisecond < timeout {
		timeout = time.Duration(a.TimeoutMS) * time.Millisecond
	}
	out := s.spawn(ctx, spawnSpec{
		path:         path,
		argv:         a.Command,
		dir:          dir,
		timeout:      timeout,
		allowNetwork: s.shell.allowNetwork,
		unconfined:   a.Escalate,
		tool:         ShellTool,
	})
	if s.sandboxed() && !a.Escalate && out.exit != 0 && !out.timedOut && looksLikeSandboxDenial(out.stderr.text+"\n"+out.stdout.text, s.shell.allowNetwork) {
		out.result.Content = append(out.result.Content, core.Content{Type: "text", Text: fmt.Sprintf(
			"\nThis looks like the %s sandbox blocking the command (a write outside the workspace, or the network). "+
				"If it has to run anyway, call shell again with the same command, escalate: true, and a justification the user can judge.",
			s.sandbox.Name())})
		if sc, ok := out.result.StructuredContent.(map[string]any); ok {
			sc["sandbox_denied"] = true
		}
	}
	return out.result
}

// denialSigns are output fragments that, after a sandboxed command failed,
// suggest the sandbox stopped it rather than the command failing on its own.
// A false positive costs a suggestion to escalate, which still asks the user.
var denialSigns = []string{
	"operation not permitted",
	"read-only file system",
	"sandbox-exec",
	"deny(1)",
}

// networkSigns are the same for a denied network, checked only when the
// sandbox denies it.
var networkSigns = []string{
	"could not resolve host",
	"temporary failure in name resolution",
	"no such host",
	"network is unreachable",
	"dial tcp",
	"connection refused",
}

func looksLikeSandboxDenial(output string, networkAllowed bool) bool {
	out := strings.ToLower(output)
	for _, s := range denialSigns {
		if strings.Contains(out, s) {
			return true
		}
	}
	if !networkAllowed {
		for _, s := range networkSigns {
			if strings.Contains(out, s) {
				return true
			}
		}
	}
	return false
}

// classifyShell is the ApprovalClassifier for the shell tool. Forbidden
// beats escalation, so a refused prefix can't be run by asking to leave the
// sandbox.
func (s *Source) classifyShell(_ context.Context, info agent.ToolCallInfo) (agent.CallClass, bool) {
	if s.shell == nil || info.Call.Name != ShellTool {
		return agent.CallClass{}, false
	}
	a, err := parseShellArgs(info.Call.Args.Raw(), s.sandboxed())
	if err != nil {
		// The tool rejects it without running anything; no opinion.
		return agent.CallClass{}, true
	}
	class := s.shell.classify(a.Command)
	if class.Disposition == agent.DispositionForbidden || !a.Escalate {
		return class, true
	}
	return agent.CallClass{Disposition: agent.DispositionAlwaysAsk, Reason: "outside the sandbox: " + a.Justification}, true
}

// classify judges one argv by the rules. Every command in a script must
// pass: any forbidden part forbids the whole, any part the rules don't cover
// leaves it to the tool's destructive default, any prompt asks, and only a
// script whose every part is allowed is allowed.
func (sh *shell) classify(argv []string) agent.CallClass {
	cmds, ok := commandsOf(argv)
	if !ok {
		return agent.CallClass{Reason: "a script the rules can't read"}
	}
	var keys []string
	unmatched, prompt := false, false
	var reasons []string
	for _, cmd := range cmds {
		r, found := sh.rules.match(cmd)
		if !found {
			unmatched = true
			keys = append(keys, firstWords(cmd, 2))
			continue
		}
		switch r.Decision {
		case Forbidden:
			reason := r.Justification
			if reason == "" {
				reason = r.String() + " is forbidden"
			}
			return agent.CallClass{Disposition: agent.DispositionForbidden, Reason: reason}
		case Prompt:
			prompt = true
			if r.Justification != "" {
				reasons = append(reasons, r.Justification)
			}
		}
		keys = append(keys, firstWords(cmd, len(r.Prefix)))
	}
	key := ""
	if len(cmds) == 1 {
		key = keys[0]
	}
	reason := strings.Join(reasons, "; ")
	switch {
	case unmatched:
		return agent.CallClass{Key: key, Reason: reason}
	case prompt:
		return agent.CallClass{Disposition: agent.DispositionDestructive, Key: key, Reason: reason}
	default:
		return agent.CallClass{Disposition: agent.DispositionReadOnly, Key: key}
	}
}

func firstWords(argv []string, n int) string {
	if n > len(argv) {
		n = len(argv)
	}
	words := append([]string(nil), argv[:n]...)
	if len(words) > 0 {
		words[0] = filepath.Base(words[0])
	}
	return strings.Join(words, " ")
}

// renderShellApproval words the ask for a shell call: the command, where it
// runs, and whether the sandbox confines it. An escalation says plainly that
// it doesn't, with the model's justification.
func (s *Source) renderShellApproval(info agent.ToolCallInfo) (string, bool) {
	if s.shell == nil || info.Call.Name != ShellTool {
		return "", false
	}
	a, err := parseShellArgs(info.Call.Args.Raw(), s.sandboxed())
	if err != nil {
		return "", false
	}
	dir := s.roots[0]
	if a.Workdir != "" {
		dir = filepath.Join(s.roots[0], a.Workdir)
	}
	line := strings.Join(a.Command, " ")
	if a.Escalate {
		return fmt.Sprintf("Run OUTSIDE the sandbox, with full access to this machine:\n  %s\nin %s\nThe agent says: %s", line, dir, a.Justification), true
	}
	q := fmt.Sprintf("Run in the %s sandbox:\n  %s\nin %s", s.sandbox.Name(), line, dir)
	if info.Class != nil && info.Class.Reason != "" {
		q += "\n(" + info.Class.Reason + ")"
	}
	return q, true
}
