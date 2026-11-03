package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	osexec "os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// The long-running process tools (#155), named as Codex names them so a model
// trained on Codex calls them without being taught: exec_command starts a
// command and returns what it printed within a short wait, or a session id if
// it is still running; write_stdin types into it, or with nothing to type
// collects new output. list_processes and kill_process manage what is running.
const (
	ExecCommandTool   = "exec_command"
	WriteStdinTool    = "write_stdin"
	ListProcessesTool = "list_processes"
	KillProcessTool   = "kill_process"
)

// ProcessConfig turns on the long-running process tools, for commands that
// outlive one tool call: dev servers, watchers, interactive prompts. They run
// under the same sandbox as shell and are judged by the same prefix rules, so
// it needs Config.Shell. Zero fields take the defaults.
type ProcessConfig struct {
	// Max bounds how many processes run at once. Default 8.
	Max int
	// MaxOutput is the bytes of output kept per process; older output is
	// dropped first, and a read says how much it missed. Default 1 MiB.
	MaxOutput int
	// DefaultYield is how long a call waits for output when the model doesn't
	// say. Default 10s.
	DefaultYield time.Duration
	// MaxYield caps the wait a model can ask for. Default 30s.
	MaxYield time.Duration
}

const (
	defaultMaxProcesses = 8
	defaultProcOutput   = 1 << 20
	defaultYield        = 10 * time.Second
	defaultMaxYield     = 30 * time.Second
	// procReadMax bounds what one call returns to the model; the middle of a
	// longer stretch is dropped, as for run_* and shell.
	procReadMax = 16 << 10
)

// procManager owns a session's processes. They outlive the turn that started
// them, so nothing here hangs off a turn's context; Close kills them all.
type procManager struct {
	cfg   ProcessConfig
	mu    sync.Mutex
	procs map[string]*proc
	next  int
}

func newProcManager(cfg ProcessConfig) *procManager {
	if cfg.Max <= 0 {
		cfg.Max = defaultMaxProcesses
	}
	if cfg.MaxOutput <= 0 {
		cfg.MaxOutput = defaultProcOutput
	}
	if cfg.DefaultYield <= 0 {
		cfg.DefaultYield = defaultYield
	}
	if cfg.MaxYield <= 0 {
		cfg.MaxYield = defaultMaxYield
	}
	return &procManager{cfg: cfg, procs: map[string]*proc{}}
}

// proc is one running (or just finished) process.
type proc struct {
	id      string
	command string
	cmd     *osexec.Cmd
	stdin   io.Writer
	closeIn func()
	tty     bool
	out     *tailBuffer
	started time.Time

	done chan struct{}
	exit int

	// readMu serializes reads, so two calls on one process in the same step
	// (tool calls run in parallel) don't both claim the same output.
	readMu sync.Mutex
	// read is how much of the output the model has been shown.
	read int64
}

// tailBuffer keeps the last max bytes written and counts everything, so a
// reader can ask for what arrived since its last read and learn what it
// missed.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	max   int
	total int64
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
	}
	b.total += int64(len(p))
	return len(p), nil
}

// since returns the output after position from, the new position, and how
// many bytes after from were dropped before they could be read.
func (b *tailBuffer) since(from int64) (string, int64, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := b.total - int64(len(b.buf))
	dropped := int64(0)
	if from < start {
		dropped, from = start-from, start
	}
	return string(b.buf[from-start:]), b.total, dropped
}

