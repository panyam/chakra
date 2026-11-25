// Package codefix is a coding agent that edits a Go module until its tests
// pass (#352). Each case copies testdata/base, a small shop module, into a
// temporary directory and brings in one bug. The agent gets a bug report,
// and the case passes when `go test ./...` passes and no _test.go file
// changed.
package codefix

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
	"github.com/panyam/chakra/ext/exec"
	"github.com/panyam/chakra/ext/files"
	"github.com/panyam/mcpkit/core"
)

// Instructions is the agent's system prompt on every step.
const Instructions = "You fix bugs in a Go module. Read the code, edit it, and finish with one line saying what you changed. Never edit a _test.go file."

// workspace is the module the current case works in. Cases run one at a
// time, so the tools, the repo map, the plan slot and the test gate all
// read it, and each case's Setup points it at a fresh copy.
type workspace struct {
	mu    sync.Mutex
	dir   string
	slots *agent.Slots
}

func (w *workspace) get() (string, *agent.Slots) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dir, w.slots
}

// kit is the tools a step offers, built for whichever module the workspace
// holds. A step sets which ones are on; the case decides where they point.
type kit struct {
	ws    *workspace
	tests bool
	plan  bool

	mu    sync.Mutex
	dir   string
	built agent.ToolSource
}

// errNoWorkspace is a tool call made before any case set up its copy.
var errNoWorkspace = fmt.Errorf("codefix: no workspace yet: a case's Setup makes one")

func (k *kit) source() (agent.ToolSource, error) {
	dir, slots := k.ws.get()
	if dir == "" {
		return nil, errNoWorkspace
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.built != nil && k.dir == dir {
		return k.built, nil
	}
	return k.build(dir, slots)
}

// build makes the tools for the module in dir.
func (k *kit) build(dir string, slots *agent.Slots) (agent.ToolSource, error) {
	m := agent.NewMultiSource()
	fsrc, err := files.NewSource(files.Config{Roots: []string{dir}})
	if err != nil {
		return nil, err
	}
	if err := m.Add("files", fsrc); err != nil {
		return nil, err
	}
	if k.tests {
		// The one command is go test ./... with no arguments, in a
		// temporary copy, so it runs unconfined rather than needing each
		// platform's sandbox to allow the Go build cache.
		ext, err := exec.New(exec.Config{
			Roots:    []string{dir},
			Commands: []exec.CommandSpec{{Name: "tests", Argv: []string{"go", "test", "./..."}, Dir: dir, Description: "Run go test ./... in the module and show the output.", ReadOnly: true}},
			Sandbox:  exec.Unconfined(),
			Timeout:  2 * time.Minute,
		})
		if err != nil {
			return nil, err
		}
		src, err := ext.Tools()
		if err != nil {
			return nil, err
		}
		if err := m.Add("exec", src); err != nil {
			return nil, err
		}
	}
	if k.plan && slots != nil {
		if err := m.Add("slots", slots.Tool("plan")); err != nil {
			return nil, err
		}
	}
	k.dir, k.built = dir, m
	return m, nil
}

// Tools lists the step's tools. Before a case runs it lists them against
// the clean module, so a walk can say what a step offers. Call never falls
// back like that, so nothing can edit the fixture.
func (k *kit) Tools(ctx context.Context) ([]core.ToolDef, error) {
	src, err := k.source()
	if err == errNoWorkspace {
		dir, berr := base()
		if berr != nil {
			return nil, err
		}
		slots, _ := agent.NewSlots(agent.SlotSpec{Name: "plan"})
		src, err = (&kit{tests: k.tests, plan: k.plan}).build(dir, slots)
	}
	if err != nil {
		return nil, err
	}
	return src.Tools(ctx)
}

func (k *kit) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	src, err := k.source()
	if err != nil {
		return nil, err
	}
	return src.Call(ctx, name, args)
}

