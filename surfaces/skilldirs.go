package surfaces

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/panyam/chakra/host"
)

// SkillPresets are the skill folders other coding agents read, as the
// starting list a workspace's skills come from. A relative folder is read
// under each workspace root; one starting with "~" is the user's own.
//
//   - codex: .agents/skills in the repository and ~/.agents/skills (the
//     Codex docs; Codex also walks from the working directory up to the
//     repository root, and here the workspace root is that directory).
//   - claude: .claude/skills and ~/.claude/skills (Claude Code's docs).
//   - chakra, the default: both repository folders and ~/.agents/skills.
var SkillPresets = map[string][]string{
	"codex":  {".agents/skills", "~/.agents/skills"},
	"claude": {".claude/skills", "~/.claude/skills"},
	"chakra": {".agents/skills", ".claude/skills", "~/.agents/skills"},
}

// DefaultSkillPreset is the preset WorkspaceSkillDirs uses when the config
// names neither a preset nor a list.
const DefaultSkillPreset = "chakra"

// SkillFolders resolves a skills setting to a folder list: dirs when set
// (an empty, non-nil list means none), else the named preset, else the
// default preset.
func SkillFolders(preset string, dirs []string) ([]string, error) {
	if dirs != nil {
		return dirs, nil
	}
	if preset == "" {
		preset = DefaultSkillPreset
	}
	list, ok := SkillPresets[preset]
	if !ok {
		names := make([]string, 0, len(SkillPresets))
		for n := range SkillPresets {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("skills: no preset %q (have %v)", preset, names)
	}
	return append([]string(nil), list...), nil
}

// WorkspaceSkillDirs turns the workspace's skill folders into host skill
// folders for host.WithSkillDirs: each relative folder once per root, each
// "~" or absolute folder once. Folders that don't exist are kept; the host
// skips them quietly, so the defaults can name folders a repository doesn't
// have. Nil when there are no roots.
func WorkspaceSkillDirs(cfg WorkspaceConfig) ([]host.SkillDir, error) {
	if len(cfg.Roots) == 0 {
		return nil, nil
	}
	folders, err := SkillFolders(cfg.SkillsPreset, cfg.SkillDirs)
	if err != nil {
		return nil, err
	}
	var out []host.SkillDir
	for _, f := range folders {
		switch {
		case f == "~" || strings.HasPrefix(f, "~/"):
			out = append(out, host.SkillDir{Path: f, Label: f, Own: true})
		case filepath.IsAbs(f):
			out = append(out, host.SkillDir{Path: f, Label: f})
		default:
			for _, root := range cfg.Roots {
				label := f
				if len(cfg.Roots) > 1 {
					label = filepath.Base(root) + "/" + f
				}
				out = append(out, host.SkillDir{Path: filepath.Join(root, f), Label: label})
			}
		}
	}
	return out, nil
}