func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (s *Source) procDefs() []core.ToolDef {
	confinement := fmt.Sprintf("It runs inside the %s sandbox, under the same rules as shell.", s.sandbox.Name())
	if !s.sandboxed() {
		confinement = "It runs without a sandbox, under the same rules as shell."
	}
	return []core.ToolDef{
		{
			Name:  ExecCommandTool,
			Title: "Start a command",
			Description: "Run a shell command (through bash -lc) that may keep running, such as a dev server, a watcher or a program waiting " +
				"for input. Returns what it printed within yield_ms; if it is still running, the result names a session_id to use " +
				"with write_stdin. Set tty for programs that need a terminal. " + confinement,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"cmd"},
				"properties": map[string]any{
					"cmd":      map[string]any{"type": "string", "description": "The command line."},
					"workdir":  map[string]any{"type": "string", "description": "Directory to run in, relative to the workspace root. Defaults to the root."},
					"yield_ms": map[string]any{"type": "integer", "description": "How long to wait for output before returning. Defaults to 10000."},
					"tty":      map[string]any{"type": "boolean", "description": "Run in a pseudo-terminal."},
				},
			},
		},
		{
			Name:  WriteStdinTool,
			Title: "Write to a running command",
			Description: "Send text to a command started with exec_command and return the output since the last call. Send empty chars " +
				"to just collect new output. Include \"\\n\" to press enter, or \"\\u0003\" for ctrl-c on a tty.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string"},
					"chars":      map[string]any{"type": "string", "description": "Text to write; empty to poll."},
					"yield_ms":   map[string]any{"type": "integer", "description": "How long to wait for output. Defaults to 10000."},
				},
			},
		},
		{
			Name:        ListProcessesTool,
			Title:       "List running commands",
			Description: "List the commands started with exec_command, running or finished but not yet read.",
			Annotations: map[string]any{"readOnlyHint": true},
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        KillProcessTool,
			Title:       "Stop a running command",
			Description: "Stop a command started with exec_command, with everything it started, and return its last output.",
			InputSchema: map[string]any{
				"type":       "object",
				"required":   []string{"session_id"},
				"properties": map[string]any{"session_id": map[string]any{"type": "string"}},
			},
		},
	}
}

type execCommandArgs struct {
	Cmd     string `json:"cmd"`
	Workdir string `json:"workdir"`
	YieldMS int    `json:"yield_ms"`
	TTY     bool   `json:"tty"`
}

type writeStdinArgs struct {
	SessionID string `json:"session_id"`
	Chars     string `json:"chars"`
	YieldMS   int    `json:"yield_ms"`
}

func decodeArgs(args map[string]any, into any) error {
	raw, _ := json.Marshal(args)
	return json.Unmarshal(raw, into)
}

// shellArgv is how a cmd string runs: bash -lc, or sh -c where there is no
// bash. The prefix rules read the same argv, so a script is judged command by
// command.
func shellArgv(cmd string) []string {
	if _, err := osexec.LookPath("bash"); err == nil {
		return []string{"bash", "-lc", cmd}
	}
	return []string{"sh", "-c", cmd}
}

func (m *procManager) yield(ms int) time.Duration {
	if ms <= 0 {
		return m.cfg.DefaultYield
	}
	d := time.Duration(ms) * time.Millisecond
	if d > m.cfg.MaxYield {
		return m.cfg.MaxYield
	}
	return d
}

func (s *Source) execCommand(args map[string]any) *core.ToolResult {
	var a execCommandArgs
	if err := decodeArgs(args, &a); err != nil || strings.TrimSpace(a.Cmd) == "" {
		return toolError("exec_command needs cmd, the command line to run")
	}
	m := s.procs
	m.mu.Lock()
	running := 0
	for _, p := range m.procs {
		if p.running() {
			running++
		}
	}
	m.mu.Unlock()
	if running >= m.cfg.Max {
		return toolError(fmt.Sprintf("exec_command: %d commands are already running, the most allowed; stop one with kill_process", running))
	}
	dir := s.roots[0]
	if a.Workdir != "" {
		var err error
		if dir, err = s.confine(a.Workdir); err != nil {
			return toolError("exec_command: workdir: " + err.Error())
		}
	}
	argv := shellArgv(a.Cmd)
	path, err := osexec.LookPath(argv[0])
	if err != nil {
		return toolError("exec_command: " + err.Error())
	}
	cmd := osexec.Command(path, argv[1:]...)
	cmd.Args, cmd.Dir = argv, dir
	p := &proc{command: a.Cmd, cmd: cmd, tty: a.TTY, out: &tailBuffer{max: m.cfg.MaxOutput}, done: make(chan struct{}), started: time.Now()}

	policy := Policy{Write: s.write, DenyRead: s.denyRead, Dir: dir, AllowNetwork: s.shell.allowNetwork}
	if a.TTY {
		if err := startTTY(p, s.sandbox, policy); err != nil {
			return toolError("exec_command: " + err.Error())
		}
	} else {
		setProcessGroup(cmd)
		if err := s.sandbox.Confine(cmd, policy); err != nil {
			return toolError("exec_command: sandbox: " + err.Error())
		}
		in, err := cmd.StdinPipe()
		if err != nil {
			return toolError("exec_command: " + err.Error())
		}
		cmd.Stdout, cmd.Stderr = p.out, p.out
		if err := cmd.Start(); err != nil {
			return toolError("exec_command: " + err.Error())
		}
		p.stdin, p.closeIn = in, func() { in.Close() }
	}
	go func() {
		err := cmd.Wait()
		p.exit = cmd.ProcessState.ExitCode()
		if err != nil && p.exit == 0 {
			p.exit = -1
		}
		if p.closeIn != nil {
			p.closeIn()
		}
		close(p.done)
	}()

	m.mu.Lock()
	m.next++
	p.id = fmt.Sprintf("p%d", m.next)
	m.procs[p.id] = p
	m.mu.Unlock()
	return m.collect(p, m.yield(a.YieldMS), "$ "+a.Cmd)
}

