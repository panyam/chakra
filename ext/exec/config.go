package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultTimeout bounds a command that does not set its own.
const DefaultTimeout = 2 * time.Minute

// DefaultMaxOutput caps the bytes held in memory for a command's stdout, and
// separately for its stderr. It guards against a runaway process.
// DefaultInlineOutput shapes what the model reads, so this cap is set well
// above a realistic failing go test transcript.
const DefaultMaxOutput = 1 << 20

// DefaultInlineOutput caps how many bytes of each stream go into a result when
// the host is not offloading. Output beyond it is dropped from the middle and
// the head and tail are kept, because a failing build usually states the
// problem at one end or the other.
const DefaultInlineOutput = 32 << 10

// toolPrefix namespaces the generated tool names so a command called "test"
// registers as run_test and cannot collide with an unrelated source's "test".
const toolPrefix = "run_"

// nameRE constrains a command name, because the name becomes a tool name and
// a tool name is matched literally by approval rules and per-tool config.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Config configures the command source.
//
// There is no mode in which the model supplies a command. Commands come from
// here, and the model chooses among them. files.Config.Roots and
// lsp.ServerSpec.Command follow the same rule for the same reason. A model's
// instructions can come from content it read rather than from the user. So if
// the model can name something, an injected instruction can name it too.
type Config struct {
	// Roots are the directories a command may run in and write to. At least
	// one is required. The first is primary, and a relative Dir or path
	// argument resolves against it.
	//
	// Roots is a set rather than one directory, as files.Config.Roots is,
	// because few sessions keep to one repository (panyam/mcpkit#1314).
	Roots []string

	// Commands is the allowlist. Each entry becomes one tool, so the model
	// picks a tool rather than composing a command line. Empty is an error,
	// because an exec extension with nothing to run is a misconfiguration. It
	// isn't a degenerate case worth supporting.
	Commands []CommandSpec

	// Shell turns on the general command tool, "shell", governed by prefix
	// rules instead of an allowlist. Nil leaves it off. See ShellConfig.
	Shell *ShellConfig

	// Network lets commands reach the listed hosts through a local proxy,
	// and nothing else beyond loopback. Nil keeps the all-or-nothing
	// AllowNetwork behaviour. See NetworkConfig.
	Network *NetworkConfig

	// Sandbox confines every command. Nil selects the platform's backend and
	// fails construction when there is none, rather than running unconfined.
	//
	// Unconfined() is the deliberate opt-out, and has to be written here. Use
	// it when an operator has already isolated the process, as in the
	// ordinary deployed case. There the operator's container is the boundary,
	// and a second fence inside it protects nothing.
	Sandbox Sandbox

	// Timeout bounds any command whose spec does not set its own. Zero means
	// DefaultTimeout.
	Timeout time.Duration

	// MaxOutput caps the bytes held in memory for each of stdout and stderr,
	// dropping from the middle beyond it. Zero means DefaultMaxOutput.
	MaxOutput int

	// InlineOutput caps how many bytes of each stream go into the result, in
	// both its text and its structured content. Zero means
	// DefaultInlineOutput. Negative returns all of MaxOutput, for a host whose
	// OffloadingSource stores large results. A failing assertion usually sits
	// in the middle of a long run. With a negative value that middle goes to
	// the store, and read_tool_result can grep it there. A timed-out run
	// always gets the DefaultInlineOutput view, since offloading leaves error
	// results inline.
	InlineOutput int

	// WritePaths lists directories outside Roots where commands may still
	// write. Nil means DefaultWritePaths, which covers the build and module
	// caches that toolchains need. An explicitly empty non-nil slice means
	// none.
	//
	// Suppose a toolchain needs a path not listed here. It fails with a
	// sandbox denial rather than writing silently outside confinement. We
	// prefer that failure to the other one.
	WritePaths []string

	// DenyRead blocks commands from reading these paths. Reads are otherwise
	// unrestricted. Nil means DefaultDenyRead, and an explicitly empty non-nil
	// slice means no read is denied.
	//
	// Reads are broadly allowed on purpose. A real toolchain reads compilers,
	// SDKs, module caches and system libraries from all over the filesystem.
	// Confining reads would break every one of them, and the failures would
	// look like bugs rather than policy. That leaves one exposure, a command
	// reading credentials. So the default denies the well-known credential
	// locations, and doesn't pretend the profile confines more than it does.
	DenyRead []string
}

// DefaultWritePaths lists the caches a build or test command writes to outside
// the workspace. Without them a sandboxed `go test` fails on its build cache
// before it compiles anything.
var DefaultWritePaths = []string{
	"$TMPDIR",
	"/tmp",
	"/private/tmp",
	"/private/var/tmp",
	"$HOME/Library/Caches",
	"$HOME/.cache",
	"$HOME/go/pkg/mod",
	"$HOME/.cargo/registry",
	"$HOME/.npm",
}

