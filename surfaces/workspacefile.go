package surfaces

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/panyam/chakra/ext/agentsmd"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/ext/git"
	"github.com/panyam/chakra/ext/lsp"
)

// WorkspaceFile holds workspace settings as JSON. It covers the settings too
// big for a flag, such as the exec allowlist, the language servers and the
// repo map.
//
// Roots are deliberately absent, and only --workspace sets them.
// WorkspaceConfig.Roots documents the rule. Nothing picks the confinement on
// the user's behalf, and a checked-in file that named directories would do
// exactly that. The exec commands run and write in the same roots as the
// file tools.
type WorkspaceFile struct {
	// Exec enables the allowlisted command tools. Absent means none.
	Exec *ExecFile `json:"exec,omitempty"`

	// LanguageServers enables LSP-in-loop; see WorkspaceConfig.LanguageServers.
	LanguageServers []LanguageServerFile `json:"languageServers,omitempty"`

	// RepoMap adds the ranked repository map; see WorkspaceConfig.RepoMap.
	RepoMap bool `json:"repoMap,omitempty"`

	// EditTools picks the editing tools: ["edit_file"], ["apply_patch"], or
	// both (absent). See WorkspaceConfig.EditTools.
	EditTools []string `json:"editTools,omitempty"`

	// Instructions picks how AGENTS.md and similar files load: a "preset"
	// ("chakra", "codex" or "claude"), any agentsmd.Profile field to change on
	// top of it, or "off": true. Absent means the chakra preset.
	//
	//	"instructions": {"preset": "codex", "maxBytes": 65536}
	Instructions json.RawMessage `json:"instructions,omitempty"`

	// Skills picks the folders local skills come from: a preset ("chakra",
	// "codex" or "claude") and/or a dirs list, which replaces the preset's.
	//
	//	"skills": {"dirs": [".agents/skills", "~/my-skills"]}
	Skills *SkillsFile `json:"skills,omitempty"`

	// Plan adds update_plan; see WorkspaceConfig.Plan.
	Plan bool `json:"plan,omitempty"`

	// Git enables ext/git over the first root. Absent means none.
	//
	//	"git": {"review": true}
	Git *GitFile `json:"git,omitempty"`
}

// GitFile is the workspace file's git section. Each field turns on more than
// the read-only tools, which are always there.
type GitFile struct {
	// Mutations offers git_commit and git_branch.
	Mutations bool `json:"mutations,omitempty"`
	// WorktreeDir offers the worktree tools, creating worktrees under it.
	WorktreeDir string `json:"worktreeDir,omitempty"`
	// Review offers the review tool and /review, with the default rubric.
	Review bool `json:"review,omitempty"`
}

// SkillsFile is the workspace file's skills section.
type SkillsFile struct {
	Preset string   `json:"preset,omitempty"`
	Dirs   []string `json:"dirs,omitempty"`
}

// instructionsFile is the decoded form of WorkspaceFile.Instructions.
type instructionsFile struct {
	Preset string `json:"preset"`
	Off    bool   `json:"off"`
	agentsmd.Profile
}

