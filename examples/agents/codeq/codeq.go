// Package codeq is an agent that answers questions about Go code through a
// tool server over MCP (#351). The server, cmd/codeserver, indexes two small
// modules in testdata, example.com/shop and example.com/ledger, and runs as
// its own process. The eval asks fifteen questions whose answers are rows:
// import paths, or qualified names such as example.com/shop/orders.Place.
package codeq

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// Instructions is the agent's system prompt on every step.
const Instructions = "You answer questions about Go code in two modules, example.com/shop and example.com/ledger. Look the answer up with the tools rather than guessing."

// rules is what the second step adds to the prompt: when to use which tool,
// and the two scoping rules a question can carry.
const rules = `
Use packages for a module's packages, imports for what one package imports, importers for what imports a package, find to turn a bare name into a qualified one, and callers for what calls a function.
When a question says "in <module>", leave out every row from other modules.
When a question says "non-test", leave out rows marked (test files only).`

// question is one case: its text, the rows a right answer names, and its
// tag. The tag names the kind of lookup the question needs, so a router can
// send each kind to a different model (#365).
type question struct {
	text string
	want []string
	tag  string
}

// The tags.
const (
	definition = "definition"
	imports    = "imports"
	reverseHop = "reverse hop"
	negation   = "negation"
	callers    = "callers"
	multiHop   = "multi-hop"
)

var questions = []question{
	{"What is the qualified name of the function Format in example.com/ledger?", []string{"example.com/ledger/money.Format"}, definition},
	{"What is the qualified name of the type Amount?", []string{"example.com/ledger/money.Amount"}, definition},
	{"Which packages does example.com/shop/orders import?",
		[]string{"example.com/ledger/journal", "example.com/ledger/money", "example.com/shop/catalog", "example.com/shop/internal/db"}, imports},
	{"Which packages in example.com/shop import example.com/ledger/money, counting only non-test files?",
		[]string{"example.com/shop/catalog", "example.com/shop/orders", "example.com/shop/receipt"}, reverseHop},
	{"Which packages in example.com/shop import example.com/ledger/journal?",
		[]string{"example.com/shop/orders", "example.com/shop/payments", "example.com/shop/refunds"}, reverseHop},
	{"Which functions in example.com/shop call example.com/ledger/journal.Post?",
		[]string{"example.com/shop/orders.Place", "example.com/shop/payments.Charge"}, callers},
	{"Which functions call Save from example.com/shop/internal/db?",
		[]string{"example.com/shop/orders.Place", "example.com/shop/payments.Charge"}, callers},
	{"Which packages make up the module example.com/ledger?",
		[]string{"example.com/ledger/audit", "example.com/ledger/journal", "example.com/ledger/money"}, imports},
	{"Which packages import example.com/shop/catalog?", []string{"example.com/shop/orders"}, reverseHop},
	{"Which packages in example.com/shop import example.com/shop/orders?",
		[]string{"example.com/shop/checkout", "example.com/shop/cmd/shopd"}, reverseHop},
	{"What is the qualified name of the function Format in example.com/shop?", []string{"example.com/shop/receipt.Format"}, definition},
	{"Which packages in example.com/shop import nothing from example.com/ledger, counting only non-test files?",
		[]string{"example.com/shop/api", "example.com/shop/checkout", "example.com/shop/cmd/shopd", "example.com/shop/internal/db"}, negation},
	{"Which functions in example.com/shop reach example.com/ledger/journal.Post only through other functions, without calling it directly?",
		[]string{"example.com/shop/api.Handle", "example.com/shop/checkout.Run", "example.com/shop/cmd/shopd.main", "example.com/shop/refunds.Issue"}, multiHop},
	{"Which functions call example.com/shop/internal/db.Save, directly or through other functions?",
		[]string{"example.com/shop/api.Handle", "example.com/shop/checkout.Run", "example.com/shop/cmd/shopd.main", "example.com/shop/orders.Place", "example.com/shop/payments.Charge"}, multiHop},
	{"Which packages in example.com/ledger does example.com/shop/api depend on, directly or through other packages?",
		[]string{"example.com/ledger/journal", "example.com/ledger/money"}, multiHop},
}

