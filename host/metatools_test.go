package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/chakra"
)

// A trigger in config turns on event subscription and the consumer, but not
// the model-facing control-plane tools. Those need metaTools: true, so a
// page co-pilot's model doesn't see create_trigger or remove_trigger beside
// its page tools (#383).
func TestATriggerInConfigOffersNoMetaTools(t *testing.T) {
	ts := startTestServer(t)
	offered := func(meta bool) []string {
		cfg := testConfig(ts.URL)
		cfg.MetaTools = meta
		cfg.Triggers = []TriggerConfig{{Server: "page", Event: "card.moved", Instructions: "check the card", Label: "check"}}
		stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
		app, err := NewApp(cfg, &syncWriter{}, strings.NewReader(""), WithProvider(stub))
		if err != nil {
			t.Fatal(err)
		}
		defer app.Close()
		if err := app.RunTurn(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, d := range stub.Requests()[0].Tools {
			names = append(names, d.Name)
		}
		return names
	}
	if got := offered(false); slices.Contains(got, "create_trigger") || slices.Contains(got, "remove_trigger") {
		t.Errorf("a config trigger offered the model %v, want no meta-tools", got)
	}
	if got := offered(true); !slices.Contains(got, "create_trigger") {
		t.Errorf("metaTools: true offered %v, want create_trigger among them", got)
	}
}