// InstructionsProfile decodes an "instructions" object. It starts from the
// named preset, or agentsmd.DefaultPreset when none is named, and lays the
// object's other fields over it. off reports "off": true. Unknown fields are
// an error, like everywhere else in the file.
func InstructionsProfile(raw json.RawMessage) (p *agentsmd.Profile, off bool, err error) {
	var head struct {
		Preset string `json:"preset"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, false, fmt.Errorf("instructions: %w", err)
	}
	name := head.Preset
	if name == "" {
		name = agentsmd.DefaultPreset
	}
	base, err := agentsmd.Preset(name)
	if err != nil {
		return nil, false, fmt.Errorf("instructions: %w", err)
	}
	f := instructionsFile{Profile: base}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, false, fmt.Errorf("instructions: %w", err)
	}
	return &f.Profile, f.Off, nil
}

// ExecFile is the JSON form of exec.Config, minus Roots.
type ExecFile struct {
	// Sandbox is "platform" or empty for the platform's backend, and
	// construction fails where no backend exists. "unconfined" runs with no
	// sandbox. The opt-out is a word the operator has to write, matching
	// exec.Unconfined.
	Sandbox string `json:"sandbox,omitempty"`

	// Timeout is a Go duration string ("2m"). Empty means exec.DefaultTimeout.
	Timeout string `json:"timeout,omitempty"`

	// MaxOutput caps how many bytes of each output stream exec holds for a
	// command. Zero means exec.DefaultMaxOutput.
	MaxOutput int `json:"maxOutput,omitempty"`

	// InlineOutput caps how many bytes of each stream go into a result, and
	// negative means all of it. Zero means exec.DefaultInlineOutput, or all of it when
	// the host offloads (see WorkspaceConfig.Offloading).
	InlineOutput int `json:"inlineOutput,omitempty"`

	// WritePaths and DenyRead treat nil and empty as exec.Config does. Absent
	// selects the package default, and [] selects none.
	WritePaths []string `json:"writePaths"`
	DenyRead   []string `json:"denyRead"`

	// Commands is the allowlist, one tool per entry.
	Commands []CommandFile `json:"commands"`

	// Shell turns on the general "shell" tool with these prefix rules. See
	// exec.ShellConfig.
	Shell *ShellFile `json:"shell,omitempty"`

	// Network lets commands reach the listed hosts through a local proxy:
	// {"allow": ["proxy.golang.org", "*.npmjs.org"], "ask": true}. See
	// exec.NetworkConfig.
	Network *exec.NetworkConfig `json:"network,omitempty"`
}

// ShellFile is exec.ShellConfig as JSON.
type ShellFile struct {
	Rules        []ShellRuleFile `json:"rules"`
	Timeout      string          `json:"timeout,omitempty"`
	AllowNetwork bool            `json:"allowNetwork,omitempty"`
	// Processes turns on exec_command, write_stdin, list_processes and
	// kill_process: true for the defaults, or an object with limits.
	Processes *ProcessesFile `json:"processes,omitempty"`
}

// ProcessesFile is exec.ProcessConfig: true for the defaults, false or absent
// for off, or {"max": 4, "maxOutput": 262144, "defaultYield": "5s",
// "maxYield": "30s", "streamBudget": 1048576, "detach": true}. A negative
// streamBudget streams without limit. detach is exec.ProcessConfig.AllowDetach.
type ProcessesFile struct {
	On           bool
	Max          int    `json:"max,omitempty"`
	MaxOutput    int    `json:"maxOutput,omitempty"`
	DefaultYield string `json:"defaultYield,omitempty"`
	MaxYield     string `json:"maxYield,omitempty"`
	StreamBudget int    `json:"streamBudget,omitempty"`
	Detach       bool   `json:"detach,omitempty"`
}

// UnmarshalJSON accepts true, false, or an object.
func (p *ProcessesFile) UnmarshalJSON(b []byte) error {
	var on bool
	if err := json.Unmarshal(b, &on); err == nil {
		*p = ProcessesFile{On: on}
		return nil
	}
	type plain ProcessesFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var v plain
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("exec.shell.processes: %w", err)
	}
	*p = ProcessesFile(v)
	p.On = true
	return nil
}

// ShellRuleFile is one exec.PrefixRule. Each prefix position is a word or a
// list of alternative words: ["git", ["status", "diff"]].
type ShellRuleFile struct {
	Prefix        []PrefixWord `json:"prefix"`
	Decision      string       `json:"decision"`
	Justification string       `json:"justification,omitempty"`
	Match         [][]string   `json:"match,omitempty"`
	NotMatch      [][]string   `json:"notMatch,omitempty"`
}

// PrefixWord is one position of a shell rule's prefix: a JSON string or an
// array of strings.
type PrefixWord []string

// UnmarshalJSON accepts "word" or ["word", "other"].
func (w *PrefixWord) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*w = PrefixWord{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("a prefix word is a string or an array of strings, got %s", b)
	}
	*w = many
	return nil
}

// CommandFile is the JSON form of exec.CommandSpec.
type CommandFile struct {
	Name         string    `json:"name"`
	Argv         []string  `json:"argv"`
	Dir          string    `json:"dir,omitempty"`
	Description  string    `json:"description"`
	Args         *ArgsFile `json:"args,omitempty"`
	AllowNetwork bool      `json:"allowNetwork,omitempty"`
	ReadOnly     bool      `json:"readOnly,omitempty"`
	Reversible   bool      `json:"reversible,omitempty"`
	Timeout      string    `json:"timeout,omitempty"`
}

// ArgsFile is the JSON form of exec.ArgPolicy.
type ArgsFile struct {
	Max   int    `json:"max"`
	Match string `json:"match"`
	Paths bool   `json:"paths,omitempty"`
}

// LanguageServerFile is the JSON form of lsp.ServerSpec.
type LanguageServerFile struct {
	Command    []string `json:"command"`
	Extensions []string `json:"extensions"`
	LanguageID string   `json:"languageId,omitempty"`
}

// LoadWorkspaceFile reads and decodes a workspace file.
//
// Unknown fields are an error. A misspelt "readOnly" or "sandbox" would
// otherwise decode to its zero value, and in this file the zero values
// change what runs and what gets asked about.
func LoadWorkspaceFile(path string) (*WorkspaceFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workspace file: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f WorkspaceFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("workspace file %s: %w", path, err)
	}
	return &f, nil
}

// LoadFile applies the workspace file at path onto c. An empty path is a
// no-op, so a surface can pass its --workspace-config flag through unchecked.
func (c *WorkspaceConfig) LoadFile(path string) error {
	if path == "" {
		return nil
	}
	f, err := LoadWorkspaceFile(path)
	if err != nil {
		return err
	}
	return f.Apply(c)
}

// Apply copies the file's settings onto cfg. It leaves Roots, Exclude and the
// checkpoint settings untouched, since flags set those.
func (f *WorkspaceFile) Apply(cfg *WorkspaceConfig) error {
	if f == nil {
		return nil
	}
	cfg.RepoMap = f.RepoMap
	if f.EditTools != nil {
		cfg.EditTools = f.EditTools
	}
	if len(f.Instructions) > 0 {
		p, off, err := InstructionsProfile(f.Instructions)
		if err != nil {
			return err
		}
		cfg.Instructions, cfg.NoInstructions = p, off
	}
	if f.Skills != nil {
		if _, err := SkillFolders(f.Skills.Preset, f.Skills.Dirs); err != nil {
			return err
		}
		cfg.SkillsPreset = f.Skills.Preset
		if f.Skills.Dirs != nil {
			cfg.SkillDirs = f.Skills.Dirs
		}
	}
	for _, ls := range f.LanguageServers {
		cfg.LanguageServers = append(cfg.LanguageServers, lsp.ServerSpec{
			Command:    ls.Command,
			Extensions: ls.Extensions,
			LanguageID: ls.LanguageID,
		})
	}
	cfg.Plan = f.Plan
	if f.Git != nil {
		cfg.Git = &git.Config{Mutations: f.Git.Mutations, WorktreeDir: f.Git.WorktreeDir}
		if f.Git.Review {
			cfg.Git.Review = &git.ReviewConfig{}
		}
	}
	if f.Exec != nil {
		ec, err := f.Exec.config()
		if err != nil {
			return err
		}
		cfg.Exec = ec
	}
	return nil
}

func (e *ExecFile) config() (*exec.Config, error) {
	cfg := &exec.Config{
		MaxOutput:    e.MaxOutput,
		InlineOutput: e.InlineOutput,
		WritePaths:   e.WritePaths,
		DenyRead:     e.DenyRead,
	}
	switch e.Sandbox {
	case "", "platform":
	case "unconfined":
		cfg.Sandbox = exec.Unconfined()
	default:
		return nil, fmt.Errorf("workspace file: exec.sandbox %q: want \"platform\" or \"unconfined\"", e.Sandbox)
	}
	var err error
	if cfg.Timeout, err = parseDuration("exec.timeout", e.Timeout); err != nil {
		return nil, err
	}
	for _, c := range e.Commands {
		spec := exec.CommandSpec{
			Name:         c.Name,
			Argv:         c.Argv,
			Dir:          c.Dir,
			Description:  c.Description,
			AllowNetwork: c.AllowNetwork,
			ReadOnly:     c.ReadOnly,
			Reversible:   c.Reversible,
		}
		if c.Args != nil {
			spec.Args = &exec.ArgPolicy{Max: c.Args.Max, Match: c.Args.Match, Paths: c.Args.Paths}
		}
		if spec.Timeout, err = parseDuration("exec.commands["+c.Name+"].timeout", c.Timeout); err != nil {
			return nil, err
		}
		cfg.Commands = append(cfg.Commands, spec)
	}
	cfg.Network = e.Network
	if e.Shell != nil {
		sh := &exec.ShellConfig{AllowNetwork: e.Shell.AllowNetwork}
		if sh.Timeout, err = parseDuration("exec.shell.timeout", e.Shell.Timeout); err != nil {
			return nil, err
		}
		for _, r := range e.Shell.Rules {
			pr := exec.PrefixRule{Decision: exec.Decision(r.Decision), Justification: r.Justification, Match: r.Match, NotMatch: r.NotMatch}
			for _, w := range r.Prefix {
				pr.Prefix = append(pr.Prefix, []string(w))
			}
			sh.Rules = append(sh.Rules, pr)
		}
		if pf := e.Shell.Processes; pf != nil && pf.On {
			pc := &exec.ProcessConfig{Max: pf.Max, MaxOutput: pf.MaxOutput, StreamBudget: pf.StreamBudget, AllowDetach: pf.Detach}
			if pc.DefaultYield, err = parseDuration("exec.shell.processes.defaultYield", pf.DefaultYield); err != nil {
				return nil, err
			}
			if pc.MaxYield, err = parseDuration("exec.shell.processes.maxYield", pf.MaxYield); err != nil {
				return nil, err
			}
			sh.Processes = pc
		}
		cfg.Shell = sh
	}
	return cfg, nil
}

func parseDuration(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("workspace file: %s: %w", field, err)
	}
	return d, nil
}
