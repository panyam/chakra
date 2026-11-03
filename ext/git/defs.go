package git

import (
	"fmt"
	osexec "os/exec"

	"github.com/panyam/mcpkit/core"
)

// lookGit finds git on PATH once, at construction, as ext/exec does with
// its commands, so a missing git fails startup rather than the first call.
func lookGit() (string, error) {
	p, err := osexec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git: git is not on PATH: %w", err)
	}
	return p, nil
}

// readOnly lets the approval gate run a tool without asking, and destructive
// makes the gate ask first. Each returns a fresh map, since a caller may write
// into a ToolDef's annotations. Hints are per ToolDef (CLAUDE.md, Traps), so
// there is one tool per operation. A single "git" tool would give a commit a
// read's disposition, or a status a commit's.
func readOnly() map[string]any    { return map[string]any{"readOnlyHint": true} }
func destructive() map[string]any { return map[string]any{"destructiveHint": true} }

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func readDefs() []core.ToolDef {
	return []core.ToolDef{
		{
			Name:        "git_status",
			Title:       "Git status",
			Annotations: readOnly(),
			Description: "Show the branch and every changed, staged and untracked file in the workspace, one per line in git's short format.",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name:        "git_diff",
			Title:       "Git diff",
			Annotations: readOnly(),
			Description: "Show changes as a unified diff. With no base or commit: everything not yet committed (staged and unstaged, against HEAD), with untracked files listed after. With base: everything since this branch forked from base (their merge base), committed or not. With commit: that one commit. Give path to narrow it, or stat for files and line counts only.",
			InputSchema: obj(map[string]any{
				"base":   str("A branch or commit to diff against from where HEAD forked from it, e.g. main."),
				"commit": str("A commit to show on its own, e.g. HEAD~1 or a commit id."),
				"path":   str("A file or directory to narrow the diff to, relative to the workspace root."),
				"stat":   boolean("List changed files and line counts instead of the patch."),
			}),
		},
		{
			Name:        "git_log",
			Title:       "Git log",
			Annotations: readOnly(),
			Description: "List recent commits, newest first: short id, date, author and subject. Give path for the commits that touched it.",
			InputSchema: obj(map[string]any{
				"path":  str("A file or directory, relative to the workspace root."),
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "How many commits, 20 by default."},
			}),
		},
	}
}

func mutationDefs() []core.ToolDef {
	return []core.ToolDef{
		{
			Name:        "git_commit",
			Title:       "Git commit",
			Annotations: destructive(),
			Description: "Commit what is staged, or with all set, every change in the workspace including new files. The repository's commit hooks run.",
			InputSchema: obj(map[string]any{
				"message": str("The commit message."),
				"all":     boolean("Stage every change, new and deleted files included, before committing."),
			}, "message"),
		},
		{
			Name:        "git_branch",
			Title:       "Git branch",
			Annotations: destructive(),
			Description: "Create a branch at HEAD and switch to it. Uncommitted changes carry over.",
			InputSchema: obj(map[string]any{"name": str("The new branch's name.")}, "name"),
		},
	}
}

func worktreeDefs() []core.ToolDef {
	return []core.ToolDef{
		{
			Name:        "git_worktree_add",
			Title:       "Add a git worktree",
			Annotations: destructive(),
			Description: "Create a separate working copy at HEAD on a new branch, for work kept apart from this workspace. It is made in the configured worktree directory under name, and the result says where.",
			InputSchema: obj(map[string]any{
				"name":   str("The worktree's directory name: letters, digits, '.', '_' or '-'."),
				"branch": str("The new branch to create for it; name by default."),
			}, "name"),
		},
		{
			Name:        "git_worktree_remove",
			Title:       "Remove a git worktree",
			Annotations: destructive(),
			Description: "Remove a worktree git_worktree_add made. Its branch is kept. Refused when it has uncommitted changes, unless force is set.",
			InputSchema: obj(map[string]any{
				"name":  str("The worktree's directory name."),
				"force": boolean("Remove it even with uncommitted changes, which are lost."),
			}, "name"),
		},
	}
}
