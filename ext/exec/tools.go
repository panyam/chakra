package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// Source serves one tool per allowlisted command over agent.ToolSource.
//
// Each command gets its own tool rather than one run_command taking a name.
// The approval gate reads safety annotations, and those are per tool. With one
// tool, every command would share one destructiveHint. The operator could not
// then let `go build` run unattended while `terraform apply` asks. The
// containment is the same either way. The model picks from a fixed set and
// never composes a command line.
type Source struct {
	roots     []string
	cmds      map[string]*command
	defs      []core.ToolDef
	sandbox   Sandbox
	maxOutput int
	inline    int
	write     []string
	denyRead  []string
	shell     *shell
	procs     *procManager
	egress    *egress
}

// command is one resolved allowlist entry.
type command struct {
	spec    CommandSpec
	tool    string
	path    string
	dir     string
	timeout time.Duration
}

// NewSource resolves the allowlist and the sandbox, failing on anything it
// cannot settle now rather than on the first call.
//
// Several of these checks exist so that resolution happens at construction.
// Argv[0] goes through PATH once, here. So the tool runs the binary that was on
// PATH when the operator configured it, and a later PATH change can't swap in
// another binary.
func NewSource(cfg Config) (*Source, error) {
	if len(cfg.Roots) == 0 {
		return nil, errors.New("exec: Config.Roots needs at least one directory")
	}
	if len(cfg.Commands) == 0 && cfg.Shell == nil {
		return nil, errors.New("exec: Config.Commands is empty and Config.Shell is unset; there is nothing to run")
	}

	var roots []string
	for _, r := range cfg.Roots {
		if r == "" {
			return nil, errors.New("exec: Config.Roots contains an empty path")
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("exec: resolve root %s: %w", r, err)
		}
		// Resolve through symlinks. Containment checks compare against these
		// roots, and on darwin /tmp is a symlink to /private/tmp. Comparing an
		// unresolved root against a resolved path would reject a path inside
		// it.
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("exec: resolve root %s: %w", r, err)
		}
		roots = append(roots, real)
	}

	sandbox, err := resolveSandbox(cfg.Sandbox, defaultSandbox)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOutput := cfg.MaxOutput
	if maxOutput <= 0 {
		maxOutput = DefaultMaxOutput
	}
	inlineOutput := cfg.InlineOutput
	if inlineOutput == 0 {
		inlineOutput = DefaultInlineOutput
	}
	// Nil means the caller did not choose, empty means they chose nothing.
	// Collapsing the two would make "deny every extra write path" impossible.
	writePaths := cfg.WritePaths
	if writePaths == nil {
		writePaths = DefaultWritePaths
	}
	denyRead := cfg.DenyRead
	if denyRead == nil {
		denyRead = DefaultDenyRead
	}

	s := &Source{
		roots:     roots,
		cmds:      make(map[string]*command, len(cfg.Commands)),
		sandbox:   sandbox,
		maxOutput: maxOutput,
		inline:    inlineOutput,
		write:     append(append([]string{}, roots...), expandPaths(writePaths)...),
		denyRead:  expandPaths(denyRead),
	}

	for i := range cfg.Commands {
		c, err := s.resolveCommand(cfg.Commands[i], timeout)
		if err != nil {
			return nil, err
		}
		if _, dup := s.cmds[c.tool]; dup {
			return nil, fmt.Errorf("exec: two commands named %q", c.spec.Name)
		}
		s.cmds[c.tool] = c
		s.defs = append(s.defs, toolDef(c))
	}
	if cfg.Shell != nil {
		rules, err := NewRules(cfg.Shell.Rules)
		if err != nil {
			return nil, err
		}
		st := cfg.Shell.Timeout
		if st <= 0 {
			st = timeout
		}
		s.shell = &shell{rules: rules, timeout: st, allowNetwork: cfg.Shell.AllowNetwork}
		if cfg.Shell.Processes != nil {
			if s.procs, err = newProcManager(*cfg.Shell.Processes, roots[0]); err != nil {
				return nil, err
			}
		}
		s.defs = append(s.defs, s.shellDef())
		if s.procs != nil {
			s.defs = append(s.defs, s.procDefs()...)
		}
	}
	if cfg.Network != nil {
		if cfg.Shell != nil && cfg.Shell.AllowNetwork {
			return nil, errProxyWithNetwork
		}
		eg, err := newEgress(*cfg.Network)
		if err != nil {
			return nil, err
		}
		s.egress = eg
	}
	return s, nil
}

