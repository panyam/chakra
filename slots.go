package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// SlotMode says what a write does to a slot.
type SlotMode string

const (
	// SlotReplace makes each write the slot's whole value, as a plan is.
	SlotReplace SlotMode = "replace"
	// SlotAppend adds each write as a new line, as a list of facts or of
	// failed attempts grows. Past MaxBytes the oldest lines go.
	SlotAppend SlotMode = "append"
)

// SlotSpec declares one slot.
type SlotSpec struct {
	// Name identifies the slot, such as "plan" or "facts".
	Name string `json:"name"`
	// Mode is SlotReplace or SlotAppend. Empty means SlotReplace.
	Mode SlotMode `json:"mode,omitempty"`
	// MaxBytes caps the slot's value. Zero means 2000. A replace slot
	// refuses a longer write, and an append slot drops its oldest lines.
	MaxBytes int `json:"maxBytes,omitempty"`
	// Description tells the model what belongs in the slot. The write tool
	// shows it.
	Description string `json:"description,omitempty"`
}

// SlotUpdate is a slot's whole value after a write. It travels on EventSlot,
// so a run's event log holds every slot's latest value, and RestoreSlots
// rebuilds them from it.
type SlotUpdate struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SlotToolName is the tool NewSlots' Tool offers the model.
const SlotToolName = "write_slot"

// ErrUnknownSlot is wrapped by a write to a slot that wasn't declared.
var ErrUnknownSlot = errors.New("agent: unknown slot")

// Slots holds named state for a run, such as a plan, the facts a model has
// learned, or the attempts that failed (#279). Imagine a long turn in which
// the model learns early that "no node has kind module". A compaction then
// summarizes every message but the last few, and the fact may not survive.
// A fact written to a slot does, because the slots never enter the history.
// StepStage shows them on every model call instead.
//
// A tool or a stage writes a slot through Write. Every write emits EventSlot
// with the slot's new value, so a RunStore's event log keeps them, and
// RestoreSlots brings them back for a resumed run. One Slots serves one
// session. It is safe for concurrent use.
type Slots struct {
	mu     sync.Mutex
	specs  []SlotSpec
	vals   map[string]string
	header string
	asUser bool
}

// defaultSlotsHeader is SlotsConfig.Header when it is empty.
const defaultSlotsHeader = "Your notes for this task, kept across compaction:"

// SlotsConfig configures NewSlotsWith.
type SlotsConfig struct {
	// Specs declares the slots, as NewSlots takes them.
	Specs []SlotSpec
	// Header is the line Render opens with. Empty means "Your notes for this
	// task, kept across compaction:". A host that writes the slots itself,
	// rather than the model, says so here (#319).
	Header string
	// AsUser makes StepStage send the slots as a RoleUser message instead of
	// a RoleSystem one. Some chat templates reject a system message that
	// isn't the first, as with RetrievalConfig.AsUser.
	AsUser bool
}

// NewSlots declares the slots, in the order StepStage shows them. A name
// must be unique and non-empty, and a mode must be SlotReplace, SlotAppend
// or empty. It is NewSlotsWith with only Specs set.
func NewSlots(specs ...SlotSpec) (*Slots, error) {
	return NewSlotsWith(SlotsConfig{Specs: specs})
}

// NewSlotsWith declares the slots as NewSlots does, and sets how Render and
// StepStage show them.
func NewSlotsWith(cfg SlotsConfig) (*Slots, error) {
	s := &Slots{vals: map[string]string{}, header: cfg.Header, asUser: cfg.AsUser}
	if s.header == "" {
		s.header = defaultSlotsHeader
	}
	for _, sp := range cfg.Specs {
		if sp.Name == "" {
			return nil, errors.New("agent: a slot needs a name")
		}
		if slices.ContainsFunc(s.specs, func(o SlotSpec) bool { return o.Name == sp.Name }) {
			return nil, fmt.Errorf("agent: slot %q declared twice", sp.Name)
		}
		switch sp.Mode {
		case "":
			sp.Mode = SlotReplace
		case SlotReplace, SlotAppend:
		default:
			return nil, fmt.Errorf("agent: slot %q has unknown mode %q", sp.Name, sp.Mode)
		}
		if sp.MaxBytes <= 0 {
			sp.MaxBytes = 2000
		}
		s.specs = append(s.specs, sp)
	}
	return s, nil
}

// Specs are the declared slots, with defaults filled in.
func (s *Slots) Specs() []SlotSpec { return slices.Clone(s.specs) }

// Has reports whether name was declared.
func (s *Slots) Has(name string) bool {
	_, ok := s.spec(name)
	return ok
}

func (s *Slots) spec(name string) (SlotSpec, bool) {
	for _, sp := range s.specs {
		if sp.Name == name {
			return sp, true
		}
	}
	return SlotSpec{}, false
}

// Get is the slot's value, empty when unset or undeclared.
func (s *Slots) Get(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vals[name]
}

