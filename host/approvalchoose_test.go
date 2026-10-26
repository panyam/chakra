package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

// pickExt owns a "pick" tool whose items can be approved one at a time. The
// chooser is the shape ext/files uses for hunks, cut down to what the wiring
// needs: one boolean per item, and a narrowed call carrying the kept ones.
type pickExt struct {
	BaseExtension
	ran *[]string
}

func (pickExt) Name() string { return "picker" }

func (e pickExt) Tools() (agent.ToolSource, error) {
	src := agent.NewFuncSource()
	err := agent.AddFunc(src, "pick", "picks items", func(_ context.Context, in struct {
		Items []string `json:"items"`
	}) (string, error) {
		*e.ran = in.Items
		return "picked " + strings.Join(in.Items, ","), nil
	})
	return src, err
}

func (pickExt) ApprovalChoosers() []ApprovalChooser {
	return []ApprovalChooser{func(ctx context.Context, info agent.ToolCallInfo, present agent.ElicitationUI) (agent.Choice, error) {
		if info.Call.Name != "pick" {
			return agent.Choice{}, nil
		}
		var in struct {
			Items []string `json:"items"`
		}
		if err := json.Unmarshal(info.Call.Args.Raw(), &in); err != nil {
			return agent.Choice{}, nil
		}
		props := map[string]any{}
		for _, it := range in.Items {
			props[it] = map[string]any{"type": "boolean"}
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props})
		res, err := present(ctx, core.ElicitationRequest{Message: "Pick which?", RequestedSchema: schema})
		if err != nil || res.Action != "accept" {
			return agent.Choice{Claimed: true}, err
		}
		var kept []string
		for _, it := range in.Items {
			if v, _ := res.Content[it].(bool); v {
				kept = append(kept, it)
			}
		}
		args, err := core.MarshalRawJSON(map[string]any{"items": kept})
		return agent.Choice{Claimed: true, Approved: true, Args: args, Note: "the user left some out"}, err
	}}
}

// TestChooserAnswersTheAskThroughTheTerminal drives the whole host path: the
// gate hands a claimed call to the extension's chooser, the chooser's form is
// answered field by field on the terminal, the tool runs with the narrowed
// arguments, and the model reads the note. The plain y/n ask never appears,
// so the input below would desynchronise if it did.
func TestChooserAnswersTheAskThroughTheTerminal(t *testing.T) {
	ts := startTestServer(t)
	cfg := testConfig(ts.URL)
	cfg.Approval = &ApprovalConfig{Mode: "ask"}

	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "pick", Args: core.NewRawJSON(json.RawMessage(`{"items":["a","b"]}`))}}},
		agent.StubTurn{Text: "done"},
	)
	var ran []string
	var out strings.Builder
	// Fields prompt in sorted order: a, then b.
	app, err := NewApp(cfg, &out, strings.NewReader("y\nn\n"), WithProvider(stub), WithExtension(pickExt{ran: &ran}))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "pick a and b"); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "a" {
		t.Fatalf("tool ran with %v, want [a]:\n%s", ran, out.String())
	}
	if strings.Contains(out.String(), "Allow tool call") {
		t.Errorf("the plain ask was shown as well:\n%s", out.String())
	}
	toolMsg := stub.Requests()[1].Messages[2]
	if !strings.Contains(toolMsg.Text, "picked a") || !strings.Contains(toolMsg.Text, "the user left some out") {
		t.Errorf("model saw %q, want the result and the note", toolMsg.Text)
	}
}
