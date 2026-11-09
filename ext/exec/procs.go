package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// The long-running process tools (#155) use Codex's names, so a model trained
// on Codex calls them without being taught. exec_command starts a command. It
// returns what the command printed within a short wait, or a session id if the
// command is still running. write_stdin types into a running command, or
// collects new output when there is nothing to type. list_processes and
// kill_process manage the running processes.
const (
	ExecCommandTool   = "exec_command"
	WriteStdinTool    = "write_stdin"
	ListProcessesTool = "list_processes"
	KillProcessTool   = "kill_process"
)

// ProcessConfig turns on the long-running process tools, for commands that
// outlive one tool call, such as dev servers, watchers and interactive
// prompts. They run under the same sandbox as shell and are judged by the
// same prefix rules, so ProcessConfig needs Config.Shell. Zero fields take the
// defaults.
type ProcessConfig struct {
	// Max bounds how many processes run at once. Default 8.
	Max int
	// MaxOutput caps the bytes of output kept per process. Older output is
	// dropped first, and a read says how much it missed. Default 1 MiB.
	MaxOutput int
	// DefaultYield is how long a call waits for output when the model doesn't
	// say. Default 10s.
	DefaultYield time.Duration
	// MaxYield caps the wait a model can ask for. Default 30s.
	MaxYield time.Duration
	// StreamBudget caps how many bytes of one process's output agentweb and
	// agentchat show the user as it arrives. Past the budget, streaming
	// pauses for that process, and the model still reads all of the output.
	// Default 4 MiB, and negative streams without limit. The host keeps
	// streamed output in its event log, and host.Config.MaxEventLogRetention
	// bounds that log by count.
	StreamBudget int
	// AllowDetach offers exec_command's detach argument (#228). A detached
	// command keeps running after the session closes, for tasks that leave a
	// server up for whatever checks the work afterwards. It starts in its own
	// session, reads stdin from /dev/null and writes its output to a log file
	// in the system temp dir, outside the workspace. Close leaves it running,
	// and the log file stays behind. It still runs under the sandbox. The
	// network proxy closes with the session, so a detached command that needs
	// the network through it loses that when the session ends. Every detached
	// call asks for approval unless the shell rules forbid the command.
	//
	// A later session on the same first root finds the processes an earlier
	// one left running (#239). list_processes shows them, write_stdin with
	// empty chars reads their output, and kill_process stops them.
	AllowDetach bool
	// StateDir holds the registry of detached processes, their log files and
	// their exit codes. Empty uses $XDG_STATE_HOME, or ~/.local/state. Each
	// workspace gets its own directory under chakra/detached.
	StateDir string
}

const (
	defaultMaxProcesses = 8
	defaultProcOutput   = 1 << 20
	defaultYield        = 10 * time.Second
	defaultMaxYield     = 30 * time.Second
	// procReadMax bounds what one call returns to the model. The middle of a
	// longer stretch is dropped, as for run_* and shell.
	procReadMax = 16 << 10
)

// procManager owns a session's processes. They outlive the turn that started
// them, so nothing here derives from a turn's context. Close kills them all,
// except the detached ones.
type procManager struct {
	cfg   ProcessConfig
	mu    sync.Mutex
	procs map[string]*proc
	next  int
	// report receives output as it arrives, when the host shows it.
	report func(host.ProcessOutput)

	// reg is the workspace's registry of detached processes, and nil
	// without AllowDetach. owner is this session's token in its entries.
	reg   *registry
	owner string
	// stop ends the watchers of adopted processes when the session closes.
	stop     chan struct{}
	stopOnce sync.Once
}

// newProcManager builds the manager for a session whose first root is root.
// With AllowDetach it opens the workspace's registry and adopts the detached
// processes earlier sessions left.
func newProcManager(cfg ProcessConfig, root string) (*procManager, error) {
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
	if cfg.StreamBudget == 0 {
		cfg.StreamBudget = defaultStreamBudget
	}
	m := &procManager{cfg: cfg, procs: map[string]*proc{}, stop: make(chan struct{})}
	if !cfg.AllowDetach {
		return m, nil
	}
	state := cfg.StateDir
	if state == "" {
		state = stateDir()
	}
	reg, err := newRegistry(state, root)
	if err != nil {
		return nil, fmt.Errorf("exec: detached process registry: %w", err)
	}
	m.reg = reg
	m.owner = reg.newID()
	openOwners.Store(m.owner, true)
	if err := m.adopt(); err != nil {
		openOwners.Delete(m.owner)
		return nil, fmt.Errorf("exec: detached process registry: %w", err)
	}
	return m, nil
}

