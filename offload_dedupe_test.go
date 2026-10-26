package agent

import (
	"regexp"
	"strings"
	"testing"
)

var refPattern = regexp.MustCompile(`stored as (res:[0-9a-f]+)`)

func stubRef(t *testing.T, stub string) string {
	t.Helper()
	m := refPattern.FindStringSubmatch(stub)
	if m == nil {
		t.Fatalf("no ref in stub: %q", stub)
	}
	return m[1]
}

// #109: re-reading an unchanged file stored a second copy under a new ref,
// and nothing told the model it had already paged through this content.
func TestOffloadingSameContentSharesARefAndSaysSo(t *testing.T) {
	big := strings.Repeat("unchanged file line\n", 50)
	o, _ := newOffloader(t, textResult(big), OffloadConfig{Threshold: 100})

	first := toolResultText(callDump(t, o))
	second := toolResultText(callDump(t, o))
	if a, b := stubRef(t, first), stubRef(t, second); a != b {
		t.Fatalf("same content stored under two refs: %s, %s", a, b)
	}
	if strings.Contains(first, "identical to an earlier result") {
		t.Errorf("first stub claims it was seen before:\n%s", first)
	}
	if !strings.Contains(second, "identical to an earlier result") {
		t.Errorf("second stub does not say the content was seen before:\n%s", second)
	}
	if !strings.Contains(second, ReadToolResultName) {
		t.Errorf("second stub lost the way to read it:\n%s", second)
	}
}

func TestOffloadingDifferentContentGetsDifferentRefs(t *testing.T) {
	a, _ := newOffloader(t, textResult(strings.Repeat("a", 500)), OffloadConfig{Threshold: 100})
	b, _ := newOffloader(t, textResult(strings.Repeat("b", 500)), OffloadConfig{Threshold: 100})
	if ra, rb := stubRef(t, toolResultText(callDump(t, a))), stubRef(t, toolResultText(callDump(t, b))); ra == rb {
		t.Fatalf("different content shares ref %s", ra)
	}
}