// Ladder is codefix's five steps over its five tasks.
func Ladder(context.Context) (eval.Ladder, func(), error) {
	ws := &workspace{}
	parent, err := os.MkdirTemp("", "codefix")
	if err != nil {
		return eval.Ladder{}, nil, err
	}
	var cs []eval.SuiteCase
	for i, t := range tasks {
		t := t
		cs = append(cs, eval.SuiteCase{
			Scenario: eval.Scenario{Name: t.report, Turns: []string{t.prompt()}},
			Setup: func(context.Context) (eval.Setup, error) {
				dir, err := t.workspace(filepath.Join(parent, fmt.Sprintf("%d-%s-%d", i, t.name, time.Now().UnixNano())))
				if err != nil {
					return eval.Setup{}, err
				}
				slots, err := agent.NewSlots(agent.SlotSpec{Name: "plan", Description: "Your plan for this fix: what is wrong, and every change still to make. Tick items off as you go."})
				if err != nil {
					return eval.Setup{}, err
				}
				ws.mu.Lock()
				ws.dir, ws.slots = dir, slots
				ws.mu.Unlock()
				return eval.Setup{}, nil
			},
			Scorers: func(agent.Provider) []eval.Scorer { return []eval.Scorer{testsPass(ws)} },
		})
	}
	tools := func(c agent.RunnerConfig, tests, plan bool) agent.RunnerConfig {
		c.Tools = &kit{ws: ws, tests: tests, plan: plan}
		return c
	}
	l := eval.Ladder{
		Name: "codefix",
		Steps: []eval.Step{
			{Name: "read + edit", Configure: func(c agent.RunnerConfig) (agent.RunnerConfig, error) {
				return tools(c, false, false), nil
			}},
			{Name: "+ run tests", Configure: func(c agent.RunnerConfig) (agent.RunnerConfig, error) {
				return tools(c, true, false), nil
			}},
			{Name: "+ repo map", Configure: func(c agent.RunnerConfig) (agent.RunnerConfig, error) {
				c.StepStages = append(slices.Clip(c.StepStages), agent.NewRetrievalStage(agent.RetrievalConfig{
					Retrievers: []agent.Retriever{agent.NewRetriever("repo map", func(context.Context, agent.RetrievalQuery) ([]agent.Snippet, error) {
						dir, _ := ws.get()
						m, err := repoMap(dir)
						if err != nil {
							return nil, err
						}
						return []agent.Snippet{{Source: "repo map", Text: m, Score: 1}}, nil
					})},
				}))
				return c, nil
			}},
			{Name: "+ plan slot", Configure: func(c agent.RunnerConfig) (agent.RunnerConfig, error) {
				c = tools(c, true, true)
				c.StepStages = append(slices.Clip(c.StepStages), agent.StepStage{
					Name:  "codefix.plan",
					Scope: agent.ScopeRequest,
					Run: func(ctx context.Context, info agent.StepInfo, view agent.StepView) (agent.StepView, error) {
						if _, slots := ws.get(); slots != nil {
							return slots.StepStage().Run(ctx, info, view)
						}
						return view, nil
					},
				})
				return c, nil
			}},
			{Name: "+ test gate", Configure: func(c agent.RunnerConfig) (agent.RunnerConfig, error) {
				c.TurnEnd = append(slices.Clip(c.TurnEnd), testGate(ws))
				return c, nil
			}},
		},
		Cases: cs,
	}
	return l, func() { os.RemoveAll(parent) }, nil
}

// testsPass grades a case: go test ./... passes in the workspace, and no
// test file changed.
func testsPass(ws *workspace) eval.Scorer {
	return scorer(func(eval.Result) eval.Score {
		dir, _ := ws.get()
		score := eval.Score{Name: "Tests", Want: "go test ./... passes, tests unchanged"}
		if same, err := testsUnchanged(dir); err != nil || !same {
			score.Detail = "a _test.go file changed"
			return score
		}
		out, ok := runTests(dir)
		score.Pass = ok
		if ok {
			score.Value = 1
			score.Detail = "go test ./... passes"
			return score
		}
		score.Detail = "go test ./... fails: " + firstFailure(out)
		return score
	})
}

type scorer func(eval.Result) eval.Score

func (s scorer) Score(r eval.Result) eval.Score { return s(r) }

// testGate refuses an answer while the tests fail, with their output, and
// sends the model back. On the last step it accepts, so a refusal can't
// fail a turn on its own.
func testGate(ws *workspace) agent.TurnEndHook {
	return func(_ context.Context, info agent.TurnEndInfo) (agent.TurnEndVerdict, error) {
		if info.StepsLeft <= 1 {
			return agent.TurnEndVerdict{}, nil
		}
		dir, _ := ws.get()
		out, ok := runTests(dir)
		if ok {
			return agent.TurnEndVerdict{}, nil
		}
		return agent.TurnEndVerdict{Continue: "The tests still fail. Fix the code and try again:\n" + tail(out, 15)}, nil
	}
}

// firstFailure is the first line of go test's output that says why.
func firstFailure(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "_test.go:") || strings.HasPrefix(line, "panic:") {
			return line
		}
	}
	return tail(out, 1)
}

func tail(out string, n int) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// repoMap lists each non-test file in the module with what it declares and
// the functions of other packages it calls, one line per file.
func repoMap(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("codefix: no workspace yet")
	}
	var lines []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		var decls, calls []string
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := star.X.(*ast.Ident); ok {
						name = "(*" + id.Name + ")." + name
					}
				}
			}
			decls = append(decls, name)
			ast.Inspect(fn, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name != "fmt" && isImport(f, x.Name) {
						call := x.Name + "." + sel.Sel.Name
						if !slices.Contains(calls, call) {
							calls = append(calls, call)
						}
					}
				}
				return true
			})
		}
		rel, _ := filepath.Rel(dir, path)
		line := rel + ": " + strings.Join(decls, ", ")
		if len(calls) > 0 {
			line += "; calls " + strings.Join(calls, ", ")
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		return "", err
	}
	return "Files in the module, with what each declares and calls:\n" + strings.Join(lines, "\n"), nil
}

func isImport(f *ast.File, name string) bool {
	for _, im := range f.Imports {
		p := strings.Trim(im.Path.Value, `"`)
		if (im.Name != nil && im.Name.Name == name) || p[strings.LastIndex(p, "/")+1:] == name {
			return true
		}
	}
	return false
}
