package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSourceNeedsAWorkTree(t *testing.T) {
	if _, err := NewSource(Config{Root: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "not inside a git work tree") {
		t.Errorf("a plain directory: %v", err)
	}
	if _, err := NewSource(Config{}); err == nil {
		t.Errorf("no root: no error")
	}
}

// Reads are always offered and read-only; commit and branch only with
// Mutations, the worktree tools only with a WorktreeDir, and all four are
// destructive, so the gate asks before they run.
func TestToolsOfferedAndTheirHints(t *testing.T) {
	dir := repo(t)
	got, _ := source(t, Config{Root: dir}).Tools(context.Background())
	if names(got) != "git_status git_diff git_log" {
		t.Errorf("default tools: %s", names(got))
	}
	all, _ := source(t, Config{Root: dir, Mutations: true, WorktreeDir: t.TempDir()}).Tools(context.Background())
	if names(all) != "git_status git_diff git_log git_commit git_branch git_worktree_add git_worktree_remove" {
		t.Errorf("all tools: %s", names(all))
	}
	for _, d := range all {
		read := d.Annotations["readOnlyHint"] == true
		destroy := d.Annotations["destructiveHint"] == true
		if wantRead := strings.Contains("git_status git_diff git_log", d.Name); read != wantRead || destroy == wantRead {
			t.Errorf("%s: readOnly %v, destructive %v", d.Name, read, destroy)
		}
	}
	if _, err := source(t, Config{Root: dir}).Call(context.Background(), "git_commit", map[string]any{"message": "x"}); err == nil {
		t.Errorf("control: git_commit ran without Mutations")
	}
}

func TestStatusShowsChangesAndSaysWhenClean(t *testing.T) {
	dir := repo(t)
	s := source(t, Config{Root: dir})
	if out, _ := call(t, s, "git_status", nil); !strings.Contains(out, "## main") || !strings.Contains(out, "(clean") {
		t.Errorf("clean: %q", out)
	}
	write(t, dir, "a.txt", "one\nTWO\n")
	write(t, dir, "new.txt", "hi\n")
	out, isErr := call(t, s, "git_status", nil)
	if isErr || !strings.Contains(out, " M a.txt") || !strings.Contains(out, "?? new.txt") || strings.Contains(out, "(clean") {
		t.Errorf("changed: %q", out)
	}
}

// With no target, git_diff is everything uncommitted against HEAD, staged
// and unstaged together, with untracked files listed after; path narrows it
// and stat summarises.
func TestDiffOfUncommittedChanges(t *testing.T) {
	dir := repo(t)
	s := source(t, Config{Root: dir})
	if out, _ := call(t, s, "git_diff", nil); out != "(no changes)\n" {
		t.Errorf("clean: %q", out)
	}
	write(t, dir, "a.txt", "one\nTWO\n")
	write(t, dir, "lib/b.go", "package lib\n\nvar X = 1\n")
	sh(t, dir, "add", "lib/b.go")
	write(t, dir, "new.txt", "hi\n")
	out, _ := call(t, s, "git_diff", nil)
	for _, want := range []string{"-two\n+TWO", "+var X = 1", "untracked (not in the diff above):\n  new.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if out, _ := call(t, s, "git_diff", map[string]any{"path": "lib"}); strings.Contains(out, "a.txt") || !strings.Contains(out, "var X") {
		t.Errorf("path lib:\n%s", out)
	}
	if out, _ := call(t, s, "git_diff", map[string]any{"stat": true}); !strings.Contains(out, "2 files changed") || strings.Contains(out, "+var") {
		t.Errorf("stat:\n%s", out)
	}
}

// base diffs from where HEAD forked from it, so commits base gained since
// don't show; commit shows one commit.
func TestDiffAgainstABaseAndOfOneCommit(t *testing.T) {
	dir := repo(t)
	sh(t, dir, "switch", "-q", "-c", "feature")
	write(t, dir, "a.txt", "one\nfeature\n")
	sh(t, dir, "commit", "-qam", "feature work")
	sh(t, dir, "switch", "-q", "main")
	write(t, dir, "lib/b.go", "package lib\n// main moved on\n")
	sh(t, dir, "commit", "-qam", "main moved on")
	sh(t, dir, "switch", "-q", "feature")
	write(t, dir, "lib/c.go", "package lib\n")
	s := source(t, Config{Root: dir})

	out, isErr := call(t, s, "git_diff", map[string]any{"base": "main"})
	if isErr || !strings.Contains(out, "+feature") || strings.Contains(out, "main moved on") {
		t.Errorf("base main:\n%s", out)
	}
	out, _ = call(t, s, "git_diff", map[string]any{"commit": "HEAD"})
	if !strings.Contains(out, "feature work") || !strings.Contains(out, "+feature") || strings.Contains(out, "lib/c.go") {
		t.Errorf("commit HEAD:\n%s", out)
	}
}

// A ref git would read as an option, a path outside the root, and both
// targets at once are refused as results the model can act on, and the
// option never reaches git. control: a plain ref goes through.
func TestDiffRefusesWhatGitWouldMisread(t *testing.T) {
	dir := repo(t)
	s := source(t, Config{Root: dir})
	out := filepath.Join(dir, "leaked")
	for _, args := range []map[string]any{
		{"base": "--output=" + out},
		{"commit": "--output=" + out},
		{"commit": "-p"},
		{"base": "main", "commit": "HEAD"},
		{"path": "../elsewhere"},
		{"base": "main branch"},
	} {
		if text, isErr := call(t, s, "git_diff", args); !isErr {
			t.Errorf("%v: accepted\n%s", args, text)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Errorf("an option in base reached git and wrote %s", out)
	}
	if _, isErr := call(t, s, "git_diff", map[string]any{"base": "main~0"}); isErr {
		t.Errorf("control: main~0 was refused")
	}
}

// With Root a directory inside the repository, a path that leaves Root is
// refused even though it's still inside the repository, where git alone
// would accept it.
func TestPathsStayUnderARootInsideTheRepository(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "changed\n")
	s := source(t, Config{Root: filepath.Join(dir, "lib")})
	if out, isErr := call(t, s, "git_diff", map[string]any{"path": "../a.txt"}); !isErr {
		t.Errorf("../a.txt from lib was accepted:\n%s", out)
	}
	if out, isErr := call(t, s, "git_log", map[string]any{"path": "../a.txt"}); !isErr {
		t.Errorf("git_log ../a.txt from lib was accepted:\n%s", out)
	}
	if _, isErr := call(t, s, "git_diff", map[string]any{"path": "b.go"}); isErr {
		t.Errorf("control: b.go under lib was refused")
	}
}

func TestLogListsCommitsForAPath(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "changed\n")
	sh(t, dir, "commit", "-qam", "touch a")
	s := source(t, Config{Root: dir})
	out, _ := call(t, s, "git_log", nil)
	if lines := strings.Count(out, "\n"); lines != 2 || !strings.Contains(out, "touch a") {
		t.Errorf("log:\n%s", out)
	}
	if out, _ := call(t, s, "git_log", map[string]any{"path": "lib", "limit": float64(5)}); strings.Contains(out, "touch a") || !strings.Contains(out, "initial") {
		t.Errorf("log lib:\n%s", out)
	}
	if out, _ := call(t, s, "git_log", map[string]any{"limit": float64(1)}); strings.Count(out, "\n") != 1 {
		t.Errorf("limit 1:\n%s", out)
	}
}

