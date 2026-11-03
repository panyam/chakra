package host

import (
	"context"
	"time"

	"github.com/panyam/chakra"
)

// Built-in source ids whose output the host vouches for. These are the only
// sources that derive ProvenanceOperator, and the list is closed on purpose.
//
// Operator means the process computed the output rather than relaying it, and
// the host can only honestly claim that for its own in-repo, non-relaying
// tools. An extension is arbitrary code that may shell out or fetch, so
// derivation never vouches for it, and config has to say so explicitly.
// Getting this backwards silently disables the mitigation. Other mistakes
// here only make it noisy.
const (
	sourceHost          = "host"
	sourceRunnerControl = "runner-control"
)

// Prefixes the host uses when adding sources that are themselves agents. A
// sub-agent's output relays whatever it read, so it is marked. When a Mark
// renders it, it is still worth telling apart from open-web content.
var agentSourcePrefixes = []string{"subagent:", "fanout:", "serveragents:"}

// ownerLookupTimeout bounds the source lookup a classification does. It is
// short because the index is memoized, and the common path does no I/O at
// all. The timeout only fires when a re-list has to reach a server that is
// not answering. Marking is the right answer in that case anyway.
const ownerLookupTimeout = 2 * time.Second

// recordOrigin notes what a source id is, for provenance derivation. Called
// where the source is added, since a server id and an extension name are
// indistinguishable strings afterwards.
func (a *App) recordOrigin(id string, p agent.Provenance) {
	if a.toolOrigins == nil {
		a.toolOrigins = map[string]agent.Provenance{}
	}
	a.toolOrigins[id] = p
}

// derivedProvenance maps a source id to a label, or "" when the host has no
// honest claim to make.
//
// The recorded map wins, then the built-in allowlist, then the agent prefixes.
// Anything else returns "", such as an extension or a source added by a path
// that forgot to record it. The caller then defaults it to world. Resolving
// an unknown source to marked is the safe direction. Otherwise a source
// nobody classified would reach the model unfenced.
func (a *App) derivedProvenance(id string) agent.Provenance {
	if p, ok := a.toolOrigins[id]; ok {
		return p
	}
	switch id {
	case sourceHost, sourceRunnerControl:
		return agent.ProvenanceOperator
	}
	for _, prefix := range agentSourcePrefixes {
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			return agent.ProvenanceAgent
		}
	}
	return ""
}

// classifyTool is the spotlight classifier: explicit config first, then
// derivation from the source the call resolves to, then world.
//
// It resolves through MultiSource.OwnerOf rather than matching the tool name
// itself. Collision handling can qualify a name into the "sourceID_name"
// form, and the model may call that form. Re-deriving that mapping here would
// copy the resolution logic, and the copy would drift from the one Call uses.
//
// It is evaluated per call rather than snapshotted at construction. So it
// classifies a server connected later, or an extension registered after the
// middleware was built, instead of silently treating it as unknown.
//
// ownerLookupTimeout bounds the lookup. agent.SpotlightConfig's Classify
// takes no context, and a miss against the memoized index makes MultiSource
// re-list every source over the network. An unreachable server must not be
// able to stall tool dispatch. A timeout resolves to world, and world marks.
func (a *App) classifyTool(labels map[string]agent.Provenance) func(agent.ToolCallInfo) agent.Provenance {
	return func(info agent.ToolCallInfo) agent.Provenance {
		if p, ok := labels[info.Call.Name]; ok {
			return p
		}
		if a.sources == nil {
			return agent.ProvenanceWorld
		}
		args := map[string]any{}
		if info.Call.Args.Len() > 0 {
			_ = info.Call.Args.Bind(&args)
		}
		ctx, cancel := context.WithTimeout(context.Background(), ownerLookupTimeout)
		defer cancel()
		id, found := a.sources.OwnerOf(ctx, info.Call.Name, args)
		if !found {
			return agent.ProvenanceWorld
		}
		if p := a.derivedProvenance(id); p != "" {
			return p
		}
		return agent.ProvenanceWorld
	}
}