func (s *Source) resolveCommand(spec CommandSpec, defaultTimeout time.Duration) (*command, error) {
	if !nameRE.MatchString(spec.Name) {
		return nil, fmt.Errorf("exec: command name %q must match %s", spec.Name, nameRE)
	}
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("exec: command %q has no Argv", spec.Name)
	}
	if spec.Description == "" {
		return nil, fmt.Errorf("exec: command %q has no Description; it is the only thing the model has to choose by", spec.Name)
	}
	path, err := osexec.LookPath(spec.Argv[0])
	if err != nil {
		return nil, fmt.Errorf("exec: command %q: %w", spec.Name, err)
	}
	dir := spec.Dir
	if dir == "" {
		dir = s.roots[0]
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.roots[0], dir)
	}
	dir, err = s.confine(dir)
	if err != nil {
		return nil, fmt.Errorf("exec: command %q Dir: %w", spec.Name, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("exec: command %q Dir %s is not a directory", spec.Name, dir)
	}
	if spec.Args != nil {
		if err := spec.Args.validate(); err != nil {
			return nil, fmt.Errorf("exec: command %q: %w", spec.Name, err)
		}
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &command{spec: spec, tool: toolPrefix + spec.Name, path: path, dir: dir, timeout: timeout}, nil
}

// confine resolves a path and checks that it is inside a root.
//
// It resolves symlinks on the longest part of the path that exists, then
// re-attaches the rest. Requiring the whole path to exist would reject the
// build patterns these commands take, since `./ext/...` names no file.
// Skipping symlinks entirely would accept a link that sits inside a root and
// points out of it.
func (s *Source) confine(p string) (string, error) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.roots[0], p)
	}

	rest := ""
	probe := p
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			p = filepath.Join(real, rest)
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		rest = filepath.Join(filepath.Base(probe), rest)
		probe = parent
	}

	for _, root := range s.roots {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is outside every workspace root", p)
}

func toolDef(c *command) core.ToolDef {
	props := map[string]any{}
	if c.spec.Args != nil {
		props["args"] = map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"maxItems":    c.spec.Args.Max,
			"description": argsDescription(c.spec.Args),
		}
	}

	// readOnlyHint only when claimed, and destructiveHint only when the
	// command was declared reversible. An absent destructiveHint reads as
	// destructive, and an unclassified command should get that default.
	annotations := map[string]any{}
	if c.spec.ReadOnly {
		annotations["readOnlyHint"] = true
	}
	if c.spec.Reversible {
		annotations["destructiveHint"] = false
	}
	if len(annotations) == 0 {
		annotations = nil
	}

	return core.ToolDef{
		Name:        c.tool,
		Title:       "Run " + strings.ReplaceAll(c.spec.Name, "_", " "),
		Description: fmt.Sprintf("%s Runs `%s` in %s.", c.spec.Description, strings.Join(c.spec.Argv, " "), c.dir),
		InputSchema: map[string]any{
			"type":       "object",
			"properties": props,
		},
		Annotations: annotations,
	}
}

func argsDescription(p *ArgPolicy) string {
	b := fmt.Sprintf("Up to %d extra arguments, appended to the command. Each must match %s.", p.Max, "`"+p.Match+"`")
	if p.Paths {
		b += " Each must also name a path inside the workspace."
	}
	return b
}

// Tools returns the definitions, in configuration order.
func (s *Source) Tools(context.Context) ([]core.ToolDef, error) {
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call runs one allowlisted command.
//
// A name that is not in the allowlist is a dispatch failure and returns an
// error, per the ToolSource contract. Everything the command itself does,
// including exiting non-zero, comes back as a result the model reads.
func (s *Source) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if name == ShellTool && s.shell != nil {
		return s.callShell(ctx, args), nil
	}
	if s.procs != nil {
		switch name {
		case ExecCommandTool:
			return s.execCommand(args), nil
		case WriteStdinTool:
			return s.writeStdin(args), nil
		case ListProcessesTool:
			return s.listProcesses(), nil
		case KillProcessTool:
			return s.killProcess(args), nil
		}
	}
	c, ok := s.cmds[name]
	if !ok {
		return nil, fmt.Errorf("exec: unknown tool %q", name)
	}
	extra, err := s.checkArgs(c, args)
	if err != nil {
		return toolError(err.Error()), nil
	}
	return s.run(ctx, c, extra), nil
}

// checkArgs validates the model's trailing arguments against the spec.
func (s *Source) checkArgs(c *command, args map[string]any) ([]string, error) {
	raw, present := args["args"]
	if c.spec.Args == nil {
		if present {
			return nil, fmt.Errorf("%s takes no arguments", c.tool)
		}
		return nil, nil
	}
	if !present || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: args must be an array of strings", c.tool)
	}
	if len(list) > c.spec.Args.Max {
		return nil, fmt.Errorf("%s takes at most %d arguments, got %d", c.tool, c.spec.Args.Max, len(list))
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		sv, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: args must be an array of strings", c.tool)
		}
		if !c.spec.Args.re.MatchString(sv) {
			return nil, fmt.Errorf("%s: argument %q is not permitted; it must match %s", c.tool, sv, c.spec.Args.Match)
		}
		if c.spec.Args.Paths {
			if _, err := s.confine(filepath.Join(c.dir, sv)); err != nil {
				return nil, fmt.Errorf("%s: argument %q: %w", c.tool, sv, err)
			}
		}
		out = append(out, sv)
	}
	return out, nil
}

