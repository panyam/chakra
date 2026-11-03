package host

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// skillSet holds every connected server's loaded skills and instructions. It
// keeps the system-prompt blocks each server contributes, each server's
// catalog entries for load_skill, and the config order for rendering the
// blocks.
//
// These fields moved together because they are read together, and one mutex
// always guarded them. The server list lives here, not beside the connection
// state, because of ordering. Both the prompt blocks and the catalog are
// assembled in config order. So the order belongs to how server content
// renders, not to how servers connect.
//
// Skills and instructions load in the ready-observer, which fires for late
// servers too. So this is shared mutable state, and the per-turn prompt
// builder reads it live.
type skillSet struct {
	mu           sync.Mutex
	blocks       map[string]string         // serverID -> system-prompt skill block
	instructions map[string]string         // serverID -> origin-labelled instructions block
	catalog      map[string][]catalogSkill // serverID -> catalog entries
	order        []string                  // serverIDs in config order
	loader       bool                      // load_skill registered once, lazily
}

func newSkillSet() *skillSet {
	return &skillSet{
		blocks:       map[string]string{},
		instructions: map[string]string{},
		catalog:      map[string][]catalogSkill{},
	}
}

// setInstructions records one server's origin-labelled instructions block. An
// empty block clears any earlier one, so a server that re-connects without
// instructions does not leave a stale block in the prompt.
func (s *skillSet) setInstructions(serverID, block string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if block == "" {
		delete(s.instructions, serverID)
		return
	}
	s.instructions[serverID] = block
}

// track records a server in config order, before it has loaded anything.
func (s *skillSet) track(serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, serverID)
}

// add stores one server's loaded skills, replacing what it had. It reports
// the cross-origin name collisions that this addition created, and whether
// load_skill still needs registering. It returns both rather than acting on
// them. Registering a tool and emitting warnings are the App's business, and
// doing them under this lock would widen it past the state it protects.
func (s *skillSet) add(serverID, block string, cat []catalogSkill) (collisions []string, registerLoader bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replace rather than merge. A re-list after list_changed returns the
	// server's whole current set, so a skill it dropped must go too.
	if block != "" {
		s.blocks[serverID] = block
	} else {
		delete(s.blocks, serverID)
	}
	if len(cat) > 0 {
		s.catalog[serverID] = cat
		collisions = s.collisionsLocked(serverID)
	} else {
		delete(s.catalog, serverID)
	}
	// Ask for load_skill lazily, once, on the first catalog skill, so it
	// never appears when no server offers one.
	if len(cat) > 0 && !s.loader {
		s.loader = true
		registerLoader = true
	}
	return collisions, registerLoader
}

// instructionBlocks returns each connected server's instructions block in
// config order.
func (s *skillSet) instructionBlocks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, id := range s.order {
		if block := s.instructions[id]; block != "" {
			out = append(out, block)
		}
	}
	return out
}

// promptBlocks returns each connected server's skill block in config order.
func (s *skillSet) promptBlocks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, id := range s.order {
		if block := s.blocks[id]; block != "" {
			out = append(out, block)
		}
	}
	return out
}

// allCatalog flattens every server's catalog entries in config order.
func (s *skillSet) allCatalog() []catalogSkill {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []catalogSkill
	for _, id := range s.order {
		out = append(out, s.catalog[id]...)
	}
	return out
}

// collisionsLocked reports each name that newID serves and another connected
// origin also serves. SEP-2640 asks hosts to surface these cross-origin
// collisions.
// Each name is reported once. Callers hold mu.
func (s *skillSet) collisionsLocked(newID string) []string {
	var out []string
	seenName := map[string]struct{}{}
	for _, cs := range s.catalog[newID] {
		name := cs.entry.Name()
		if name == "" {
			continue
		}
		if _, dup := seenName[name]; dup {
			continue
		}
		seenName[name] = struct{}{}
		var others []string
		for id, entries := range s.catalog {
			if id == newID {
				continue
			}
			for _, e := range entries {
				if e.entry.Name() == name {
					others = append(others, id)
					break
				}
			}
		}
		if len(others) > 0 {
			sort.Strings(others)
			out = append(out, fmt.Sprintf("%q served by %s and %s", name, newID, strings.Join(others, ", ")))
		}
	}
	sort.Strings(out)
	return out
}
