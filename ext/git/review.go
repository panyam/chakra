package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// ReviewConfig turns on review (#152): a `review` tool and a /review command
// that run a reviewer sub-agent over a diff and return structured findings.
type ReviewConfig struct {
	// Rubric is the reviewer's instructions, DefaultRubric when empty.
	Rubric string
	// MaxSteps caps the reviewer's model calls, 12 when 0.
	MaxSteps int
}

// DefaultRubric is what the reviewer is told when ReviewConfig.Rubric is
// empty.
const DefaultRubric = `You review a code change for problems its author would want to fix before merging.

Report bugs the change introduces or leaves in the lines it touches: wrong behaviour, missed cases, broken error handling, races, security problems, and tests that don't test what they claim. Don't report style, naming or formatting unless it hides a bug, and don't report problems in code the change doesn't touch.

The diff you're given is the whole change. Read the files it touches with your tools when you need the surrounding code, and only then. You can't change anything.

For each finding give a short title, a body that says what goes wrong and when, the file and the line range in the new version, a priority (0 must fix, 1 should fix, 2 worth fixing, 3 minor) and your confidence from 0 to 1. Leave findings empty when you find nothing worth fixing. Then give a verdict, "correct" or "needs changes", and a one or two sentence summary.`

// findingsSchema is the reviewer's ResponseSchema, the shape of a Review.
const findingsSchema = `{
  "type": "object",
  "properties": {
    "findings": {"type": "array", "items": {
      "type": "object",
      "properties": {
        "title": {"type": "string"},
        "body": {"type": "string"},
        "file": {"type": "string"},
        "line_start": {"type": "integer"},
        "line_end": {"type": "integer"},
        "priority": {"type": "integer", "minimum": 0, "maximum": 3},
        "confidence": {"type": "number", "minimum": 0, "maximum": 1}
      },
      "required": ["title", "body", "file", "line_start", "line_end", "priority", "confidence"],
      "additionalProperties": false
    }},
    "verdict": {"type": "string", "enum": ["correct", "needs changes"]},
    "summary": {"type": "string"}
  },
  "required": ["findings", "verdict", "summary"],
  "additionalProperties": false
}`

