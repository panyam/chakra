package files

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/mcpkit/core"
)

// Extension contributes the file tools and the prompt that tells a model how
// to drive them.
//
// The two travel together because neither works alone. The tools refuse an
// edit whose anchor is not unique, and a model that has not been told that
// will keep sending single-word anchors and keep being refused. Registering
// the tools without the prompt would ship a mechanism whose contract is
// discoverable only by failing.
type Extension struct {
	host.BaseExtension
	src *Source
}

// New builds the extension. Root confines every path the tools will touch and
// is required; see Config.
func New(cfg Config) (*Extension, error) {
	src, err := NewSource(cfg)
	if err != nil {
		return nil, err
	}
	return &Extension{src: src}, nil
}

// Name identifies the extension, and is the source id its tools register
// under.
func (e *Extension) Name() string { return "files" }

// Tools returns the workspace tool set: read_file, edit_file, write_file,
// delete_file, list_files, and search_files.
func (e *Extension) Tools() (agent.ToolSource, error) { return e.src, nil }

// WritePaths reports the file a writing call is about to touch, as the
// absolute path the tool itself will write. It is the Paths function a
// checkpoint or language-server WriteSpec should use for these tools:
//
//	checkpoint.WriteSpec{Tool: "edit_file", Paths: fx.WritePaths}
//
// A relative path resolves against the workspace roots exactly as the tools
// resolve it, never against the process's working directory, so a snapshot
// covers the file the write lands on wherever the process was started. A
// path the tools would refuse (empty, or outside every root) yields nothing,
// since that call writes nothing.
func (e *Extension) WritePaths(args map[string]any) []string {
	p, ok := args["path"].(string)
	if !ok {
		return nil
	}
	r, rel, err := e.src.resolve(p)
	if err != nil {
		return nil
	}
	return []string{r.abs(rel)}
}

// PromptSections states the edit discipline the tools enforce.
func (e *Extension) PromptSections() []host.PromptSection {
	return []host.PromptSection{host.PromptSectionFunc(func(context.Context) string {
		return `## Editing files

Read a file before editing it, and pass the hash read_file returned as expect_hash.
An edit whose expect_hash no longer matches is refused, because the file changed
after you read it and your edit was written against content that is gone. Read it
again and redo the edit against what is there now.

Each edit replaces an exact snippet. The text in ` + "`old`" + ` must appear exactly once in
the file and is matched byte for byte, including indentation. If it appears more
than once the edit is refused rather than applied to a guess: add surrounding
lines until it is unique. All edits in one call apply together or none do.

For a large file, read the part you need with start_line and end_line instead of
the whole thing; read_file reports how many lines a file has. The hash is always
the whole file's, so a partial read is enough to edit with.`
	})}
}

// ApprovalRenderers shows an edit as the change it makes, rather than as its
// arguments.
//
// The default host prompt renders a call's JSON trimmed to 200 characters. For
// most tools that is right: the arguments are an opaque payload and the
// question is really "do you trust this tool with roughly this". For an edit
// the arguments ARE the change, and 200 characters of JSON truncates the one
// thing the user is being asked to judge, so they end up approving a diff they
// could not read.
func (e *Extension) ApprovalRenderers() []host.ApprovalRenderer {
	return []host.ApprovalRenderer{renderEditApproval}
}

// ApprovalChoosers lets the user approve an edit one hunk at a time.
func (e *Extension) ApprovalChoosers() []host.ApprovalChooser {
	return []host.ApprovalChooser{chooseHunks}
}

