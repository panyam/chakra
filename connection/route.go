package connection

import (
	"context"
	"sort"
)

// Row is how one connection did on one kind of task, run one way (#348).
// A benchmark writes rows, and a TableRouter reads them.
type Row struct {
	// Connection names the connection, as a profile's connections map keys
	// it.
	Connection string `json:"connection"`
	// Arm names how the connection was run, such as "translate" for jaala's
	// translator or "agent" for a chakra agent. The caller picks the names.
	// The arm matters because one model can cost several times as much run
	// one way as the other, so rows of two arms don't average.
	Arm string `json:"arm,omitempty"`
	// Tag names the kind of task. The caller picks the names too. An empty
	// Tag is the connection's row over the whole set.
	Tag string `json:"tag,omitempty"`
	// Pass, Wrong and Abstain count the cases by outcome.
	Pass    int `json:"pass,omitempty"`
	Wrong   int `json:"wrong,omitempty"`
	Abstain int `json:"abstain,omitempty"`
	// Runs counts the benchmark runs added into the row.
	Runs int `json:"runs,omitempty"`
	// Dollars is what the cases cost when they ran. Nil means the run
	// recorded no price, and Cost then prices the tokens.
	Dollars *float64 `json:"dollars,omitempty"`
	// TokensIn counts prompt tokens, TokensCached included.
	TokensIn     int `json:"tokensIn,omitempty"`
	TokensCached int `json:"tokensCached,omitempty"`
	TokensOut    int `json:"tokensOut,omitempty"`
	// TokensReasoning counts the part of TokensOut spent thinking, as
	// agent.Usage.ReasoningTokens does. Cost doesn't read it, since
	// TokensOut already bills it.
	TokensReasoning int `json:"tokensReasoning,omitempty"`
	// Seconds is the cases' wall time.
	Seconds float64 `json:"seconds,omitempty"`
}

// Cases is Pass, Wrong and Abstain together.
func (r Row) Cases() int { return r.Pass + r.Wrong + r.Abstain }

// Cost is what the row's cases cost. Dollars wins when the row has it, since
// a row should keep the prices it ran with. Otherwise Cost prices the tokens
// at price, as Config.Cost does. It returns false when the row has no
// Dollars and price is nil.
func (r Row) Cost(price *Price) (float64, bool) {
	if r.Dollars != nil {
		return *r.Dollars, true
	}
	if price == nil {
		return 0, false
	}
	cachedRate := price.Input
	if price.CachedInput != nil {
		cachedRate = *price.CachedInput
	}
	cached := min(r.TokensCached, r.TokensIn)
	return (float64(r.TokensIn-cached)*price.Input + float64(cached)*cachedRate + float64(r.TokensOut)*price.Output) / 1e6, true
}

// add sums o into r. The sum has Dollars only when both had it, since a run
// with no price would otherwise count as free.
func (r *Row) add(o Row) {
	if r.Dollars != nil && o.Dollars != nil {
		d := *r.Dollars + *o.Dollars
		r.Dollars = &d
	} else {
		r.Dollars = nil
	}
	r.Pass += o.Pass
	r.Wrong += o.Wrong
	r.Abstain += o.Abstain
	r.Runs += o.Runs
	r.TokensIn += o.TokensIn
	r.TokensCached += o.TokensCached
	r.TokensOut += o.TokensOut
	r.TokensReasoning += o.TokensReasoning
	r.Seconds += o.Seconds
}

// Request is what a Router routes: one task.
type Request struct {
	// Tags name the kinds of task this one is, in the rows' terms.
	Tags []string
	// Lambda is the cost knob: how many points of pass rate a dollar per
	// task is worth giving up. Zero ignores cost.
	Lambda float64
}

// Router picks the connections to try for a task, best first. A host routes
// once per task rather than per step, since switching connections partway
// through a task throws away the provider's prompt cache.
type Router interface {
	Route(ctx context.Context, req Request) ([]string, error)
}

// defaultMinCases is TableRouter.MinCases when it is zero.
const defaultMinCases = 5

// TableRouter routes by looking up each connection's rows for the task's
// tags. A connection scores its pass rate over those rows, less Lambda times
// its dollars per case, and the connections go out best score first, ties by
// name.
//
// It reads only the rows for Arm and only the connections in Connections. A
// connection with no usable row is left out. When Lambda is above zero, a
// connection whose cost is unknown goes after every one whose cost is known,
// since an unknown cost isn't zero.
type TableRouter struct {
	// Rows are the profile rows.
	Rows []Row
	// Arm picks the rows to read. Empty reads the rows with no arm.
	Arm string
	// Connections are the connections to route between. A connection's
	// Price prices a row that has tokens and no Dollars.
	Connections map[string]Config
	// MinCases is how many cases a tag's row needs before the router trusts
	// it. A tag with fewer reads the connection's whole-set row instead.
	// Zero means 5.
	MinCases int
}

// Route implements Router. It never fails.
func (t TableRouter) Route(_ context.Context, req Request) ([]string, error) {
	minCases := t.MinCases
	if minCases == 0 {
		minCases = defaultMinCases
	}
	rows := map[string]map[string]Row{}
	for _, r := range t.Rows {
		if r.Arm != t.Arm {
			continue
		}
		if _, ok := t.Connections[r.Connection]; !ok {
			continue
		}
		byTag := rows[r.Connection]
		if byTag == nil {
			byTag = map[string]Row{}
			rows[r.Connection] = byTag
		}
		sum, seen := byTag[r.Tag]
		if seen {
			sum.add(r)
		} else {
			sum = r
		}
		byTag[r.Tag] = sum
	}

	type scored struct {
		name  string
		score float64
		known bool
	}
	var out []scored
	for name, byTag := range rows {
		price := t.Connections[name].Price
		var total Row
		dollars, known := 0.0, true
		for _, tag := range tags(req.Tags) {
			r, ok := byTag[tag]
			if !ok || r.Cases() < minCases {
				if r, ok = byTag[""]; !ok {
					continue
				}
			}
			total.add(r)
			d, ok := r.Cost(price)
			dollars += d
			known = known && ok
		}
		if total.Cases() == 0 {
			continue
		}
		n := float64(total.Cases())
		s := scored{name: name, score: float64(total.Pass) / n, known: known || req.Lambda == 0}
		if known {
			s.score -= req.Lambda * dollars / n
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.known != b.known {
			return a.known
		}
		if a.score != b.score {
			return a.score > b.score
		}
		return a.name < b.name
	})
	names := make([]string, len(out))
	for i, s := range out {
		names[i] = s.name
	}
	return names, nil
}

// tags is the task's tags, or the whole set's when it has none.
func tags(ts []string) []string {
	if len(ts) == 0 {
		return []string{""}
	}
	return ts
}