// run spawns an allowlisted command under the sandbox.
func (s *Source) run(ctx context.Context, c *command, extra []string) *core.ToolResult {
	argv := append(append([]string{}, c.spec.Argv...), extra...)
	return s.spawn(ctx, spawnSpec{
		path:         c.path,
		argv:         argv,
		dir:          c.dir,
		timeout:      c.timeout,
		allowNetwork: c.spec.AllowNetwork,
		tool:         c.tool,
	}).result
}

// spawnSpec is one process to start.
type spawnSpec struct {
	path         string
	argv         []string
	dir          string
	timeout      time.Duration
	allowNetwork bool
	// unconfined skips the sandbox, for an escalated shell call that the user
	// approved.
	unconfined bool
	tool       string
}

// spawned is a finished process. It holds the result for the model, and the
// parts a caller inspects before adding to that result.
type spawned struct {
	result         *core.ToolResult
	exit           int
	timedOut       bool
	stdout, stderr streamView
}

// spawn starts the process, under the sandbox unless sp.unconfined, and
// collects its output.
func (s *Source) spawn(ctx context.Context, sp spawnSpec) spawned {
	ctx, cancel := context.WithTimeout(ctx, sp.timeout)
	defer cancel()

	cmd := osexec.Command(sp.path, sp.argv[1:]...)
	// Args[0] is what the process sees as its own name. Some tools switch
	// behaviour on argv[0], so we keep the configured spelling rather than
	// the resolved path.
	cmd.Args = sp.argv
	cmd.Dir = sp.dir
	if s.egress != nil {
		cmd.Env = append(os.Environ(), s.egress.env()...)
	}
	setProcessGroup(cmd)

	stdout := newCapBuffer(s.maxOutput)
	stderr := newCapBuffer(s.maxOutput)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if !sp.unconfined {
		policy := Policy{Write: s.write, DenyRead: s.denyRead, Dir: sp.dir, AllowNetwork: sp.allowNetwork}
		if err := s.sandbox.Confine(cmd, policy); err != nil {
			return spawned{result: toolError(fmt.Sprintf("%s: sandbox: %v", sp.tool, err)), exit: -1}
		}
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return spawned{result: toolError(fmt.Sprintf("%s: %v", sp.tool, err)), exit: -1}
	}

	// Kill on timeout ourselves rather than through CommandContext, which
	// signals only the process it started. See killProcessGroup.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		killProcessGroup(cmd)
		waitErr = <-done
	}
	elapsed := time.Since(started)

	exit := cmd.ProcessState.ExitCode()
	inline := s.inline
	if timedOut && inline < 0 {
		inline = DefaultInlineOutput
	}
	out, errOut := view(stdout, inline), view(stderr, inline)
	return spawned{
		result:   renderRun(sp.timeout, sp.argv, exit, elapsed, timedOut, out, errOut, waitErr),
		exit:     exit,
		timedOut: timedOut,
		stdout:   out,
		stderr:   errOut,
	}
}

// renderRun turns a finished command into the result the model reads.
//
// A non-zero exit is not a tool error. We run a build or a test to learn
// whether it failed. Flagging a failure as an error would tell the model that
// the tool malfunctioned, when the code did. A timeout is the other way round.
// The command did not finish, so nothing it printed is a verdict.
func renderRun(timeout time.Duration, argv []string, exit int, elapsed time.Duration, timedOut bool, stdout, stderr streamView, waitErr error) *core.ToolResult {
	var b strings.Builder
	line := strings.Join(argv, " ")
	switch {
	case timedOut:
		fmt.Fprintf(&b, "%s timed out after %s and was killed. Output so far:\n", line, timeout)
	default:
		fmt.Fprintf(&b, "%s exited %d after %s.\n", line, exit, elapsed.Round(time.Millisecond))
	}
	if out := stdout.text; out != "" {
		fmt.Fprintf(&b, "\nstdout:\n%s", out)
	}
	if errOut := stderr.text; errOut != "" {
		fmt.Fprintf(&b, "\nstderr:\n%s", errOut)
	}
	if !timedOut && exit != 0 && stdout.text == "" && stderr.text == "" && waitErr != nil {
		fmt.Fprintf(&b, "\n%v", waitErr)
	}

	return &core.ToolResult{
		IsError: timedOut,
		Content: []core.Content{{Type: "text", Text: b.String()}},
		StructuredContent: map[string]any{
			"command":     line,
			"exit_code":   exit,
			"timed_out":   timedOut,
			"duration_ms": elapsed.Milliseconds(),
			"stdout":      stdout.text,
			"stderr":      stderr.text,
			"truncated":   stdout.dropped || stderr.dropped,
		},
	}
}

// streamView is one stream as it appears in the result.
type streamView struct {
	text    string
	dropped bool
}

func view(b *capBuffer, max int) streamView {
	text, dropped := b.View(max)
	return streamView{text, dropped}
}

// toolError reports a refusal the model can act on, rather than a dispatch
// failure that would abort the turn.
func toolError(msg string) *core.ToolResult {
	return &core.ToolResult{IsError: true, Content: []core.Content{{Type: "text", Text: msg}}}
}

var _ agent.ToolSource = (*Source)(nil)
