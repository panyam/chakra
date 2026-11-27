package codeq

import (
	"slices"
	"strings"
	"testing"
)

// Every question's wanted rows match what the index says, computed by
// walking it, so a fixture edit can't leave an answer stale.
func TestQuestionAnswersMatchTheIndex(t *testing.T) {
	root, err := Fixture()
	if err != nil {
		t.Fatal(err)
	}
	ix, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	rows := ix.Rows()
	for _, q := range questions {
		for _, w := range q.want {
			if !slices.Contains(rows, w) {
				t.Errorf("%q wants %q, which the index doesn't have", q.text, w)
			}
		}
	}
	// reach lists the functions that call fn through at least one hop.
	reach := func(fn string) []string {
		seen := map[string]bool{}
		next := []string{fn}
		for len(next) > 0 {
			cur := next[0]
			next = next[1:]
			cs, _ := ix.CallersOf(cur)
			for _, c := range cs {
				if !seen[c] {
					seen[c] = true
					next = append(next, c)
				}
			}
		}
		var out []string
		for c := range seen {
			out = append(out, c)
		}
		slices.Sort(out)
		return out
	}
	inShop := func(xs []string) []string {
		return slices.DeleteFunc(xs, func(x string) bool { return !strings.HasPrefix(x, "example.com/shop/") })
	}
	direct, _ := ix.CallersOf("example.com/ledger/journal.Post")
	indirect := slices.DeleteFunc(inShop(reach("example.com/ledger/journal.Post")), func(x string) bool { return slices.Contains(direct, x) })
	check := func(text string, got []string) {
		i := slices.IndexFunc(questions, func(q question) bool { return q.text == text })
		if i < 0 {
			t.Fatalf("no question %q", text)
		}
		want := slices.Clone(questions[i].want)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%q: the index says %v, the question wants %v", text, got, want)
		}
	}
	check(questions[12].text, indirect)
	check(questions[13].text, reach("example.com/shop/internal/db.Save"))
	// The ledger packages api reaches through imports.
	dep := map[string]bool{}
	var walk func(string)
	walk = func(p string) {
		es, _ := ix.ImportsOf(p)
		for _, e := range es {
			if !e.TestOnly && !dep[e.Path] {
				dep[e.Path] = true
				walk(e.Path)
			}
		}
	}
	walk("example.com/shop/api")
	var ledger []string
	for p := range dep {
		if strings.HasPrefix(p, "example.com/ledger/") {
			ledger = append(ledger, p)
		}
	}
	check(questions[14].text, ledger)
	var none []string
	for _, p := range ix.PackagesIn("example.com/shop") {
		es, _ := ix.ImportsOf(p)
		if !slices.ContainsFunc(es, func(e Edge) bool { return !e.TestOnly && strings.HasPrefix(e.Path, "example.com/ledger/") }) {
			none = append(none, p)
		}
	}
	check(questions[11].text, none)
}