// Review is a reviewer's answer.
type Review struct {
	Findings []Finding `json:"findings"`
	// Verdict is "correct" or "needs changes".
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

// Finding is one problem a reviewer found. Lines are in the change's new
// version. Priority runs from 0 (must fix) to 3 (minor), and Confidence from
// 0 to 1.
type Finding struct {
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	File       string  `json:"file"`
	LineStart  int     `json:"line_start"`
	LineEnd    int     `json:"line_end"`
	Priority   int     `json:"priority"`
	Confidence float64 `json:"confidence"`
}

// ErrNothingToReview is returned when the target has no changes, before any
// model is asked.
var ErrNothingToReview = errors.New("git: nothing to review: the target has no changes")

// reviewer runs the review sub-agent. Its host is set by UseSubAgents.
type reviewer struct {
	src  *Source
	cfg  ReviewConfig
	host host.SubAgentHost
}

// review diffs t and hands the diff to a fresh reviewer agent with read-only
// tools. It returns the reviewer's JSON as given, and the parsed Review.
func (r *reviewer) review(ctx context.Context, t DiffTarget, instructions string) (string, *Review, error) {
	if r.host.Provider == nil {
		return "", nil, errors.New("git: review needs the host's provider, and this host lent none")
	}
	diff, err := r.src.Diff(ctx, t)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(diff) == "(no changes)" {
		return "", nil, ErrNothingToReview
	}
	rubric := r.cfg.Rubric
	if rubric == "" {
		rubric = DefaultRubric
	}
	steps := r.cfg.MaxSteps
	if steps <= 0 {
		steps = 12
	}
	cfg := agent.RunnerConfig{
		Provider:       r.host.Provider,
		Instructions:   rubric,
		MaxSteps:       steps,
		ResponseSchema: core.NewRawJSON([]byte(findingsSchema)),
	}
	if r.host.ReadOnlyTools != nil {
		cfg.Tools = agent.NewFilterSource(r.host.ReadOnlyTools, func(d core.ToolDef) bool { return d.Name != "review" })
	}
	runner, err := agent.NewRunner(cfg)
	if err != nil {
		return "", nil, err
	}
	child, err := agent.NewAgentSource(agent.AgentSourceConfig{
		Name:        "review",
		Description: "Review a change.",
		Runner:      runner,
		OnEvent:     r.host.OnEvent,
	})
	if err != nil {
		return "", nil, err
	}
	res, err := child.Call(ctx, "review", map[string]any{"task": reviewTask(t, instructions, r.src.git.capped(diff))})
	if err != nil {
		return "", nil, err
	}
	text := ""
	if len(res.Content) > 0 {
		text = res.Content[0].Text
	}
	if res.IsError {
		return "", nil, fmt.Errorf("git: the reviewer failed: %s", text)
	}
	var rv Review
	if err := json.Unmarshal([]byte(text), &rv); err != nil {
		return "", nil, fmt.Errorf("git: the reviewer's answer isn't a review: %w", err)
	}
	return text, &rv, nil
}

// reviewTask is the reviewer's user turn: what is being reviewed, the
// caller's own instructions, then the diff.
func reviewTask(t DiffTarget, instructions, diff string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review %s.\n", describeTarget(t))
	if s := strings.TrimSpace(instructions); s != "" {
		fmt.Fprintf(&b, "\nAlso: %s\n", s)
	}
	fmt.Fprintf(&b, "\n<diff>\n%s</diff>\n", diff)
	return b.String()
}

func describeTarget(t DiffTarget) string {
	switch {
	case t.Commit != "":
		return "commit " + t.Commit
	case t.Base != "":
		return "the changes on this branch since it forked from " + t.Base
	}
	return "the uncommitted changes in the workspace"
}

func reviewDef() core.ToolDef {
	return core.ToolDef{
		Name:        "review",
		Title:       "Review a change",
		Annotations: readOnly(),
		Description: "Have a separate reviewer read a change and report problems: the uncommitted changes by default, a branch's changes since base, or one commit. It sees only the diff and read-only tools, not this conversation. Returns JSON findings (title, body, file, line range, priority 0-3, confidence) and a verdict.",
		InputSchema: obj(map[string]any{
			"base":         str("Review everything since this branch forked from base, e.g. main."),
			"commit":       str("Review one commit, e.g. HEAD."),
			"instructions": str("Anything the reviewer should look at in particular."),
		}),
	}
}

func (r *reviewer) call(ctx context.Context, args map[string]any) *core.ToolResult {
	base, _ := args["base"].(string)
	commit, _ := args["commit"].(string)
	instr, _ := args["instructions"].(string)
	text, _, err := r.review(ctx, DiffTarget{Base: base, Commit: commit}, instr)
	if errors.Is(err, ErrNothingToReview) {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: err.Error()}}}
	}
	if err != nil {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	var structured map[string]any
	_ = json.Unmarshal([]byte(text), &structured)
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: text}}, StructuredContent: structured}
}

// parseReviewArgs reads /review's arguments: nothing for the uncommitted
// changes, "base REF" or "commit REF", then optionally "-- instructions".
func parseReviewArgs(args string) (DiffTarget, string, error) {
	target, instr, _ := strings.Cut(args, "--")
	f := strings.Fields(target)
	var t DiffTarget
	switch {
	case len(f) == 0:
	case len(f) == 2 && f[0] == "base":
		t.Base = f[1]
	case len(f) == 2 && f[0] == "commit":
		t.Commit = f[1]
	default:
		return t, "", fmt.Errorf("usage: /review [base REF | commit REF] [-- instructions]")
	}
	return t, strings.TrimSpace(instr), nil
}

// render is a Review for a person: the verdict and summary, then each finding
// by priority as written.
func (rv *Review) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "review: %s. %s\n", rv.Verdict, rv.Summary)
	if len(rv.Findings) == 0 {
		b.WriteString("no findings\n")
	}
	for _, f := range rv.Findings {
		fmt.Fprintf(&b, "\n[P%d] %s (%s:%d-%d, confidence %.2f)\n", f.Priority, f.Title, f.File, f.LineStart, f.LineEnd, f.Confidence)
		for _, l := range strings.Split(strings.TrimSpace(f.Body), "\n") {
			b.WriteString("    " + l + "\n")
		}
	}
	return b.String()
}
