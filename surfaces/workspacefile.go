package surfaces

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

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