// Fixture is the directory holding the two modules, found through the go
// tool so it works from any directory in the examples/agents module.
func Fixture() (string, error) {
	out, err := exec.Command("go", "list", "-f", "{{.Dir}}", "github.com/panyam/chakra/examples/agents/codeq").Output()
	if err != nil {
		return "", fmt.Errorf("codeq: finding the fixture: %w", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "testdata"), nil
}

// Start builds cmd/codeserver, runs it on the fixture and connects to it
// over stdio. stop closes the client, which stops the server, and removes
// the binary.
func Start(ctx context.Context) (c *client.Client, ix *Index, stop func(), err error) {
	root, err := Fixture()
	if err != nil {
		return nil, nil, nil, err
	}
	if ix, err = Load(root); err != nil {
		return nil, nil, nil, err
	}
	dir, err := os.MkdirTemp("", "codeserver")
	if err != nil {
		return nil, nil, nil, err
	}
	bin := filepath.Join(dir, "codeserver")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/panyam/chakra/examples/agents/codeq/cmd/codeserver")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return nil, nil, nil, fmt.Errorf("codeq: building codeserver: %w\n%s", err, out)
	}
	c = client.NewClient("", core.ClientInfo{Name: "codeq", Version: "0.1.0"},
		client.WithCommandTransport(bin, []string{"-root", root}))
	if err := c.Connect(ctx); err != nil {
		c.Close()
		os.RemoveAll(dir)
		return nil, nil, nil, fmt.Errorf("codeq: connecting to codeserver: %w", err)
	}
	return c, ix, func() { c.Close(); os.RemoveAll(dir) }, nil
}

// Ladder starts the tool server and returns codeq's four steps over its fifteen
// questions. The caller runs stop once the ladder has run.
func Ladder(ctx context.Context) (l eval.Ladder, stop func(), err error) {
	c, ix, stop, err := Start(ctx)
	if err != nil {
		return eval.Ladder{}, nil, err
	}
	among := ix.Rows()
	var cs []eval.SuiteCase
	for _, q := range questions {
		// Two mentions aren't wrong answers: a row the question names, as in
		// "example.com/shop/catalog is imported by ...", and the package of
		// a wanted function, as in "Format is in example.com/ledger/money".
		others := slices.DeleteFunc(slices.Clone(among), func(r string) bool {
			return strings.Contains(q.text, r) || slices.ContainsFunc(q.want, func(w string) bool { return strings.HasPrefix(w, r+".") })
		})
		cs = append(cs, eval.Single(eval.Case{Name: q.text, Input: q.text}, eval.Rows(q.want, others), eval.NoError()))
	}
	tools := agent.NewClientSource(c)
	return eval.Ladder{
		Name: "codeq",
		Steps: []eval.Step{
			{Name: "tools only", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.Tools = tools
				return cfg, nil
			}},
			{Name: "+ instructions", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.Instructions += "\n" + rules
				return cfg, nil
			}},
			{Name: "+ stuck policy", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.StepStages = append(cfg.StepStages, agent.NewStuckDetector(agent.StuckConfig{}))
				return cfg, nil
			}},
			{Name: "+ answer check", Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
				cfg.TurnEnd = append(cfg.TurnEnd, answerCheck(among))
				return cfg, nil
			}},
		},
		Cases: cs,
	}, stop, nil
}

// rowsOnly is the note answerCheck sends back with a refused answer.
const rowsOnly = "Answer with the rows alone, one row per line. Each row is an import path, or an import path, a dot and a name, such as example.com/shop/orders.Place. Write no other words."

// answerCheck refuses an answer unless every line is a row the index knows,
// once bullets and backticks are trimmed. On the last step it accepts
// whatever it gets, so a refusal can't fail a turn that had an answer.
func answerCheck(among []string) agent.TurnEndHook {
	known := map[string]bool{}
	for _, r := range among {
		known[r] = true
	}
	return func(_ context.Context, info agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
		if info.StepsLeft <= 1 {
			return agent.TurnEndVerdict{}, nil
		}
		rows := 0
		for _, line := range strings.Split(info.Text, "\n") {
			line = strings.Trim(line, " \t-*`")
			if line == "" {
				continue
			}
			if !known[line] {
				return agent.TurnEndVerdict{Continue: rowsOnly}, nil
			}
			rows++
		}
		if rows == 0 {
			return agent.TurnEndVerdict{Continue: rowsOnly}, nil
		}
		return agent.TurnEndVerdict{}, nil
	}
}

// ChatInstructions is the prompt of the ladder's last step, for running
// codeq in agentchat.
const ChatInstructions = Instructions + "\n" + rules

// ChatServer is the command that runs codeserver on the fixture, for an
// agentchat config: go run, from the examples/agents module, so agentchat
// needs no built binary.
func ChatServer() (command string, args []string, dir string, err error) {
	root, err := Fixture()
	if err != nil {
		return "", nil, "", err
	}
	return "go", []string{"run", "github.com/panyam/chakra/examples/agents/codeq/cmd/codeserver", "-root", root}, filepath.Dir(filepath.Dir(root)), nil
}
