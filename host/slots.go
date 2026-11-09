package host

import "github.com/panyam/chakra"

// SlotsExtension adds named state slots to a session (#279): a write_slot
// tool for the slots the model may write, and a step stage that shows every
// slot on each model call, so they survive compaction. It restores the
// slots from a resumed run's event log. Build one per session, since it
// holds that session's slots.
type SlotsExtension struct {
	BaseExtension
	slots    *agent.Slots
	writable []string
}

// NewSlotsExtension serves slots, and offers write_slot over the writable
// ones. Slots that another extension writes, such as the plan, stay out of
// writable.
func NewSlotsExtension(slots *agent.Slots, writable ...string) *SlotsExtension {
	return &SlotsExtension{slots: slots, writable: writable}
}

// Name is "slots".
func (e *SlotsExtension) Name() string { return "slots" }

// Tools is write_slot over the writable slots, or nothing when none are.
func (e *SlotsExtension) Tools() (agent.ToolSource, error) {
	if len(e.writable) == 0 {
		return nil, nil
	}
	return e.slots.Tool(e.writable...), nil
}

// StepStages shows the slots on every model call; see agent.Slots.StepStage.
func (e *SlotsExtension) StepStages() []agent.StepStage {
	return []agent.StepStage{e.slots.StepStage()}
}

// RestoreRun sets the slots from run's events, implementing RunRestorer.
func (e *SlotsExtension) RestoreRun(run agent.Run) { e.slots.RestoreSlots(run.Events) }

// Slots is the session's slots.
func (e *SlotsExtension) Slots() *agent.Slots { return e.slots }
