package exec

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A detached process outlives the session that started it (#228), so a later
// session on the same workspace needs a way to find it (#239). Each detached
// start writes an entry to a registry directory for the workspace, and a new
// session reads that directory when it starts. The process's log and exit
// files sit beside its entry.

// regEntry is one detached process on disk.
type regEntry struct {
	// ID names the entry's files: <ID>.json, <ID>.log and <ID>.exit.
	ID      string    `json:"id"`
	PID     int       `json:"pid"`
	Cmd     string    `json:"cmd"`
	Workdir string    `json:"workdir"`
	Started time.Time `json:"started"`
	// StartToken is the process's start time as the OS reports it. A pid
	// the OS has reused for another process has a different one.
	StartToken string `json:"startToken"`
	// Owner and OwnerPID name the session holding the entry. Another session
	// leaves an entry alone while its owner is open.
	Owner      string `json:"owner"`
	OwnerPID   int    `json:"ownerPid"`
	OwnerStart string `json:"ownerStart"`
}

// registry is a workspace's directory of detached processes.
type registry struct {
	dir string
}

// stateDir is where chakra keeps state that outlives a session:
// $XDG_STATE_HOME, or ~/.local/state, on every platform. The temp dir is the
// last resort when there is no home.
func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state")
	}
	return os.TempDir()
}

// newRegistry returns the registry for the workspace whose first root is
// root, under state, creating the directory.
func newRegistry(state, root string) (*registry, error) {
	sum := sha256.Sum256([]byte(root))
	dir := filepath.Join(state, "chakra", "detached", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &registry{dir: dir}, nil
}

func (r *registry) path(id, ext string) string { return filepath.Join(r.dir, id+ext) }

// newID returns a fresh entry id.
func (r *registry) newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "proc-" + hex.EncodeToString(b)
}

// write saves e. It writes a temp file and renames it, so a reader never sees
// half an entry.
func (r *registry) write(e regEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp := r.path(e.ID, ".json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path(e.ID, ".json"))
}

// remove deletes an entry with its log and exit files.
func (r *registry) remove(id string) {
	for _, ext := range []string{".json", ".log", ".exit"} {
		os.Remove(r.path(id, ext))
	}
}

// entries reads every entry. An unreadable one is skipped.
func (r *registry) entries() []regEntry {
	names, _ := filepath.Glob(filepath.Join(r.dir, "*.json"))
	var out []regEntry
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			continue
		}
		var e regEntry
		if json.Unmarshal(b, &e) != nil || e.ID == "" || e.PID <= 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// exitCode reads the code the launch wrapper wrote when the command finished.
func (r *registry) exitCode(id string) (int, bool) {
	b, err := os.ReadFile(r.path(id, ".exit"))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return n, err == nil
}

// alive reports whether pid is the process that had start token tok.
func alive(pid int, tok string) bool {
	got, ok := procStart(pid)
	return ok && got == tok
}

// openOwners holds the tokens of this process's sessions that are still
// open. One process can host several sessions, as agentweb does, so a pid
// alone doesn't say whether an entry's owner is gone.
var openOwners sync.Map

// ownerOpen reports whether e's session is still open.
func ownerOpen(e regEntry) bool {
	if e.OwnerPID == os.Getpid() {
		_, ok := openOwners.Load(e.Owner)
		return ok
	}
	return alive(e.OwnerPID, e.OwnerStart)
}

// detachedWrapper runs a detached command and records its exit code on fd 3,
// which the parent opened on the entry's exit file. The sandbox doesn't have
// to allow writing outside the workspace, and the command itself doesn't see
// fd 3. The wrapper exits with the command's code.
const detachedWrapper = `"$@" 3>&-; c=$?; echo $c >&3; exit $c`

// watchPoll is how often an adopted process is checked for having exited.
// The session didn't start it, so it can't wait on it.
const watchPoll = 200 * time.Millisecond