// In a repository with no commits, diff is against the empty tree and log
// says there's nothing yet.
func TestAnEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	sh(t, dir, "init", "-q")
	write(t, dir, "x.txt", "x\n")
	sh(t, dir, "add", "x.txt")
	s := source(t, Config{Root: dir})
	if out, isErr := call(t, s, "git_diff", nil); isErr || !strings.Contains(out, "+x") {
		t.Errorf("diff:\n%s", out)
	}
	if out, _ := call(t, s, "git_log", nil); out != "(no commits yet)\n" {
		t.Errorf("log: %q", out)
	}
}

func TestCommitAndBranch(t *testing.T) {
	dir := repo(t)
	s := source(t, Config{Root: dir, Mutations: true})
	if _, isErr := call(t, s, "git_commit", map[string]any{"message": "nothing"}); !isErr {
		t.Errorf("nothing to commit was accepted")
	}
	write(t, dir, "new.txt", "hi\n")
	out, isErr := call(t, s, "git_commit", map[string]any{"message": "add new", "all": true})
	if isErr || !strings.Contains(out, "committed") || !strings.Contains(out, "new.txt") {
		t.Errorf("commit all:\n%s", out)
	}
	if st := sh(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("left uncommitted: %q", st)
	}
	if _, isErr := call(t, s, "git_branch", map[string]any{"name": "-D"}); !isErr {
		t.Errorf("an option as a branch name was accepted")
	}
	if out, isErr := call(t, s, "git_branch", map[string]any{"name": "topic"}); isErr || strings.TrimSpace(sh(t, dir, "branch", "--show-current")) != "topic" {
		t.Errorf("branch: %s", out)
	}
}

func TestWorktreesLandUnderTheirDirectory(t *testing.T) {
	dir := repo(t)
	wt := t.TempDir()
	s := source(t, Config{Root: dir, WorktreeDir: wt})
	if _, isErr := call(t, s, "git_worktree_add", map[string]any{"name": "../escape"}); !isErr {
		t.Errorf("a name with a path in it was accepted")
	}
	out, isErr := call(t, s, "git_worktree_add", map[string]any{"name": "task-1"})
	if isErr {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(wt, "task-1", "a.txt")); err != nil {
		t.Errorf("worktree not created under %s: %s", wt, out)
	}
	if !strings.Contains(sh(t, dir, "branch"), "task-1") {
		t.Errorf("no branch task-1")
	}
	if out, isErr := call(t, s, "git_worktree_remove", map[string]any{"name": "task-1"}); isErr {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(wt, "task-1")); !os.IsNotExist(err) {
		t.Errorf("worktree still there: %v", err)
	}
}