// Write changes a slot and returns its new value. A replace slot takes text
// as its value, and empty text clears it. An append slot adds text as a new
// line, and drops its oldest lines past MaxBytes. It refuses text that alone
// is over the cap, as a replace slot does, with an error the model can act
// on. When ctx belongs to a tool call, the write emits EventSlot for that
// call's step. A write from outside a turn emits nothing.
func (s *Slots) Write(ctx context.Context, name, text string) (string, error) {
	sp, ok := s.spec(name)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownSlot, name)
	}
	text = strings.TrimSpace(text)
	if len(text) > sp.MaxBytes {
		return "", fmt.Errorf("slot %q holds %d bytes, and this is %d; write it shorter", name, sp.MaxBytes, len(text))
	}
	s.mu.Lock()
	val := text
	if sp.Mode == SlotAppend && text != "" {
		lines := strings.Split(s.vals[name], "\n")
		if s.vals[name] == "" {
			lines = nil
		}
		lines = append(lines, text)
		for len(strings.Join(lines, "\n")) > sp.MaxBytes {
			lines = lines[1:]
		}
		val = strings.Join(lines, "\n")
	} else if sp.Mode == SlotAppend {
		val = s.vals[name]
	}
	s.vals[name] = val
	s.mu.Unlock()
	if emit := toolEmitFrom(ctx); emit != nil {
		emit(Event{Kind: EventSlot, Slot: &SlotUpdate{Name: name, Value: val}})
	}
	return val, nil
}

// RestoreSlots sets every declared slot from the last EventSlot for it in
// events, as a RunStore's event log has them, and clears the rest. Updates
// for slots that aren't declared are ignored.
func (s *Slots) RestoreSlots(events []Event) {
	vals := map[string]string{}
	for _, e := range events {
		if e.Kind == EventSlot && e.Slot != nil && s.Has(e.Slot.Name) {
			vals[e.Slot.Name] = e.Slot.Value
		}
	}
	s.mu.Lock()
	s.vals = vals
	s.mu.Unlock()
}

// Render is the slots as the model sees them, under SlotsConfig.Header, or
// empty when every slot is empty.
func (s *Slots) Render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, sp := range s.specs {
		v := s.vals[sp.Name]
		if v == "" {
			continue
		}
		fmt.Fprintf(&b, "\n\n[%s]\n%s", sp.Name, v)
	}
	if b.Len() == 0 {
		return ""
	}
	return s.header + b.String()
}

// StepStage shows the slots before every model call, as one system message
// after the history, or a user message with SlotsConfig.AsUser. The message is for that call only, so it never enters
// the turn's record, and a compactor never sees it. It adds nothing while
// every slot is empty.
//
// It goes after the history rather than before it so a slot write doesn't
// change the prompt's prefix, and a provider's prompt cache still covers the
// history.
func (s *Slots) StepStage() StepStage {
	return StepStage{
		Name:  "agent.slots",
		Scope: ScopeRequest,
		Run: func(_ context.Context, _ StepInfo, view StepView) (StepView, error) {
			text := s.Render()
			if text == "" {
				return view, nil
			}
			role := RoleSystem
			if s.asUser {
				role = RoleUser
			}
			view.Messages = append(slices.Clip(view.Messages), Message{Role: role, Text: text})
			return view, nil
		},
	}
}

// Tool is write_slot over the named slots, for the model to write them. Names
// that weren't declared are left out. With none left, it lists no tool.
func (s *Slots) Tool(names ...string) ToolSource {
	var specs []SlotSpec
	for _, n := range names {
		if sp, ok := s.spec(n); ok {
			specs = append(specs, sp)
		}
	}
	return &slotTool{slots: s, specs: specs}
}

type slotTool struct {
	slots *Slots
	specs []SlotSpec
}

func (t *slotTool) Tools(context.Context) ([]core.ToolDef, error) {
	if len(t.specs) == 0 {
		return nil, nil
	}
	var names []string
	var desc strings.Builder
	desc.WriteString("Save something you'll need later in this task. Every slot is shown to you on every call, even after older messages are summarized away. Keep each entry short.")
	for _, sp := range t.specs {
		names = append(names, sp.Name)
		how := "each write replaces it"
		if sp.Mode == SlotAppend {
			how = "each write adds a line, and the oldest lines go when it's full"
		}
		fmt.Fprintf(&desc, "\n- %s (%s; up to %d bytes)", sp.Name, how, sp.MaxBytes)
		if sp.Description != "" {
			desc.WriteString(": " + sp.Description)
		}
	}
	return []core.ToolDef{{
		Name:        SlotToolName,
		Title:       "Write a slot",
		Annotations: map[string]any{"readOnlyHint": true},
		Description: desc.String(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slot": map[string]any{"type": "string", "enum": names},
				"text": map[string]any{"type": "string", "description": "What to write."},
			},
			"required":             []string{"slot", "text"},
			"additionalProperties": false,
		},
	}}, nil
}

func (t *slotTool) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if name != SlotToolName {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	slot, _ := args["slot"].(string)
	text, _ := args["text"].(string)
	if !slices.ContainsFunc(t.specs, func(sp SlotSpec) bool { return sp.Name == slot }) {
		return slotResult(fmt.Sprintf("no slot %q to write; the slots are %s", slot, t.names()), true), nil
	}
	val, err := t.slots.Write(ctx, slot, text)
	if err != nil {
		return slotResult("slot not written: "+err.Error(), true), nil
	}
	return slotResult(fmt.Sprintf("%s: %d bytes", slot, len(val)), false), nil
}

func (t *slotTool) names() string {
	var out []string
	for _, sp := range t.specs {
		out = append(out, sp.Name)
	}
	return strings.Join(out, ", ")
}

func slotResult(text string, isErr bool) *core.ToolResult {
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: text}}, IsError: isErr}
}
