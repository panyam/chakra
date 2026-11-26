package support

import (
	"context"
	"embed"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/panyam/chakra"
)

//go:embed docs/*.md
var docFS embed.FS

// doc is one policy page: its name, which a citation uses as [name], and
// its text.
type doc struct {
	name, text string
}

// docs are the policy pages, sorted by name.
func docs() []doc {
	entries, _ := docFS.ReadDir("docs")
	var out []doc
	for _, e := range entries {
		raw, _ := docFS.ReadFile("docs/" + e.Name())
		out = append(out, doc{name: strings.TrimSuffix(e.Name(), path.Ext(e.Name())), text: string(raw)})
	}
	return out
}

// order is one order in the shop's database.
type order struct {
	ID        string
	Customer  string
	Item      string
	Price     int // cents
	Status    string
	DaysAgo   int // since delivery, for a delivered order
	FinalSale bool
	Opened    bool
	Address   string
	Refunded  bool
}

// fixture is the database every case starts from.
func fixture() map[string]*order {
	orders := []order{
		{ID: "A1", Customer: "Ana", Item: "wool scarf", Price: 3500, Status: "delivered", DaysAgo: 10, Address: "4 Elm St, Springfield"},
		{ID: "A2", Customer: "Ben", Item: "rain jacket", Price: 8900, Status: "delivered", DaysAgo: 45, Address: "9 Pine Rd, Shelbyville"},
		{ID: "A3", Customer: "Cal", Item: "clearance boots", Price: 4000, Status: "delivered", DaysAgo: 5, FinalSale: true, Address: "2 Bay Ave, Ogdenville"},
		{ID: "A4", Customer: "Dee", Item: "desk lamp", Price: 2500, Status: "processing", Address: "7 Hill Ct, North Haverbrook"},
		{ID: "A5", Customer: "Eve", Item: "headphones", Price: 12000, Status: "shipped", Address: "5 Lake Dr, Capital City"},
		{ID: "A6", Customer: "Fin", Item: "kettle", Price: 3000, Status: "delivered", DaysAgo: 20, Address: "1 Oak Way, Springfield"},
	}
	m := map[string]*order{}
	for i := range orders {
		o := orders[i]
		m[o.ID] = &o
	}
	return m
}

// shop is the database the current case works on, and what the agent did
// to it. Cases run one at a time, and each case's Setup resets it.
type shop struct {
	mu          sync.Mutex
	orders      map[string]*order
	escalations []string
	// replies is everything the agent said to the customer in the case so
	// far, kept by the case's User, which sees the whole conversation.
	replies []string
}

func (s *shop) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders, s.escalations, s.replies = fixture(), nil, nil
}

// heard keeps the agent's replies in conversation.
func (s *shop) heard(conversation []agent.Message) {
	var replies []string
	for _, m := range conversation {
		if m.Role == agent.RoleAssistant && strings.TrimSpace(m.Text) != "" {
			replies = append(replies, m.Text)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = replies
}

func (s *shop) get(id string) (order, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[strings.ToUpper(strings.TrimSpace(id))]
	if !ok {
		return order{}, false
	}
	return *o, true
}

// refundable says why o can't be refunded, or "" when it can.
func refundable(o order) string {
	switch {
	case o.Refunded:
		return "it was already refunded"
	case o.Status != "delivered":
		return "it hasn't been delivered"
	case o.FinalSale:
		return "it was a final sale"
	case o.Opened:
		return "it was opened"
	case o.DaysAgo > 30:
		return fmt.Sprintf("it was delivered %d days ago, past the 30-day window", o.DaysAgo)
	}
	return ""
}

// readdressable says why o's address can't change, or "" when it can.
func readdressable(o order) string {
	if o.Status != "processing" {
		return "it has already " + o.Status
	}
	return ""
}

// tools is the shop's tools. handoff adds escalate_to_human.
func (s *shop) tools(handoff bool) (*agent.FuncSource, error) {
	src := agent.NewFuncSource()
	type orderArg struct {
		OrderID string `json:"order_id" jsonschema:"the order number, such as A1"`
	}
	err := agent.AddFunc(src, "lookup_order", "Look up an order: item, price, status, days since delivery, final sale, address, and whether it was refunded.",
		func(_ context.Context, in orderArg) (string, error) {
			o, ok := s.get(in.OrderID)
			if !ok {
				return "", fmt.Errorf("no order %q", in.OrderID)
			}
			return fmt.Sprintf("order %s: %s for $%d.%02d, customer %s, status %s, delivered %d days ago, final sale %v, opened %v, address %q, refunded %v",
				o.ID, o.Item, o.Price/100, o.Price%100, o.Customer, o.Status, o.DaysAgo, o.FinalSale, o.Opened, o.Address, o.Refunded), nil
		}, agent.ReadOnly())
	if err != nil {
		return nil, err
	}
	err = agent.AddFunc(src, "refund_order", "Refund an order in full, to the original payment method.",
		func(_ context.Context, in orderArg) (string, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			o, ok := s.orders[strings.ToUpper(strings.TrimSpace(in.OrderID))]
			if !ok {
				return "", fmt.Errorf("no order %q", in.OrderID)
			}
			o.Refunded = true
			return fmt.Sprintf("refunded order %s, $%d.%02d", o.ID, o.Price/100, o.Price%100), nil
		})
	if err != nil {
		return nil, err
	}
	err = agent.AddFunc(src, "change_address", "Change an order's delivery address.",
		func(_ context.Context, in struct {
			OrderID string `json:"order_id" jsonschema:"the order number, such as A1"`
			Address string `json:"address" jsonschema:"the new delivery address"`
		}) (string, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			o, ok := s.orders[strings.ToUpper(strings.TrimSpace(in.OrderID))]
			if !ok {
				return "", fmt.Errorf("no order %q", in.OrderID)
			}
			o.Address = in.Address
			return fmt.Sprintf("order %s now ships to %q", o.ID, o.Address), nil
		})
	if err != nil {
		return nil, err
	}
	if handoff {
		err = agent.AddFunc(src, "escalate_to_human", "Hand the conversation to a person, with a summary of what the customer needs.",
			func(_ context.Context, in struct {
				Summary string `json:"summary" jsonschema:"what the customer needs, in a sentence or two"`
			}) (string, error) {
				s.mu.Lock()
				defer s.mu.Unlock()
				s.escalations = append(s.escalations, in.Summary)
				return "handed off: a person will reply to the customer within a day", nil
			})
		if err != nil {
			return nil, err
		}
	}
	return src, nil
}

// search returns the k policy pages that share the most words with query,
// each with its name in brackets. A page sharing no words is left out.
func search(query string, k int) []doc {
	words := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
			if len(w) > 3 {
				m[strings.TrimSuffix(w, "s")] = true
			}
		}
		return m
	}
	q := words(query)
	type hit struct {
		d     doc
		score int
	}
	var hits []hit
	for _, d := range docs() {
		n := 0
		for w := range words(d.name + " " + d.text) {
			if q[w] {
				n++
			}
		}
		if n > 0 {
			hits = append(hits, hit{d, n})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var out []doc
	for _, h := range hits[:min(k, len(hits))] {
		out = append(out, h.d)
	}
	return out
}

// cited lists the page names cited in text as [name].
func cited(text string) []string {
	var out []string
	for _, d := range docs() {
		if strings.Contains(text, "["+d.name+"]") && !slices.Contains(out, d.name) {
			out = append(out, d.name)
		}
	}
	return out
}
