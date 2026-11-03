// Package files edits text by anchoring to the content being replaced, so an
// edit built against a stale view of a file fails instead of applying.
//
// Imagine an agent reads a file, decides on a change, and writes it back. In
// between, the file changed underneath. A formatter ran, a sibling tool wrote
// it, or the user saved in their editor. An edit addressed by line number
// applies anyway and silently discards that change. Nothing reports it,
// because nothing noticed. This package exists to prevent that failure.
//
// Two mechanisms answer two different questions. An anchor is the exact text
// being replaced. It says WHERE a change goes, and it survives the line
// numbers moving. A content hash says WHETHER the file is still the one the
// caller looked at. Neither substitutes for the other. An anchor can still
// match uniquely in a file that was reformatted underneath, and that is
// precisely the case where applying it is wrong. A hash cannot locate
// anything.
//
// Nothing here is specific to code. It needs a filesystem and text, not a
// language, so a data file, a prose draft, and a source file are the same
// problem to it.
package files

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The conditions under which an edit refuses to apply. They are separate
// sentinels rather than one generic failure, because the caller answers each
// one differently. A stale file wants re-reading, an ambiguous anchor wants
// more surrounding context, and a missing anchor means the assumed text was
// never there.
//
// Errors wrap these with the specifics, so match with errors.Is and read the
// message for which hunk and how many matches.
var (
	// ErrStale means the content did not match ExpectHash. The file changed
	// after the caller read it, so every anchor in the edit was written
	// against a view that no longer exists.
	ErrStale = errors.New("file changed since it was read")

	// ErrAnchorNotFound means a hunk's Old text does not appear at all.
	ErrAnchorNotFound = errors.New("anchor not found")

	// ErrAnchorAmbiguous means a hunk's Old text appears more than once, so
	// there is no single place the edit belongs. The fix is to extend the
	// anchor with surrounding lines until it is unique, never to pick one.
	ErrAnchorAmbiguous = errors.New("anchor is not unique")

	// ErrOverlap means two hunks matched regions that share characters, so
	// their replacements would contradict each other.
	ErrOverlap = errors.New("hunks overlap")

	// ErrEmptyAnchor means a hunk has no Old text. An empty anchor matches at
	// every position rather than none, so it is rejected as malformed instead
	// of being reported as ambiguous.
	ErrEmptyAnchor = errors.New("hunk has an empty anchor")

	// ErrNoHunks means an Edit carried no hunks. Applying it would be a
	// no-op. More likely the caller built the edit wrong than meant to change
	// nothing.
	ErrNoHunks = errors.New("edit has no hunks")
)

// Hunk is one replacement, located by the text it replaces.
type Hunk struct {
	// Old is the exact text to replace. It must appear exactly once in the
	// content. Matching is byte-exact: no whitespace normalization, no fuzzy
	// or nearest match. Approximate matching produces silently wrong edits,
	// and this package exists to prevent those. So if a caller's anchor does
	// not match, the caller gets an error and a chance to look again.
	Old string

	// New is what replaces Old. Empty deletes the anchored text.
	New string
}

// Edit is a set of hunks applied to one file's content as a single unit.
type Edit struct {
	// ExpectHash is the Hash of the content that the caller wrote this edit
	// against. Empty skips the check. That is right only when the caller has
	// some other guarantee that nothing changed in between. Without such a
	// guarantee, an edit can still apply cleanly to a file it was never meant
	// for.
	ExpectHash string

	// Hunks are applied together or not at all. Each anchor is matched
	// against the original content rather than against the result of earlier
	// hunks, so listing them in another order does not change the outcome.
	Hunks []Hunk
}

// Apply returns src with every hunk applied, or an error and no partial result.
//
// All or nothing, and order-independent. Every anchor is resolved against the
// original src before anything is spliced. So a hunk cannot match text that an
// earlier hunk introduced, and a failure on the last hunk leaves the caller
// exactly where it started. Both properties have one reason. The caller wrote
// all these hunks while looking at one version of the content, so the hunks
// should be interpreted against that version.
func (e Edit) Apply(src string) (string, error) {
	if e.ExpectHash != "" {
		if got := Hash(src); got != e.ExpectHash {
			return "", fmt.Errorf("%w: expected %s, found %s; re-read it before editing", ErrStale, e.ExpectHash, got)
		}
	}
	if len(e.Hunks) == 0 {
		return "", ErrNoHunks
	}

	type span struct {
		start, end int
		replace    string
	}
	spans := make([]span, 0, len(e.Hunks))
	for i, h := range e.Hunks {
		if h.Old == "" {
			return "", fmt.Errorf("hunk %d: %w", i, ErrEmptyAnchor)
		}
		switch n := strings.Count(src, h.Old); {
		case n == 0:
			return "", fmt.Errorf("hunk %d: %w: %s", i, ErrAnchorNotFound, quote(h.Old))
		case n > 1:
			return "", fmt.Errorf("hunk %d: %w: %s matches %d places; extend it until it matches one", i, ErrAnchorAmbiguous, quote(h.Old), n)
		}
		start := strings.Index(src, h.Old)
		spans = append(spans, span{start: start, end: start + len(h.Old), replace: h.New})
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", fmt.Errorf("%w: two hunks both cover offset %d", ErrOverlap, spans[i].start)
		}
	}

	var b strings.Builder
	prev := 0
	for _, s := range spans {
		b.WriteString(src[prev:s.start])
		b.WriteString(s.replace)
		prev = s.end
	}
	b.WriteString(src[prev:])
	return b.String(), nil
}

// quote renders an anchor for an error message, shortened so a hunk spanning
// half the file does not bury the rest of the message.
func quote(s string) string {
	const max = 60
	if len(s) > max {
		s = s[:max] + "..."
	}
	return fmt.Sprintf("%q", s)
}
