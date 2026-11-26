package connection

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"testing"
)

func dollars(d float64) *float64 { return &d }

// On Declaire's bench/families, 14b answered every F4 question and none of
// F3 (panyam/jaala#574). A per-tag table sends F4 to 14b and F3 to the
// frontier model, where one ladder for every question sends both to 14b.
func TestTableRouterPicksByTag(t *testing.T) {
	r := TableRouter{
		Connections: map[string]Config{"14b": {Price: &Price{}}, "frontier": {Price: &Price{Input: 1, Output: 4}}},
		Rows: []Row{
			{Connection: "14b", Tag: "F4", Pass: 17, Runs: 1, Dollars: dollars(0)},
			{Connection: "14b", Tag: "F3", Wrong: 19, Runs: 1, Dollars: dollars(0)},
			{Connection: "frontier", Tag: "F4", Pass: 16, Wrong: 1, Runs: 1, Dollars: dollars(0.4)},
			{Connection: "frontier", Tag: "F3", Pass: 15, Wrong: 4, Runs: 1, Dollars: dollars(0.5)},
		},
	}
	for tag, want := range map[string][]string{"F4": {"14b", "frontier"}, "F3": {"frontier", "14b"}} {
		got, err := r.Route(context.Background(), Request{Tags: []string{tag}})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: Route = %v, want %v", tag, got, want)
		}
	}
}

// Lambda trades pass rate for dollars per case. At zero the better
// connection leads, and a high enough lambda puts the cheaper one first.
func TestTableRouterLambdaTradesPassForCost(t *testing.T) {
	r := TableRouter{
		Connections: map[string]Config{"cheap": {}, "dear": {}},
		Rows: []Row{
			{Connection: "cheap", Tag: "x", Pass: 6, Wrong: 4, Runs: 1, Dollars: dollars(0.01)},
			{Connection: "dear", Tag: "x", Pass: 9, Wrong: 1, Runs: 1, Dollars: dollars(2)},
		},
	}
	for _, c := range []struct {
		lambda float64
		want   []string
	}{{0, []string{"dear", "cheap"}}, {10, []string{"cheap", "dear"}}} {
		got, err := r.Route(context.Background(), Request{Tags: []string{"x"}, Lambda: c.lambda})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("lambda %v: Route = %v, want %v", c.lambda, got, c.want)
		}
	}
}

