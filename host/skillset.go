package host

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// skillSet holds every connected server's loaded skills and instructions: the
// system-prompt blocks a server contributes, its catalog entries for
// load_skill, and the config order the blocks are rendered in.
//
// These fields moved together because they are read together and were always
// guarded by one mutex. Ordering is the reason the server list lives here
// rather than beside the connection state: both the prompt blocks and the
// catalog are assembled in config order, so the order is a property of how
// server content renders, not of how servers connect.
//
// Skills and instructions load in the ready-observer, which fires for late
// servers too, so this is shared mutable state read live by the per-turn prompt
// builder.
type skillSet struct {
	mu           sync.Mutex
	blocks       map[string]string         // serverID -> system-prompt skill block
	instructions map[string]string         // serverID -> origin-labelled instructions block
	catalog      map[string][]catalogSkill // serverID -> catalog entries
	order        []string                  // serverIDs in config order
	loader       bool                      // load_skill registered once, lazily
	watched      map[string]bool           // servers whose skill changes are watched
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

// watch reports whether serverID still needs its list_changed watch, marking
// it watched. A server that reconnects keeps the one watch it already has.
func (s *skillSet) watch(serverID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watched == nil {
		s.watched = map[string]bool{}
	}
	if s.watched[serverID] {
		return false
	}
	s.watched[serverID] = true
	return true
}

// add stores one server's loaded skills, replacing what it had, and reports the cross-origin name
// collisions this addition created, plus whether load_skill still needs
// registering. Both are returned rather than acted on here: registering a tool
// and emitting warnings are the App's business, and doing them under this lock
// would widen it past the state it protects.
func (s *skillSet) add(serverID, block string, cat []catalogSkill) (collisions []string, registerLoader bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replace rather than merge: a re-list after list_changed carries the
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
	// Lazily, once, on the first catalog skill — so load_skill never appears
	// when no server offers one.
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

// collisionsLocked reports names newID serves that another connected origin
// also serves — the cross-origin collisions SEP-2640 asks hosts to surface.
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
