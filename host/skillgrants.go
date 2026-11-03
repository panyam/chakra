package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	agent "github.com/panyam/chakra"
)

// A skill's allowed-tools frontmatter names tools it wants to use without
// asking (#187). Approving the load_skill call approves those grants, and they
// cover matching calls until the turn ends. A skill from the user's own folder
// grants them whenever it loads, since the user wrote it. A project or server
// skill grants them only after a person approved the load through an ask that
// listed them. Otherwise a pull request could add a skill that pre-approves
// shell.

// skillGrant is one allowed-tools entry mapped onto a chakra tool.
type skillGrant struct {
	// tool is the chakra tool the entry covers.
	tool string
	// argv, for the shell tool, is the command the call must start with. Empty
	// with any true matches every command.
	argv []string
	// any lets argv be followed by more arguments.
	any bool
}

// String is how the grant is shown in the ask, e.g. "shell: git tag *".
func (g skillGrant) String() string {
	if len(g.argv) == 0 {
		return g.tool
	}
	s := g.tool + ": " + strings.Join(g.argv, " ")
	if g.any {
		s += " *"
	}
	return s
}

// matches reports whether the grant covers call. A shell grant reads the
// call's argv, and a call it can't read isn't covered.
func (g skillGrant) matches(call agent.ToolCall) bool {
	if call.Name != g.tool {
		return false
	}
	if len(g.argv) == 0 {
		return true
	}
	var args struct {
		Command []string `json:"command"`
	}
	if err := json.Unmarshal(call.Args.Raw(), &args); err != nil || len(args.Command) < len(g.argv) {
		return false
	}
	if !g.any && len(args.Command) != len(g.argv) {
		return false
	}
	for i, w := range g.argv {
		got := args.Command[i]
		if i == 0 {
			got = filepath.Base(got)
		}
		if got != w {
			return false
		}
	}
	return true
}

// claudeTools maps Claude Code's tool names onto chakra's.
var claudeTools = map[string][]string{
	"Bash":      {"shell"},
	"Read":      {"read_file"},
	"Write":     {"write_file"},
	"Edit":      {"edit_file", "apply_patch"},
	"MultiEdit": {"edit_file", "apply_patch"},
	"Glob":      {"list_files"},
	"Grep":      {"search_files"},
}

// chakraToolName is a tool name written chakra's way, such as read_file.
var chakraToolName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// parseSkillGrants maps an allowed-tools value onto chakra tools. Entries
// are separated by commas or spaces outside parentheses. Bash(git tag *) and
// Bash(git tag:*) both grant shell calls whose argv starts "git tag". A bare
// Claude name such as Read or a chakra name such as read_file grants that
// tool. Anything else comes back in ignored, so the ask can show it. That
// includes a path-scoped entry such as Read(src/**), because chakra can't
// scope a file tool by path.
func parseSkillGrants(text string) (grants []skillGrant, ignored []string) {
	for _, entry := range splitAllowedTools(text) {
		name, spec, scoped := strings.Cut(entry, "(")
		if scoped {
			if name != "Bash" || !strings.HasSuffix(spec, ")") {
				ignored = append(ignored, entry)
				continue
			}
			g, ok := bashGrant(strings.TrimSuffix(spec, ")"))
			if !ok {
				ignored = append(ignored, entry)
				continue
			}
			grants = append(grants, g)
			continue
		}
		if tools, ok := claudeTools[name]; ok {
			for _, t := range tools {
				grants = append(grants, skillGrant{tool: t, any: true})
			}
			continue
		}
		if chakraToolName.MatchString(name) {
			grants = append(grants, skillGrant{tool: name, any: true})
			continue
		}
		ignored = append(ignored, entry)
	}
	return grants, ignored
}

// bashGrant reads a Bash(...) spec: words, then an optional "*" or ":*"
// that lets more arguments follow.
func bashGrant(spec string) (skillGrant, bool) {
	spec = strings.TrimSpace(spec)
	g := skillGrant{tool: "shell"}
	switch {
	case spec == "*" || spec == "":
		g.any = true
		return g, true
	case strings.HasSuffix(spec, ":*"):
		spec, g.any = strings.TrimSuffix(spec, ":*"), true
	case strings.HasSuffix(spec, " *"):
		spec, g.any = strings.TrimSuffix(spec, " *"), true
	}
	g.argv = strings.Fields(spec)
	if len(g.argv) == 0 {
		return g, false
	}
	for _, w := range g.argv {
		if strings.ContainsAny(w, "*?") {
			return g, false // only a trailing wildcard is understood
		}
	}
	return g, true
}