// collect waits up to d for the process to finish, then returns the output
// since the model's last read. A finished process is forgotten once its last
// output has been read.
func (m *procManager) collect(p *proc, d time.Duration, header string) *core.ToolResult {
	if d > 0 {
		t := time.NewTimer(d)
		select {
		case <-p.done:
		case <-t.C:
		}
		t.Stop()
	}
	if !p.running() {
		// Let the output copier drain what the process wrote before it exited.
		time.Sleep(20 * time.Millisecond)
	}
	p.readMu.Lock()
	text, pos, dropped := p.out.since(p.read)
	p.read = pos
	p.readMu.Unlock()
	var b strings.Builder
	if header != "" {
		b.WriteString(header + "\n")
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "... %d bytes of earlier output were dropped ...\n", dropped)
	}
	if len(text) > procReadMax {
		text = text[:procReadMax/2] + fmt.Sprintf("\n... %d bytes dropped from the middle ...\n", len(text)-procReadMax) + text[len(text)-procReadMax/2:]
	}
	b.WriteString(text)
	sc := map[string]any{"session_id": p.id, "dropped": dropped}
	elapsed := time.Since(p.started).Round(time.Millisecond)
	if p.running() {
		fmt.Fprintf(&b, "\n[still running as %s after %s; call write_stdin to send input or collect output, kill_process to stop it]", p.id, elapsed)
		sc["running"] = true
	} else {
		fmt.Fprintf(&b, "\n[exited %d after %s]", p.exit, elapsed)
		sc["running"], sc["exit_code"] = false, p.exit
		m.mu.Lock()
		delete(m.procs, p.id)
		m.mu.Unlock()
	}
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: b.String()}}, StructuredContent: sc}
}

func (m *procManager) get(id string) (*proc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[id]
	if !ok {
		return nil, fmt.Errorf("no process %q; list_processes shows the ones there are", id)
	}
	return p, nil
}

func (s *Source) writeStdin(args map[string]any) *core.ToolResult {
	var a writeStdinArgs
	if err := decodeArgs(args, &a); err != nil || a.SessionID == "" {
		return toolError("write_stdin needs session_id")
	}
	p, err := s.procs.get(a.SessionID)
	if err != nil {
		return toolError("write_stdin: " + err.Error())
	}
	if a.Chars != "" {
		if !p.running() {
			return s.procs.collect(p, 0, "write_stdin: the process has already exited, so nothing was written")
		}
		if _, err := io.WriteString(p.stdin, a.Chars); err != nil {
			return toolError("write_stdin: " + err.Error())
		}
	}
	return s.procs.collect(p, s.procs.yield(a.YieldMS), "")
}