// A tag with fewer cases than MinCases reads the connection's whole-set row
// (Tag "") instead, so two lucky cases don't outrank a long record.
func TestTableRouterBacksOffToTheWholeSet(t *testing.T) {
	r := TableRouter{
		Connections: map[string]Config{"a": {}, "b": {}},
		MinCases:    5,
		Rows: []Row{
			{Connection: "a", Tag: "rare", Pass: 2, Runs: 1, Dollars: dollars(0)},
			{Connection: "a", Pass: 10, Wrong: 30, Runs: 1, Dollars: dollars(0)},
			{Connection: "b", Tag: "rare", Pass: 1, Wrong: 1, Runs: 1, Dollars: dollars(0)},
			{Connection: "b", Pass: 30, Wrong: 10, Runs: 1, Dollars: dollars(0)},
		},
	}
	got, err := r.Route(context.Background(), Request{Tags: []string{"rare"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b", "a"}; !slices.Equal(got, want) {
		t.Errorf("Route = %v, want %v from the whole-set rows", got, want)
	}

	r.MinCases = 2
	got, _ = r.Route(context.Background(), Request{Tags: []string{"rare"}})
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("control: with MinCases 2, Route = %v, want %v from the tag rows", got, want)
	}
}

// A row's measured dollars win over the connection's Price. A row with
// tokens and no dollars is priced at the Price.
func TestRowCostMeasuredDollarsWin(t *testing.T) {
	price := &Price{Input: 2, Output: 10}
	measured := Row{Pass: 1, TokensIn: 1_000_000, Dollars: dollars(0.5)}
	if d, ok := measured.Cost(price); !ok || d != 0.5 {
		t.Errorf("measured row: Cost = %v, %v; want 0.5, true", d, ok)
	}
	cached := 0.5
	tokens := Row{Pass: 1, TokensIn: 1_000_000, TokensCached: 400_000, TokensOut: 100_000}
	want := 0.6*2 + 0.4*0.5 + 0.1*10
	if d, ok := tokens.Cost(&Price{Input: 2, CachedInput: &cached, Output: 10}); !ok || math.Abs(d-want) > 1e-12 {
		t.Errorf("token row: Cost = %v, %v; want %v, true", d, ok, want)
	}
	if _, ok := tokens.Cost(nil); ok {
		t.Errorf("token row with no Price: Cost says it knows")
	}
}

// Rows of another arm are left out: the same connection costs a different
// amount inside jaala's translator than as a chakra agent (#348).
func TestTableRouterReadsOnlyItsArm(t *testing.T) {
	r := TableRouter{
		Arm:         "translate",
		Connections: map[string]Config{"a": {}, "b": {}},
		Rows: []Row{
			{Connection: "a", Arm: "translate", Tag: "x", Pass: 9, Wrong: 1, Runs: 1, Dollars: dollars(0)},
			{Connection: "b", Arm: "translate", Tag: "x", Pass: 5, Wrong: 5, Runs: 1, Dollars: dollars(0)},
			{Connection: "b", Arm: "agent", Tag: "x", Pass: 100, Runs: 1, Dollars: dollars(0)},
		},
	}
	got, err := r.Route(context.Background(), Request{Tags: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("Route = %v, want %v from the translate rows only", got, want)
	}
}

// With a cost knob set, a connection whose cost is unknown goes after every
// one whose cost is known, since an unknown cost isn't zero. A connection
// with no row at all, or missing from Connections, is left out.
func TestTableRouterUnknownCostGoesLast(t *testing.T) {
	r := TableRouter{
		Connections: map[string]Config{"priced": {}, "unpriced": {}, "norow": {}},
		Rows: []Row{
			{Connection: "unpriced", Tag: "x", Pass: 10, Runs: 1, TokensIn: 5000},
			{Connection: "priced", Tag: "x", Pass: 5, Wrong: 5, Runs: 1, Dollars: dollars(0.1)},
			{Connection: "stranger", Tag: "x", Pass: 10, Runs: 1, Dollars: dollars(0)},
		},
	}
	got, err := r.Route(context.Background(), Request{Tags: []string{"x"}, Lambda: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"priced", "unpriced"}; !slices.Equal(got, want) {
		t.Errorf("lambda 1: Route = %v, want %v", got, want)
	}
	got, _ = r.Route(context.Background(), Request{Tags: []string{"x"}})
	if want := []string{"unpriced", "priced"}; !slices.Equal(got, want) {
		t.Errorf("control: lambda 0: Route = %v, want %v", got, want)
	}
}

// A task with several tags reads the rows of all of them together.
func TestTableRouterSumsSeveralTags(t *testing.T) {
	r := TableRouter{
		Connections: map[string]Config{"a": {}, "b": {}},
		Rows: []Row{
			{Connection: "a", Tag: "x", Pass: 10, Runs: 1, Dollars: dollars(0)},
			{Connection: "a", Tag: "y", Wrong: 30, Runs: 1, Dollars: dollars(0)},
			{Connection: "b", Tag: "x", Pass: 5, Wrong: 5, Runs: 1, Dollars: dollars(0)},
			{Connection: "b", Tag: "y", Pass: 15, Wrong: 15, Runs: 1, Dollars: dollars(0)},
		},
	}
	got, err := r.Route(context.Background(), Request{Tags: []string{"x", "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b", "a"}; !slices.Equal(got, want) {
		t.Errorf("Route = %v, want %v (a passes 10 of 40, b 20 of 40)", got, want)
	}
}

func TestRowLoadsFromJSON(t *testing.T) {
	var rows []Row
	if err := json.Unmarshal([]byte(`[{"connection":"14b","arm":"translate","tag":"F4","pass":17,"runs":1,"dollars":0,"seconds":120}]`), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Connection != "14b" || rows[0].Arm != "translate" || rows[0].Tag != "F4" || rows[0].Pass != 17 || rows[0].Dollars == nil || rows[0].Seconds != 120 {
		t.Errorf("rows = %+v", rows)
	}
}

// Two runs of one tag, one priced and one not, add up to an unknown cost
// when the connection has no Price, rather than to the priced run's dollars.
func TestTableRouterPartlyPricedRowsAreUnknown(t *testing.T) {
	r := TableRouter{
		Connections: map[string]Config{"mixed": {}, "priced": {}},
		Rows: []Row{
			{Connection: "mixed", Tag: "x", Pass: 10, Runs: 1, Dollars: dollars(0)},
			{Connection: "mixed", Tag: "x", Pass: 10, Runs: 1, TokensIn: 1_000_000},
			{Connection: "priced", Tag: "x", Pass: 5, Wrong: 15, Runs: 1, Dollars: dollars(0.1)},
		},
	}
	got, err := r.Route(context.Background(), Request{Tags: []string{"x"}, Lambda: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"priced", "mixed"}; !slices.Equal(got, want) {
		t.Errorf("Route = %v, want %v", got, want)
	}
}

// Two runs of a tag add their reasoning tokens, and the count doesn't change
// the cost, since TokensOut already bills it.
func TestRowSumsReasoningTokens(t *testing.T) {
	var rows []Row
	if err := json.Unmarshal([]byte(`[{"connection":"q","tag":"F4","pass":1,"tokensOut":900,"tokensReasoning":800},{"connection":"q","tag":"F4","pass":1,"tokensOut":100,"tokensReasoning":50}]`), &rows); err != nil {
		t.Fatal(err)
	}
	sum := rows[0]
	sum.add(rows[1])
	if sum.TokensReasoning != 850 || sum.TokensOut != 1000 {
		t.Errorf("sum = %+v, want 850 reasoning of 1000 out", sum)
	}
	price := &Price{Output: 1}
	withThinking, _ := sum.Cost(price)
	sum.TokensReasoning = 0
	without, _ := sum.Cost(price)
	if withThinking != without {
		t.Errorf("cost with reasoning = %v, without = %v; want the same", withThinking, without)
	}
}
