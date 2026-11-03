package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// DefaultMaxOutput caps a tool's text when Config.MaxOutput is 0.
const DefaultMaxOutput = 64 << 10

// Config configures the git tools.
type Config struct {
	// Root is the workspace directory. It must be inside a git work tree,
	// and every path a tool takes is confined to it. Required.
	Root string

	// Mutations offers git_commit and git_branch. Off by default, so a
	// workspace gets read-only git unless the wiring asks for more.
	Mutations bool

	// WorktreeDir, when set, offers git_worktree_add and git_worktree_remove,
	// which create and remove worktrees as directories directly under it,
	// named by the call. It's kept apart from Root so a worktree never lands
	// inside the tree it was made from.
	WorktreeDir string

	// MaxOutput caps each tool's text in bytes, DefaultMaxOutput when 0. A
	// longer output is cut with a note saying how much was left out.
	MaxOutput int
}

// Source is the git tool set over one workspace, an agent.ToolSource.
type Source struct {
	git  runner
	cfg  Config
	defs []core.ToolDef
}

// NewSource checks that git is on PATH and that Root is inside a git work
// tree, and returns the tools Config asks for.
func NewSource(cfg Config) (*Source, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("git: Config.Root is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	bin, err := lookGit()
	if err != nil {
		return nil, err
	}
	if _, err := workTree(context.Background(), bin, root); err != nil {
		return nil, err
	}
	if cfg.MaxOutput <= 0 {
		cfg.MaxOutput = DefaultMaxOutput
	}
	if cfg.WorktreeDir != "" {
		if cfg.WorktreeDir, err = filepath.Abs(cfg.WorktreeDir); err != nil {
			return nil, err
		}
	}
	cfg.Root = root
	s := &Source{git: runner{bin: bin, root: root, max: cfg.MaxOutput}, cfg: cfg}
	s.defs = readDefs()
	if cfg.Mutations {
		s.defs = append(s.defs, mutationDefs()...)
	}
	if cfg.WorktreeDir != "" {
		s.defs = append(s.defs, worktreeDefs()...)
	}
	return s, nil
}

// Tools lists the tools Config enabled: the reads always, the mutations and
// worktree tools only when asked for.
func (s *Source) Tools(context.Context) ([]core.ToolDef, error) {
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call runs one tool. A git failure (an unknown ref, nothing to commit) comes
// back as an IsError result for the model to act on; an error is returned
// only for a tool this source doesn't offer.
func (s *Source) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if !s.offers(name) {
		return nil, fmt.Errorf("git: unknown tool %q", name)
	}
	var out string
	var err error
	switch name {
	case "git_status":
		out, err = s.status(ctx)
	case "git_diff":
		out, err = s.diff(ctx, args)
	case "git_log":
		out, err = s.log(ctx, args)
	case "git_commit":
		out, err = s.commit(ctx, args)
	case "git_branch":
		out, err = s.branch(ctx, args)
	case "git_worktree_add":
		out, err = s.worktreeAdd(ctx, args)
	case "git_worktree_remove":
		out, err = s.worktreeRemove(ctx, args)
	}
	if err != nil {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: s.git.capped(out)}}}, nil
}