// DefaultDenyRead lists credential locations a build or test command has no
// business reading. It is not exhaustive and cannot be. Even so, a profile
// with it stops the obvious exfiltration, and a profile without it stops
// none.
var DefaultDenyRead = []string{
	"$HOME/.ssh",
	"$HOME/.aws",
	"$HOME/.gnupg",
	"$HOME/.kube",
	"$HOME/.docker",
	"$HOME/.config/gcloud",
	"$HOME/.netrc",
	"$HOME/.npmrc",
	"$HOME/.pypirc",
}

// CommandSpec is one entry in the allowlist.
type CommandSpec struct {
	// Name identifies the command to the model. It becomes the tool name
	// with a run_ prefix, so it must match [a-z][a-z0-9_]*.
	Name string

	// Argv is the command and its fixed arguments. Argv[0] is resolved
	// through PATH once, at construction, so the tool runs the binary that
	// was on PATH when the operator configured it.
	//
	// It is an argv and never a shell string. Nothing here is passed to a
	// shell, so an argument containing ; or | or $( is an argument.
	Argv []string

	// Dir is the working directory, relative to the first root or absolute
	// inside one of them. Empty means the first root.
	Dir string

	// Description tells the model when to reach for this command. It becomes
	// the tool description. The model has only that description to choose
	// between two entries.
	Description string

	// Args lets the model supply trailing arguments. Nil means the command
	// takes none and the tool exposes no argument at all. Nil is the default,
	// because accepting free-form arguments makes an allowlist weaker than it
	// appears to be.
	Args *ArgPolicy

	// AllowNetwork lets this command reach the network. It is off by default,
	// because a build that can fetch at test time can also post.
	AllowNetwork bool

	// ReadOnly marks a command with no side effects worth reviewing, and
	// becomes the tool's readOnlyHint. It makes the command auto-allowed
	// under ModeReadOnlyAuto and above.
	ReadOnly bool

	// Reversible marks a command whose effects can be undone, and clears the
	// tool's destructiveHint. It makes the command auto-allowed under
	// ModeReversibleAuto.
	//
	// The zero value declares the command destructive, so an unclassified
	// command needs approval. Only the operator who wrote the allowlist knows
	// which entries are safe to run unattended. A default that guessed for
	// them would guess in the permissive direction.
	Reversible bool

	// Timeout overrides Config.Timeout for this command.
	Timeout time.Duration
}

// ArgPolicy constrains the trailing arguments a model may supply.
//
// It lets the test loop run a subset rather than only whole suites. Without
// it, the loop reruns everything to learn one thing.
type ArgPolicy struct {
	// Max is the most arguments accepted, and must be positive.
	Max int

	// Match is a regular expression, and every argument must match it end to
	// end. It is required. A policy that permits arguments without
	// constraining them gives back the free-form command line, and this
	// package exists to avoid that.
	Match string

	// Paths additionally requires every argument to name a path inside a
	// root, resolved through symlinks. It applies to every argument, so a
	// command taking a mix of flags and paths cannot use it.
	Paths bool

	re *regexp.Regexp
}

func (p *ArgPolicy) validate() error {
	if p.Max <= 0 {
		return fmt.Errorf("ArgPolicy.Max must be positive")
	}
	if p.Match == "" {
		return fmt.Errorf("ArgPolicy.Match is required; a policy that constrains nothing permits everything")
	}
	re, err := regexp.Compile(`\A(?:` + p.Match + `)\z`)
	if err != nil {
		return fmt.Errorf("ArgPolicy.Match: %w", err)
	}
	p.re = re
	return nil
}

// expandPaths resolves $HOME and $TMPDIR in a path list and drops entries that
// expand to nothing, so a default list still works on a machine where one of
// the variables is unset.
func expandPaths(in []string) []string {
	var out []string
	for _, p := range in {
		missing := false
		e := os.Expand(p, func(k string) string {
			var v string
			switch k {
			case "HOME":
				v = os.Getenv("HOME")
			case "TMPDIR":
				v = strings.TrimSuffix(os.Getenv("TMPDIR"), "/")
			}
			// An unset variable drops the whole entry. Expanding it to nothing
			// would turn $HOME/.ssh into /.ssh, which is a different path that
			// happens to exist, so the rule would silently apply somewhere the
			// operator never named.
			if v == "" {
				missing = true
			}
			return v
		})
		if missing || e == "" || !strings.HasPrefix(e, "/") {
			continue
		}
		out = append(out, resolveBoth(filepath.Clean(e))...)
	}
	return out
}

// resolveBoth returns a path and, when it differs, the same path with symlinks
// resolved.
//
// It returns both because this code can't know which spelling a rule will be
// matched against. Seatbelt matches the resolved path. On darwin /tmp is a
// link to /private/tmp, so a rule naming /tmp silently covers nothing. The
// profile doesn't show that failure, because it lists the rule the operator
// asked for. For a WritePaths entry the failure is closed. Writes outside the
// listed paths are refused anyway, so the command just can't write there. For
// a DenyRead entry the failure is open. The read denial appears to be in
// force and is not, which is worse than having no rule, because the operator
// believes the path is protected.
func resolveBoth(p string) []string {
	real, err := filepath.EvalSymlinks(p)
	if err != nil || real == p {
		return []string{p}
	}
	return []string{p, real}
}
