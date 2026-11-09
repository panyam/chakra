package surfaces

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/panyam/chakra/host"
)

// SkillPresets maps a preset name to a starting list of skill folders. The
// codex and claude lists match the folders those agents read. A relative
// folder is read under each workspace root, and one starting with "~" is the
// user's own.
//
//   - codex: .agents/skills in the repository and ~/.agents/skills, per the
//     Codex docs. Codex also walks up from the working directory to the
//     repository root. Here the working directory is the workspace root.
//   - claude: .claude/skills and ~/.claude/skills, per Claude Code's docs.
//   - chakra, the default: both repository folders and ~/.agents/skills.
var SkillPresets = map[string][]string{
	"codex":  {".agents/skills", "~/.agents/skills"},
	"claude": {".claude/skills", "~/.claude/skills"},
	"chakra": {".agents/skills", ".claude/skills", "~/.agents/skills"},
}

// defaultSkillPreset is the preset WorkspaceSkillDirs uses when the config
// names neither a preset nor a list.
const defaultSkillPreset = "chakra"

// skillFolders resolves a skills setting to a folder list. It returns dirs
// when dirs is non-nil, and an empty non-nil list means no folders.
// Otherwise it returns the named preset's list, or the default preset's when
// preset is empty.
func skillFolders(preset string, dirs []string) ([]string, error) {
	if dirs != nil {
		return dirs, nil
	}
	if preset == "" {
		preset = defaultSkillPreset
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
// folders for host.WithSkillDirs. A relative folder appears once per root,
// and a "~" or absolute folder appears once. It keeps folders that don't
// exist. The host skips those quietly, so the defaults can name folders
// missing from a repository. It returns nil when there are no roots.
func WorkspaceSkillDirs(cfg WorkspaceConfig) ([]host.SkillDir, error) {
	if len(cfg.Roots) == 0 {
		return nil, nil
	}
	folders, err := skillFolders(cfg.SkillsPreset, cfg.SkillDirs)
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