func (s *Source) listProcesses() *core.ToolResult {
	m := s.procs
	m.mu.Lock()
	ps := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		ps = append(ps, p)
	}
	m.mu.Unlock()
	sort.Slice(ps, func(i, j int) bool { return ps[i].started.Before(ps[j].started) })
	if len(ps) == 0 {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "no processes"}}}
	}
	var b strings.Builder
	var list []map[string]any
	for _, p := range ps {
		state := "running"
		if !p.running() {
			state = fmt.Sprintf("exited %d, output not yet read", p.exit)
		}
		fmt.Fprintf(&b, "%s  %s  (%s, started %s ago)\n", p.id, p.command, state, time.Since(p.started).Round(time.Second))
		list = append(list, map[string]any{"session_id": p.id, "cmd": p.command, "running": p.running()})
	}
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: strings.TrimRight(b.String(), "\n")}}, StructuredContent: map[string]any{"processes": list}}
}

func (s *Source) killProcess(args map[string]any) *core.ToolResult {
	var a writeStdinArgs
	if err := decodeArgs(args, &a); err != nil || a.SessionID == "" {
		return toolError("kill_process needs session_id")
	}
	p, err := s.procs.get(a.SessionID)
	if err != nil {
		return toolError("kill_process: " + err.Error())
	}
	killProcessGroup(p.cmd)
	return s.procs.collect(p, 5*time.Second, "")
}

// killAll stops every process, for the end of a session.
func (m *procManager) killAll() {
	m.mu.Lock()
	ps := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		ps = append(ps, p)
	}
	m.procs = map[string]*proc{}
	m.mu.Unlock()
	for _, p := range ps {
		killProcessGroup(p.cmd)
	}
	for _, p := range ps {
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
		}
	}
}

// classifyProcs judges the process tools' calls. exec_command is judged by the
// shell's prefix rules, like shell. write_stdin with nothing to send only
// reads; with text it types into a running program, which can do anything
// that program does, so it asks unless the mode runs everything. An approval
// is remembered per process.
func (s *Source) classifyProcs(_ context.Context, info agent.ToolCallInfo) (agent.CallClass, bool) {
	if s.procs == nil {
		return agent.CallClass{}, false
	}
	switch info.Call.Name {
	case ExecCommandTool:
		var a execCommandArgs
		if err := json.Unmarshal(info.Call.Args.Raw(), &a); err != nil || a.Cmd == "" {
			return agent.CallClass{}, true
		}
		return s.shell.classify(shellArgv(a.Cmd)), true
	case WriteStdinTool:
		var a writeStdinArgs
		if err := json.Unmarshal(info.Call.Args.Raw(), &a); err != nil {
			return agent.CallClass{}, true
		}
		if a.Chars == "" {
			return agent.CallClass{Disposition: agent.DispositionReadOnly, Key: "poll"}, true
		}
		return agent.CallClass{Disposition: agent.DispositionDestructive, Key: a.SessionID, Reason: "types into a running command"}, true
	case KillProcessTool:
		return agent.CallClass{Disposition: agent.DispositionReversible, Key: "kill"}, true
	}
	return agent.CallClass{}, false
}

// renderProcApproval words the ask for the process tools.
func (s *Source) renderProcApproval(info agent.ToolCallInfo) (string, bool) {
	if s.procs == nil {
		return "", false
	}
	switch info.Call.Name {
	case ExecCommandTool:
		var a execCommandArgs
		if json.Unmarshal(info.Call.Args.Raw(), &a) != nil || a.Cmd == "" {
			return "", false
		}
		where := fmt.Sprintf("in the %s sandbox", s.sandbox.Name())
		if !s.sandboxed() {
			where = "without a sandbox"
		}
		return fmt.Sprintf("Start, %s, and leave running:\n  %s", where, a.Cmd), true
	case WriteStdinTool:
		var a writeStdinArgs
		if json.Unmarshal(info.Call.Args.Raw(), &a) != nil {
			return "", false
		}
		command := a.SessionID
		if p, err := s.procs.get(a.SessionID); err == nil {
			command = fmt.Sprintf("%s (%s)", a.SessionID, p.command)
		}
		return fmt.Sprintf("Type into %s:\n  %q", command, a.Chars), true
	}
	return "", false
}

// Close stops every process the session started.
func (s *Source) Close() error {
	if s.procs != nil {
		s.procs.killAll()
	}
	return nil
}

// errNoTTY is returned where pseudo-terminals aren't supported.
var errNoTTY = errors.New("tty is not supported on this platform")