func (s *Source) offers(name string) bool {
	for _, d := range s.defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func (s *Source) status(ctx context.Context) (string, error) {
	out, err := s.git.run(ctx, nil, "status", "--porcelain=v1", "--branch", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if strings.Count(out, "\n") <= 1 {
		out += "(clean: nothing changed, nothing untracked)\n"
	}
	return out, nil
}

func (s *Source) diff(ctx context.Context, args map[string]any) (string, error) {
	base, _ := args["base"].(string)
	commit, _ := args["commit"].(string)
	stat, _ := args["stat"].(bool)
	p, _ := args["path"].(string)
	return s.Diff(ctx, DiffTarget{Base: base, Commit: commit, Path: p, Stat: stat})
}

// DiffTarget says which changes Diff reports. With neither Base nor Commit
// set, it's the uncommitted ones.
type DiffTarget struct {
	// Base diffs the working tree against where HEAD forked from this
	// branch or commit (their merge base), so only this branch's changes
	// show, not ones Base gained since.
	Base string
	// Commit is that one commit's change.
	Commit string
	// Path narrows the diff to a file or directory under the root.
	Path string
	// Stat reports files and line counts instead of the patch.
	Stat bool
}

// Diff returns the changes t names as git prints them, untracked files listed
// after an uncommitted diff, since git diff leaves them out. It is the same
// text git_diff answers with, uncapped.
func (s *Source) Diff(ctx context.Context, t DiffTarget) (string, error) {
	if t.Base != "" && t.Commit != "" {
		return "", fmt.Errorf("give base or commit, not both")
	}
	rel, err := relPath(s.cfg.Root, t.Path)
	if err != nil {
		return "", err
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv"}
	switch {
	case t.Commit != "":
		if err := checkRef("commit", t.Commit); err != nil {
			return "", err
		}
		args = []string{"show", "--no-ext-diff", "--no-textconv", "--format=commit %H%nAuthor: %an <%ae>%nDate:   %ad%n%n%w(0,4,4)%B"}
		if t.Stat {
			args = append(args, "--stat")
		}
		args = append(args, t.Commit)
	case t.Base != "":
		if err := checkRef("base", t.Base); err != nil {
			return "", err
		}
		mb, err := s.git.run(ctx, nil, "merge-base", t.Base, "HEAD")
		if err != nil {
			return "", err
		}
		if t.Stat {
			args = append(args, "--stat")
		}
		args = append(args, strings.TrimSpace(mb))
	default:
		if t.Stat {
			args = append(args, "--stat")
		}
		args = append(args, s.head(ctx))
	}
	if rel != "" {
		args = append(args, "--", rel)
	}
	out, err := s.git.run(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	if t.Commit == "" && t.Base == "" {
		untracked, err := s.untracked(ctx, rel)
		if err != nil {
			return "", err
		}
		if untracked != "" {
			out += "\nuntracked (not in the diff above):\n" + untracked
		}
	}
	if out == "" {
		out = "(no changes)\n"
	}
	return out, nil
}

// head is HEAD, or the empty tree in a repository with no commits yet.
func (s *Source) head(ctx context.Context) string {
	if _, err := s.git.run(ctx, nil, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return emptyTree
	}
	return "HEAD"
}

func (s *Source) untracked(ctx context.Context, rel string) (string, error) {
	args := []string{"ls-files", "--others", "--exclude-standard"}
	if rel != "" {
		args = append(args, "--", rel)
	}
	out, err := s.git.run(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			b.WriteString("  " + l + "\n")
		}
	}
	return b.String(), nil
}

func (s *Source) log(ctx context.Context, args map[string]any) (string, error) {
	n := 20
	if v, ok := args["limit"].(float64); ok && v >= 1 {
		n = min(int(v), 200)
	}
	p, _ := args["path"].(string)
	rel, err := relPath(s.cfg.Root, p)
	if err != nil {
		return "", err
	}
	if s.head(ctx) == emptyTree {
		return "(no commits yet)\n", nil
	}
	a := []string{"log", "--date=short", "--format=%h %ad %an  %s", fmt.Sprintf("-n%d", n)}
	if rel != "" {
		a = append(a, "--", rel)
	}
	return s.git.run(ctx, nil, a...)
}

func (s *Source) commit(ctx context.Context, args map[string]any) (string, error) {
	msg, _ := args["message"].(string)
	if strings.TrimSpace(msg) == "" {
		return "", fmt.Errorf("a commit needs a message")
	}
	if all, _ := args["all"].(bool); all {
		if _, err := s.git.run(ctx, nil, "add", "-A"); err != nil {
			return "", err
		}
	}
	if _, err := s.git.run(ctx, nil, "commit", "-m", msg); err != nil {
		return "", err
	}
	return s.git.run(ctx, nil, "log", "-1", "--format=committed %h %s", "--stat")
}

func (s *Source) branch(ctx context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if _, err := s.git.run(ctx, nil, "check-ref-format", "--branch", name); err != nil || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("%q is not a branch name git accepts", name)
	}
	if _, err := s.git.run(ctx, nil, "switch", "-c", name); err != nil {
		return "", err
	}
	return fmt.Sprintf("created branch %s from HEAD and switched to it\n", name), nil
}

func (s *Source) worktreePath(args map[string]any) (string, string, error) {
	name, _ := args["name"].(string)
	if !namePattern.MatchString(name) {
		return "", "", fmt.Errorf("worktree name %q: use letters, digits, '.', '_' or '-'", name)
	}
	return name, filepath.Join(s.cfg.WorktreeDir, name), nil
}

func (s *Source) worktreeAdd(ctx context.Context, args map[string]any) (string, error) {
	name, dir, err := s.worktreePath(args)
	if err != nil {
		return "", err
	}
	branch, _ := args["branch"].(string)
	if branch == "" {
		branch = name
	}
	if _, err := s.git.run(ctx, nil, "check-ref-format", "--branch", branch); err != nil || strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("%q is not a branch name git accepts", branch)
	}
	if _, err := s.git.run(ctx, nil, "worktree", "add", "-b", branch, dir, "HEAD"); err != nil {
		return "", err
	}
	return fmt.Sprintf("created worktree %s on new branch %s, from HEAD\n", dir, branch), nil
}

func (s *Source) worktreeRemove(ctx context.Context, args map[string]any) (string, error) {
	_, dir, err := s.worktreePath(args)
	if err != nil {
		return "", err
	}
	a := []string{"worktree", "remove"}
	if force, _ := args["force"].(bool); force {
		a = append(a, "--force")
	}
	if _, err := s.git.run(ctx, nil, append(a, dir)...); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed worktree %s (its branch is kept)\n", dir), nil
}
