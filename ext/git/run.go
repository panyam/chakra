package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// emptyTree is git's id for the tree with nothing in it. In a repository with
// no commits yet, diffs are taken against it.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// runner runs git in the workspace. A repository's own config or a user's
// environment could add things to a read, and every call turns them off. They
// are a pager, colour, an external diff or textconv driver, an fsmonitor hook,
// and pathspec magic. So a path argument means that path and nothing else.
type runner struct {
	bin  string // git, resolved on PATH at construction
	root string // the workspace root, where git runs
	max  int    // output cap in bytes
}

func (r runner) run(ctx context.Context, env []string, args ...string) (string, error) {
	full := append([]string{"-C", r.root, "-c", "core.fsmonitor=false", "-c", "color.ui=never", "-c", "core.quotepath=off"}, args...)
	cmd := osexec.CommandContext(ctx, r.bin, full...)
	cmd.Env = append(os.Environ(), "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "GIT_LITERAL_PATHSPECS=1", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}

// capped trims s to the runner's cap, saying how much was left out, so a huge
// diff reaches the model as a readable head rather than not at all.
func (r runner) capped(s string) string {
	if len(s) <= r.max {
		return s
	}
	return s[:r.max] + fmt.Sprintf("\n[output truncated: %d of %d bytes shown; narrow it with path or stat]\n", r.max, len(s))
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9._/@{}~^-]+$`)

// checkRef refuses a ref that git could read as an option. It also refuses a
// ref holding a character that no branch, tag, commit id or rev expression
// (HEAD~2, main@{1}) can contain.
func checkRef(what, ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") || len(ref) > 200 || !refPattern.MatchString(ref) {
		return fmt.Errorf("%s %q is not a ref git accepts here", what, ref)
	}
	return nil
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// relPath turns a tool's path argument into one relative to the root,
// refusing any that leaves it. An empty path comes back empty, meaning the
// whole tree.
func relPath(root, p string) (string, error) {
	if p == "" || p == "." {
		return "", nil
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, p)
	}
	rel, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", p)
	}
	return filepath.ToSlash(rel), nil
}

// workTree resolves root to the top of the git work tree holding it, failing
// when there is none.
func workTree(ctx context.Context, bin, root string) (string, error) {
	r := runner{bin: bin, root: root}
	out, err := r.run(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("git: " + root + " is not inside a git work tree")
	}
	return strings.TrimSpace(out), nil
}
