package web

import (
	"encoding/json"

	"github.com/panyam/chakra/host"
	webv1 "github.com/panyam/chakra/surfaces/web/gen/go/chakra/web/v1"
)

// eventToFrame projects a HostEvent onto its wire Frame. The frame's kind is
// the discriminator, and its payload is the event's own JSON from
// json.Marshal(HostEvent). The frame is a 1:1 projection of the A2
// wire-serializable event, never a per-kind proto schema. CONSTRAINTS.md A2
// forbids a translation layer, and the web wire projection must be a 1:1
// mapping.
//
// A few HostEvent kinds still hold live pointers, and json.Marshal can reject
// them (issue 994). The Failover inside a command result contains funcs, and
// TaskStatus and Task are the task events. On a marshal failure the frame gets
// a minimal {kind} payload, so one un-serializable event never stalls the
// stream. The client still learns from the kind that the event happened.
func eventToFrame(ev host.HostEvent) *webv1.Frame {
	payload := marshalOrMinimal(ev, ev.Kind)
	return &webv1.Frame{Kind: string(ev.Kind), Payload: payload}
}

// marshalOrMinimal returns json.Marshal(v), or a minimal {kind, _note} document
// when v carries a non-serializable field (issue 994). It never returns an
// error: the stream and command responses must degrade, not fail, on the long
// tail of not-yet-snapshotted payloads.
func marshalOrMinimal(v any, kind host.HostEventKind) []byte {
	if b, err := json.Marshal(v); err == nil {
		return b
	}
	b, _ := json.Marshal(struct {
		Kind host.HostEventKind `json:"Kind"`
		Note string             `json:"_note"`
	}{Kind: kind, Note: "issue 994: non-serializable payload omitted"})
	return b
}