// adopt takes over the registry's entries whose session has closed. A
// process still running becomes this session's, marked as from an earlier
// session. One that exited and left an exit code is kept until its output is
// read. Any other entry is dropped: its process is gone, or its pid now
// belongs to a process that started at another time.
func (m *procManager) adopt() error {
	unlock, err := lockDir(m.reg.dir)
	if err != nil {
		return err
	}
	defer unlock()
	ownerStart, _ := procStart(os.Getpid())
	for _, e := range m.reg.entries() {
		if ownerOpen(e) {
			continue
		}
		running := alive(e.PID, e.StartToken)
		code, exited := m.reg.exitCode(e.ID)
		if !running && !exited {
			m.reg.remove(e.ID)
			continue
		}
		e.Owner, e.OwnerPID, e.OwnerStart = m.owner, os.Getpid(), ownerStart
		if err := m.reg.write(e); err != nil {
			continue
		}
		p := &proc{command: e.Cmd, pid: e.PID, entry: e.ID, adopted: true, log: m.reg.path(e.ID, ".log"),
			out: &tailBuffer{max: m.cfg.MaxOutput}, done: make(chan struct{}), started: e.Started}
		if running {
			go m.watch(p, e.StartToken)
		} else {
			p.exit = code
			close(p.done)
		}
		m.next++
		p.id = fmt.Sprintf("p%d", m.next)
		m.procs[p.id] = p
	}
	return nil
}

// watch closes p.done once an adopted process exits. The exit code is the one
// the launch wrapper recorded, or -1 when it recorded none, as when the
// process was killed.
func (m *procManager) watch(p *proc, tok string) {
	t := time.NewTicker(watchPoll)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		if alive(p.pid, tok) {
			continue
		}
		p.exit = -1
		if code, ok := m.reg.exitCode(p.entry); ok {
			p.exit = code
		}
		close(p.done)
		return
	}
}

// proc is one running (or just finished) process.
type proc struct {
	id      string
	command string
	cmd     *osexec.Cmd
	stdin   io.Writer
	closeIn func()
	tty     bool
	// log is where a detached process writes its output. It is empty for
	// a process attached to the session.
	log string
	// pid is the process's id. cmd is nil for an adopted process.
	pid int
	// entry is a detached process's registry id, and empty otherwise.
	entry string
	// adopted marks a detached process an earlier session started.
	adopted bool
	out     *tailBuffer
	started time.Time

	done chan struct{}
	exit int
	// resize sets a tty's size. It is nil for a process with no terminal.
	resize func(cols, rows int) error
	// sizeMu guards cols and rows, the tty's current size, which the output
	// stream reports on each event.
	sizeMu     sync.Mutex
	cols, rows int

	// drained, when set, closes once the output copier has read everything
	// the process wrote. Only a tty process has one: for a pipe, cmd.Wait
	// already waits for the copy.
	drained chan struct{}

	// readMu serializes reads. Tool calls in one step run in parallel, and two
	// calls on one process must not both claim the same output.
	readMu sync.Mutex
	// read is how much of the output the model has been shown.
	read int64
}

// drainWait bounds how long wait holds done for a tty's copier. A background
// child that still holds the terminal would otherwise keep it open for good.
const drainWait = 2 * time.Second

// wait reaps the process, records its exit code and closes done. By then
// every byte the process wrote is in out, so a reader needs no grace period.
// On Linux a terminal keeps what the process wrote after it exits, and
// closing it before the copier reads that loses the end of the output.
func (p *proc) wait() {
	err := p.cmd.Wait()
	p.exit = p.cmd.ProcessState.ExitCode()
	if err != nil && p.exit == 0 {
		p.exit = -1
	}
	if p.drained != nil {
		t := time.NewTimer(drainWait)
		select {
		case <-p.drained:
		case <-t.C:
		}
		t.Stop()
	}
	if p.closeIn != nil {
		p.closeIn()
	}
	close(p.done)
}

// tailBuffer keeps the last max bytes written and counts everything, so a
// reader can ask for what arrived since its last read and learn what it
// missed.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	max   int
	total int64
	// tee, when set, also gets each write, for streaming to the surfaces.
	tee func([]byte)
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
	}
	b.total += int64(len(p))
	if b.tee != nil {
		b.tee(p)
	}
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

// size returns the tty's current size.
func (p *proc) size() (cols, rows int) {
	p.sizeMu.Lock()
	defer p.sizeMu.Unlock()
	return p.cols, p.rows
}

// setSize resizes the tty and records the size the stream reports.
func (p *proc) setSize(cols, rows int) error {
	if p.resize == nil {
		return fmt.Errorf("%s isn't running on a terminal, so it has no size", p.id)
	}
	if !p.running() {
		return fmt.Errorf("%s has exited", p.id)
	}
	if err := p.resize(cols, rows); err != nil {
		return err
	}
	p.sizeMu.Lock()
	p.cols, p.rows = cols, rows
	p.sizeMu.Unlock()
	return nil
}

