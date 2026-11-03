package surfaces

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/panyam/chakra/ext/agentsmd"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/ext/lsp"
)

// WorkspaceFile is the JSON form of the workspace settings a flag cannot
// carry: the exec allowlist, the language servers, and the repo map.
//
// Roots are deliberately absent. They come from --workspace, which keeps the
// rule WorkspaceConfig.Roots documents: nothing picks the confinement on the
// user's behalf, and a checked-in file naming directories would be exactly
// that. The exec commands run in, and write to, the same roots as the file
// tools.
type WorkspaceFile struct {
	// Exec enables the allowlisted command tools. Absent means none.
	Exec *ExecFile `json:"exec,omitempty"`

	// LanguageServers enables LSP-in-loop; see WorkspaceConfig.LanguageServers.
	LanguageServers []LanguageServerFile `json:"languageServers,omitempty"`

	// RepoMap adds the ranked repository map; see WorkspaceConfig.RepoMap.
	RepoMap bool `json:"repoMap,omitempty"`

	// Instructions picks how AGENTS.md and similar files load: a "preset"
	// ("chakra", "codex" or "claude"), any agentsmd.Profile field to change on
	// top of it, or "off": true. Absent means the chakra preset.
	//
	//	"instructions": {"preset": "codex", "maxBytes": 65536}
	Instructions json.RawMessage `json:"instructions,omitempty"`
}

// instructionsFile is the decoded form of WorkspaceFile.Instructions.
type instructionsFile struct {
	Preset string `json:"preset"`
	Off    bool   `json:"off"`
	agentsmd.Profile
}

// InstructionsProfile decodes an "instructions" object: the named preset
// (agentsmd.DefaultPreset when none is named) with the object's other
// fields laid over it. off reports "off": true. Unknown fields are an error,
// like everywhere else in the file.
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
	// Sandbox is "platform" (or empty) for the platform's backend, which fails
	// construction where none exists, or "unconfined" to run with no sandbox.
	// The opt-out is a word the operator has to write, matching exec.Unconfined.
	Sandbox string `json:"sandbox,omitempty"`

	// Timeout is a Go duration string ("2m"). Empty means exec.DefaultTimeout.
	Timeout string `json:"timeout,omitempty"`

	// MaxOutput caps the bytes held for each stream a command prints. Zero
	// means exec.DefaultMaxOutput.
	MaxOutput int `json:"maxOutput,omitempty"`

	// InlineOutput caps the bytes of each stream a result carries; negative
	// means all of it. Zero means exec.DefaultInlineOutput, or all of it when
	// the host offloads (see WorkspaceConfig.Offloading).
	InlineOutput int `json:"inlineOutput,omitempty"`

	// WritePaths and DenyRead keep exec.Config's nil-versus-empty meaning:
	// absent selects the package default, [] selects none.
	WritePaths []string `json:"writePaths"`
	DenyRead   []string `json:"denyRead"`

	// Commands is the allowlist, one tool per entry.
	Commands []CommandFile `json:"commands"`

	// Shell turns on the general "shell" tool with these prefix rules. See
	// exec.ShellConfig.
	Shell *ShellFile `json:"shell,omitempty"`
}

// ShellFile is exec.ShellConfig as JSON.
type ShellFile struct {
	Rules        []ShellRuleFile `json:"rules"`
	Timeout      string          `json:"timeout,omitempty"`
	AllowNetwork bool            `json:"allowNetwork,omitempty"`
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
// otherwise decode to its zero value, and for this file the zero values are
// the ones that change what runs and what gets asked about.
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

// Apply copies the file's settings onto cfg, leaving the fields the flags own
// (Roots, Exclude, the checkpoint settings) untouched.
func (f *WorkspaceFile) Apply(cfg *WorkspaceConfig) error {
	if f == nil {
		return nil
	}
	cfg.RepoMap = f.RepoMap
	if len(f.Instructions) > 0 {
		p, off, err := InstructionsProfile(f.Instructions)
		if err != nil {
			return err
		}
		cfg.Instructions, cfg.NoInstructions = p, off
	}
	for _, ls := range f.LanguageServers {
		cfg.LanguageServers = append(cfg.LanguageServers, lsp.ServerSpec{
			Command:    ls.Command,
			Extensions: ls.Extensions,
			LanguageID: ls.LanguageID,
		})
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