// chooseHunks claims an edit_file call with two or more hunks and asks about
// each one in a single form, with every hunk ticked to start with. The call
// that runs carries only the hunks left ticked. Leaving out hunks is all
// partial application needs, because Edit.Apply resolves every anchor against
// the original content, so the kept hunks land exactly where they would have
// with the others present, and expect_hash still describes the file.
//
// A one-hunk edit is left to the plain ask, where yes or no is the whole
// question. So are calls whose arguments do not parse, for the same reason
// renderEditApproval declines them.
func chooseHunks(ctx context.Context, info agent.ToolCallInfo, present agent.ElicitationUI) (agent.Choice, error) {
	if info.Call.Name != "edit_file" {
		return agent.Choice{}, nil
	}
	args := map[string]any{}
	if err := json.Unmarshal(info.Call.Args.Raw(), &args); err != nil {
		return agent.Choice{}, nil
	}
	path, _ := args["path"].(string)
	items, _ := args["edits"].([]any)
	hunks, err := parseHunks(args["edits"])
	if path == "" || err != nil || len(hunks) < 2 {
		return agent.Choice{}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Apply %d changes to %s? Untick any you don't want.\n", len(hunks), path)
	props := map[string]any{}
	keys := make([]string, len(hunks))
	width := len(strconv.Itoa(len(hunks)))
	for i, h := range hunks {
		// Zero-padded so a surface that orders fields by name, as the
		// terminal does, still shows change 10 after change 9.
		keys[i] = fmt.Sprintf("change_%0*d", width, i+1)
		props[keys[i]] = map[string]any{"type": "boolean", "title": fmt.Sprintf("Apply change %d", i+1), "default": true}
		fmt.Fprintf(&b, "\nChange %d:\n", i+1)
		writeDiff(&b, h.Old, h.New)
	}
	schema, err := json.Marshal(map[string]any{"type": "object", "properties": props})
	if err != nil {
		return agent.Choice{}, err
	}
	res, err := present(ctx, core.ElicitationRequest{Message: b.String(), RequestedSchema: schema})
	if err != nil {
		return agent.Choice{}, err
	}
	if res.Action != "accept" {
		return agent.Choice{Claimed: true}, nil
	}

	var kept []any
	var declined []string
	for i, k := range keys {
		// Absent means approved: a surface that shows only accept and decline
		// sends no fields, and its user accepted the whole edit.
		if v, ok := res.Content[k].(bool); ok && !v {
			declined = append(declined, strconv.Itoa(i+1))
			continue
		}
		kept = append(kept, items[i])
	}
	switch {
	case len(declined) == 0:
		return agent.Choice{Claimed: true, Approved: true}, nil
	case len(kept) == 0:
		return agent.Choice{Claimed: true, Note: fmt.Sprintf("the user declined all %d changes to %s", len(hunks), path)}, nil
	}
	args["edits"] = kept
	narrowed, err := core.MarshalRawJSON(args)
	if err != nil {
		return agent.Choice{}, err
	}
	return agent.Choice{
		Claimed:  true,
		Approved: true,
		Args:     narrowed,
		Note: fmt.Sprintf("The user declined change %s of the %d you proposed to %s, so only the other %d were sent. Tell the user which changes were left out.",
			strings.Join(declined, ", "), len(hunks), path, len(kept)),
	}, nil
}

// renderEditApproval claims edit_file, write_file and delete_file, and
// declines everything else so the default still covers tools this package
// does not own.
func renderEditApproval(_ context.Context, info agent.ToolCallInfo) (string, bool) {
	args := map[string]any{}
	if raw := info.Call.Args.Raw(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			// Unparseable arguments are exactly when the generic rendering is
			// the honest one: it shows the raw text rather than a summary
			// built from a guess.
			return "", false
		}
	}
	path, _ := args["path"].(string)
	if path == "" {
		return "", false
	}

	switch info.Call.Name {
	case "edit_file":
		hunks, err := parseHunks(args["edits"])
		if err != nil || len(hunks) == 0 {
			return "", false
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Apply %d change(s) to %s?\n", len(hunks), path)
		for _, h := range hunks {
			b.WriteString("\n")
			writeDiff(&b, h.Old, h.New)
		}
		return b.String(), true

	case "write_file":
		content, ok := args["content"].(string)
		if !ok {
			return "", false
		}
		verb := "Create"
		if _, replacing := args["expect_hash"].(string); replacing {
			verb = "Replace"
		}
		return fmt.Sprintf("%s %s (%d bytes, %d lines)?\n\n%s",
			verb, path, len(content), strings.Count(content, "\n")+1, indent(preview(content))), true

	case "delete_file":
		// No diff to show, since the content isn't in the arguments.
		return fmt.Sprintf("Delete %s?\n\nThe file is removed from the workspace.", path), true
	}
	return "", false
}

// writeDiff renders one hunk as removed and added lines.
func writeDiff(b *strings.Builder, old, nw string) {
	for _, line := range strings.Split(strings.TrimRight(old, "\n"), "\n") {
		fmt.Fprintf(b, "  - %s\n", line)
	}
	if nw == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(nw, "\n"), "\n") {
		fmt.Fprintf(b, "  + %s\n", line)
	}
}

// preview caps a whole-file write at a readable length. This is the renderer
// deciding its own truncation, which is the point of the seam: the cap that
// suits an opaque payload is not the cap that suits a diff.
func preview(s string) string {
	const maxLines = 40
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n… %d more line(s)", len(lines)-maxLines)
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

var _ host.Extension = (*Extension)(nil)