// write sends data to the process's input.
func (p *proc) write(data string) error {
	if !p.running() {
		return fmt.Errorf("%s has exited", p.id)
	}
	if p.log != "" {
		return fmt.Errorf("%s is detached and reads no input", p.id)
	}
	_, err := io.WriteString(p.stdin, data)
	return err
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
	props := map[string]any{
		"cmd":      map[string]any{"type": "string", "description": "The command line."},
		"workdir":  map[string]any{"type": "string", "description": "Directory to run in, relative to the workspace root. Defaults to the root."},
		"yield_ms": map[string]any{"type": "integer", "description": "How long to wait for output before returning. Defaults to 10000."},
		"tty":      map[string]any{"type": "boolean", "description": "Run in a pseudo-terminal."},
	}
	lifetime := " The command is stopped when this session ends."
	if s.procs.cfg.AllowDetach {
		props["detach"] = map[string]any{"type": "boolean", "description": "Keep the command running after this session ends, " +
			"for a server the task says to leave running. Its output goes to a log file named in the result, and it takes no input."}
		lifetime = " The command is stopped when this session ends, unless detach is set."
	}
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
				"with write_stdin. Set tty for programs that need a terminal." + lifetime + " " + confinement,
			InputSchema: map[string]any{
				"type":       "object",
				"required":   []string{"cmd"},
				"properties": props,
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
			Description: "List the commands started with exec_command, running or finished but not yet read, and any left running by an earlier session.",
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
	Detach  bool   `json:"detach"`
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

// shellArgv builds the argv that runs a cmd string. It uses bash -lc, or sh -c
// where there is no bash. The prefix rules read the same argv, so a script is
// judged command by command.
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
	if a.Detach && !m.cfg.AllowDetach {
		return toolError("exec_command: detach is not allowed in this workspace")
	}
	if a.Detach && a.TTY {
		return toolError("exec_command: a detached command can't have a tty")
	}
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
	if s.egress != nil {
		cmd.Env = append(os.Environ(), s.egress.env()...)
	}
	p := &proc{command: a.Cmd, cmd: cmd, tty: a.TTY, out: &tailBuffer{max: m.cfg.MaxOutput}, done: make(chan struct{}), started: time.Now()}
	m.mu.Lock()
	report := m.report
	m.mu.Unlock()
	var stream *outputStream
	if report != nil {
		// The stream is teed before the start, so the first output isn't
		// missed. That output waits in the stream until the process has its id.
		stream = newOutputStream(m.cfg.StreamBudget)
		p.out.tee = stream.add
	}

	policy := Policy{Write: s.write, DenyRead: s.denyRead, Dir: dir, AllowNetwork: s.shell.allowNetwork}
	if a.Detach {
		if err := m.startDetached(p, s.sandbox, policy); err != nil {
			return toolError("exec_command: " + err.Error())
		}
	} else if a.TTY {
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
	p.pid = cmd.Process.Pid
	go p.wait()

	m.mu.Lock()
	m.next++
	p.id = fmt.Sprintf("p%d", m.next)
	m.procs[p.id] = p
	m.mu.Unlock()
	if stream != nil {
		go stream.run(p, report)
	}
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
	p.readMu.Lock()
	var text string
	var pos, dropped int64
	if p.log != "" {
		text, pos, dropped = readLogSince(p.log, p.read, m.cfg.MaxOutput)
	} else {
		text, pos, dropped = p.out.since(p.read)
	}
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
	if p.log != "" {
		sc["detached"], sc["pid"], sc["log"] = true, p.pid, p.log
		if p.adopted {
			sc["earlier_session"] = true
		}
	}
	if p.running() && p.log != "" {
		fmt.Fprintf(&b, "\n[still running as %s (pid %d) after %s, detached: it keeps running after this session ends, "+
			"with its output in %s; kill_process stops it]", p.id, p.pid, elapsed, p.log)
		sc["running"] = true
	} else if p.running() {
		fmt.Fprintf(&b, "\n[still running as %s after %s; call write_stdin to send input or collect output, kill_process to stop it]", p.id, elapsed)
		sc["running"] = true
	} else {
		fmt.Fprintf(&b, "\n[exited %d after %s]", p.exit, elapsed)
		sc["running"], sc["exit_code"] = false, p.exit
		m.mu.Lock()
		delete(m.procs, p.id)
		m.mu.Unlock()
		if p.entry != "" {
			m.reg.remove(p.entry)
		}
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
		if p.log != "" {
			return toolError("write_stdin: " + p.id + " is detached and reads no input; send empty chars to collect its output")
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
		if p.adopted {
			state += "; detached, from an earlier session"
		}
		fmt.Fprintf(&b, "%s  %s  (%s, started %s ago)\n", p.id, p.command, state, time.Since(p.started).Round(time.Second))
		item := map[string]any{"session_id": p.id, "cmd": p.command, "running": p.running()}
		if p.adopted {
			item["earlier_session"], item["pid"], item["log"] = true, p.pid, p.log
		}
		list = append(list, item)
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
	if p.adopted {
		killGroup(p.pid)
	} else {
		killProcessGroup(p.cmd)
	}
	return s.procs.collect(p, 5*time.Second, "")
}

// killAll stops every process, for the end of a session, except the
// detached ones, which stay in the registry for a later session.
func (m *procManager) killAll() {
	m.stopOnce.Do(func() {
		close(m.stop)
		openOwners.Delete(m.owner)
	})
	m.mu.Lock()
	ps := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		if p.log == "" {
			ps = append(ps, p)
		}
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
// reads. With text it types into a running program, and that input can make
// the program do anything. So it asks unless the mode runs everything. An
// approval is remembered per process.
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
		class := s.shell.classify(shellArgv(a.Cmd))
		if a.Detach && class.Disposition != agent.DispositionForbidden {
			class = agent.CallClass{Disposition: agent.DispositionDestructive, Key: "detach " + a.Cmd, Reason: "keeps running after this session ends"}
		}
		return class, true
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
		if a.Detach {
			return fmt.Sprintf("Start, %s, and leave running after this session ends:\n  %s", where, a.Cmd), true
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

// Close stops every process the session started, and the network proxy.
func (s *Source) Close() error {
	if s.procs != nil {
		s.procs.killAll()
	}
	if s.egress != nil {
		return s.egress.close()
	}
	return nil
}

// ttyCols and ttyRows are the size of the terminal an exec_command with tty
// gets. Without one the size is 0x0, and some programs then wrap at every
// character or refuse to draw. Surfaces get the size on ProcessOutput.
const (
	ttyCols = 120
	ttyRows = 30
)

// errNoTTY is returned where pseudo-terminals aren't supported.
var errNoTTY = errors.New("tty is not supported on this platform")

// startDetached starts p in its own session with its output in a log file,
// and records it in the registry. The process holds the file itself, so its
// writes don't go through a pipe that closes when agentchat exits. A wrapper
// shell writes the command's exit code to a file the parent opened, so a later
// session can report it.
func (m *procManager) startDetached(p *proc, sb Sandbox, policy Policy) error {
	id := m.reg.newID()
	logPath, exitPath := m.reg.path(id, ".log"), m.reg.path(id, ".exit")
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	x, err := os.OpenFile(exitPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		m.reg.remove(id)
		return err
	}
	defer x.Close()
	sh, err := osexec.LookPath("sh")
	if err != nil {
		m.reg.remove(id)
		return err
	}
	inner := append([]string{p.cmd.Path}, p.cmd.Args[1:]...)
	p.cmd.Path, p.cmd.Args = sh, append([]string{"sh", "-c", detachedWrapper, "sh"}, inner...)
	setDetached(p.cmd)
	if err := sb.Confine(p.cmd, policy); err != nil {
		m.reg.remove(id)
		return fmt.Errorf("sandbox: %w", err)
	}
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = nil, f, f
	p.cmd.ExtraFiles = []*os.File{x}
	if err := p.cmd.Start(); err != nil {
		m.reg.remove(id)
		return err
	}
	p.log, p.entry = logPath, id
	tok, _ := procStart(p.cmd.Process.Pid)
	ownerStart, _ := procStart(os.Getpid())
	err = m.reg.write(regEntry{ID: id, PID: p.cmd.Process.Pid, Cmd: p.command, Workdir: p.cmd.Dir, Started: p.started,
		StartToken: tok, Owner: m.owner, OwnerPID: os.Getpid(), OwnerStart: ownerStart})
	if err != nil {
		// A detached process no later session can find is the problem this
		// registry exists to prevent, so don't leave one running.
		killProcessGroup(p.cmd)
		_ = p.cmd.Wait()
		m.reg.remove(id)
		return fmt.Errorf("record the detached process: %w", err)
	}
	return nil
}

// readLogSince returns what a detached process wrote to its log after from,
// keeping the last max bytes when there is more.
func readLogSince(path string, from int64, max int) (string, int64, int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", from, 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= from {
		return "", from, 0
	}
	size, start, dropped := info.Size(), from, int64(0)
	if size-start > int64(max) {
		dropped = size - int64(max) - start
		start = size - int64(max)
	}
	b := make([]byte, size-start)
	n, _ := f.ReadAt(b, start)
	return string(b[:n]), start + int64(n), dropped
}
