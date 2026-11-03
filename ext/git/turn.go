package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// TurnDiff is what a turn changed in the workspace: the difference between
// a snapshot taken when the turn started and one taken when it ended. It
// counts every file git would see, untracked ones included and ignored ones
// left out, whichever tool or command wrote them.
type TurnDiff struct {
	// Before and After are the snapshots' git tree ids, so a caller can ask
	// git for more (git diff Before After -- path).
	Before, After string
	// Stat is git's per-file summary, and Patch the unified diff. Both are
	// empty when the turn changed nothing.
	Stat, Patch string
	// Files, Added and Deleted count the changed files and lines.
	Files, Added, Deleted int
}

// Empty reports whether the turn changed nothing.
func (d TurnDiff) Empty() bool { return d.Before == d.After }

// Summary is the one line the extension announces, such as "this turn
// changed 3 files (+12 -4)".
func (d TurnDiff) Summary() string {
	if d.Empty() {
		return "this turn changed no files"
	}
	noun := "files"
	if d.Files == 1 {
		noun = "file"
	}
	return fmt.Sprintf("this turn changed %d %s (+%d -%d)", d.Files, noun, d.Added, d.Deleted)
}

// snapshot records the workspace as a git tree without touching the user's
// index or files. It copies the real index to a scratch file, so git only
// rehashes files whose stat changed, stages everything into the copy and
// writes it as a tree. The blobs land in the repository's object store,
// unreferenced, where git's own gc removes them in time.
func (s *Source) snapshot(ctx context.Context) (string, error) {
	tmp, err := os.CreateTemp("", "chakra-git-index-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if real, err := s.git.run(ctx, nil, "rev-parse", "--path-format=absolute", "--git-path", "index"); err == nil {
		if f, err := os.Open(strings.TrimSpace(real)); err == nil {
			_, err = io.Copy(tmp, f)
			f.Close()
			if err != nil {
				tmp.Close()
				return "", err
			}
		}
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + tmpName}
	if fi, _ := os.Stat(tmpName); fi.Size() == 0 {
		os.Remove(tmpName) // git refuses an empty file as an index; none at all is a fresh one
	}
	top, err := workTree(ctx, s.git.bin, s.cfg.Root)
	if err != nil {
		return "", err
	}
	whole := runner{bin: s.git.bin, root: top, max: s.git.max}
	if _, err := whole.run(ctx, env, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	tree, err := whole.run(ctx, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tree), nil
}

// diffTrees is what changed from one snapshot to the next, under Root.
func (s *Source) diffTrees(ctx context.Context, before, after string) (TurnDiff, error) {
	d := TurnDiff{Before: before, After: after}
	if d.Empty() {
		return d, nil
	}
	scope := []string{"--", "."}
	var err error
	if d.Stat, err = s.git.run(ctx, nil, append([]string{"diff", "--no-ext-diff", "--no-textconv", "--stat", before, after}, scope...)...); err != nil {
		return d, err
	}
	if d.Patch, err = s.git.run(ctx, nil, append([]string{"diff", "--no-ext-diff", "--no-textconv", before, after}, scope...)...); err != nil {
		return d, err
	}
	nums, err := s.git.run(ctx, nil, append([]string{"diff", "--numstat", before, after}, scope...)...)
	if err != nil {
		return d, err
	}
	for _, l := range strings.Split(strings.TrimSpace(nums), "\n") {
		var a, del int
		var path string
		if l == "" {
			continue
		}
		d.Files++
		if n, _ := fmt.Sscanf(l, "%d\t%d\t%s", &a, &del, &path); n == 3 {
			d.Added += a
			d.Deleted += del
		}
	}
	if d.Files == 0 {
		// the trees differ only outside Root
		d.Before = d.After
		d.Stat, d.Patch = "", ""
	}
	return d, nil
}