// splitAllowedTools splits on commas and spaces that sit outside
// parentheses, so "Bash(git tag *) Read" is two entries.
func splitAllowedTools(text string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, r := range text {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0 && (r == ',' || r == ' ' || r == '\t' || r == '\n'):
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// skillGrants holds the grants approved this session and the ones active in
// this turn.
type skillGrants struct {
	mu sync.Mutex
	// approved is keyed by the load_skill approval key, which covers the
	// skill's exact files. So a person's yes covers this version of the skill,
	// and an edited skill has to be approved again.
	approved map[string]bool
	active   []skillGrant
}

func (s *skillGrants) approve(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.approved == nil {
		s.approved = map[string]bool{}
	}
	s.approved[key] = true
}

func (s *skillGrants) isApproved(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approved[key]
}

func (s *skillGrants) activate(gs []skillGrant) {
	s.mu.Lock()
	s.active = append(s.active, gs...)
	s.mu.Unlock()
}

// endTurn drops the turn's grants. Approvals stay, so loading the same skill
// in a later turn grants again without a second ask.
func (s *skillGrants) endTurn() {
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
}

func (s *skillGrants) covers(call agent.ToolCall) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.active {
		if g.matches(call) {
			return true
		}
	}
	return false
}

// grantCovers is the gate's GrantFunc.
func (a *App) grantCovers(_ context.Context, info agent.ToolCallInfo) bool {
	return a.grants.covers(info.Call)
}

// loadSkillGrants resolves a load_skill call to its skill and that skill's
// grants. ok is false when the call names no known skill or the skill grants
// nothing.
func (a *App) loadSkillGrants(info agent.ToolCallInfo) (match *catalogSkill, grants []skillGrant, ignored []string, ok bool) {
	if info.Call.Name != "load_skill" {
		return nil, nil, nil, false
	}
	var in loadSkillArgs
	if err := info.Call.Args.Bind(&in); err != nil {
		return nil, nil, nil, false
	}
	match, _ = resolveCatalogSkill(a.allCatalogSkills(), strings.TrimSpace(in.Name), strings.TrimSpace(in.Server))
	if match == nil {
		return nil, nil, nil, false
	}
	grants, ignored = parseSkillGrants(allowedTools(match.entry))
	return match, grants, ignored, len(grants) > 0 || len(ignored) > 0
}

// classifySkillLoad makes loading a project or server skill that grants tools
// an ask under every mode except allow, even where load_skill would otherwise
// run as a read-only call. The key keeps the approval bound to the skill's
// files. A skill from the user's own folder is left to the usual rules.
func (a *App) classifySkillLoad(info agent.ToolCallInfo) (agent.CallClass, bool) {
	match, grants, _, ok := a.loadSkillGrants(info)
	if !ok || len(grants) == 0 || (match.local != nil && match.local.Own) {
		return agent.CallClass{}, false
	}
	key, _ := skillApprovalKey(match)
	return agent.CallClass{Disposition: agent.DispositionDestructive, Key: key}, true
}

// renderSkillLoad adds a skill's grants to the load_skill ask, so the person
// sees what saying yes approves.
func (a *App) renderSkillLoad(info agent.ToolCallInfo) (string, bool) {
	_, grants, ignored, ok := a.loadSkillGrants(info)
	if !ok {
		return "", false
	}
	q := approvalPrompt(info)
	if len(grants) > 0 {
		names := make([]string, len(grants))
		for i, g := range grants {
			names[i] = g.String()
		}
		q += " Approving also lets these run without asking for the rest of this turn: " + strings.Join(names, ", ") + "."
	}
	if len(ignored) > 0 {
		q += " Not granted, since chakra has no matching tool: " + strings.Join(ignored, ", ") + "."
	}
	return q, true
}

// noteApprovedSkillLoad records that a person approved loading a skill whose
// ask listed its grants. The load_skill handler then activates them.
func (a *App) noteApprovedSkillLoad(info agent.ToolCallInfo) {
	match, grants, _, ok := a.loadSkillGrants(info)
	if !ok || len(grants) == 0 {
		return
	}
	if key, ok := skillApprovalKey(match); ok {
		a.grants.approve(key)
	}
}

// grantSkill activates a loaded skill's grants for the rest of the turn when
// the skill is the user's own or a person approved this version of it. It
// returns the note for the model, or "".
func (a *App) grantSkill(match *catalogSkill) string {
	grants, _ := parseSkillGrants(allowedTools(match.entry))
	if len(grants) == 0 {
		return ""
	}
	own := match.local != nil && match.local.Own
	if !own {
		key, ok := skillApprovalKey(match)
		if !ok || !a.grants.isApproved(key) {
			return ""
		}
	}
	a.grants.activate(grants)
	names := make([]string, len(grants))
	for i, g := range grants {
		names[i] = g.String()
	}
	return "[granted for the rest of this turn, without asking: " + strings.Join(names, ", ") + "]"
}
