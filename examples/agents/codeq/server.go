package codeq

import (
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// NewServer returns an MCP server with five read-only tools over ix:
// packages, imports, importers, find and callers. Each answers one row per
// line. An import that only _test.go files make ends in " (test files
// only)".
func NewServer(ix *Index) *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "codeq", Version: "0.1.0"})
	srv.Register(core.TextTool[moduleArg]("packages", "List the packages of a Go module.",
		func(_ core.ToolContext, in moduleArg) (string, error) {
			rows := ix.PackagesIn(in.Module)
			if len(rows) == 0 {
				return "", fmt.Errorf("no module %q", in.Module)
			}
			return strings.Join(rows, "\n"), nil
		}))
	srv.Register(core.TextTool[pkgArg]("imports", "List the packages one package imports.",
		func(_ core.ToolContext, in pkgArg) (string, error) {
			edges, err := ix.ImportsOf(in.Package)
			if err != nil {
				return "", err
			}
			return edgeRows(edges), nil
		}))
	srv.Register(core.TextTool[pkgArg]("importers", "List the packages, from every module, that import one package.",
		func(_ core.ToolContext, in pkgArg) (string, error) {
			return edgeRows(ix.ImportersOf(in.Package)), nil
		}))
	srv.Register(core.TextTool[nameArg]("find", "Find where a function or type is declared, with its qualified name.",
		func(_ core.ToolContext, in nameArg) (string, error) {
			var rows []string
			for _, d := range ix.Find(in.Name) {
				rows = append(rows, d[0]+"  declared at "+d[1])
			}
			if len(rows) == 0 {
				return "nothing is declared as " + in.Name, nil
			}
			return strings.Join(rows, "\n"), nil
		}))
	srv.Register(core.TextTool[funcArg]("callers", "List the functions that call one function, from every module.",
		func(_ core.ToolContext, in funcArg) (string, error) {
			rows, err := ix.CallersOf(in.Function)
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				return "nothing calls " + in.Function, nil
			}
			return strings.Join(rows, "\n"), nil
		}))
	return srv
}

type pkgArg struct {
	Package string `json:"package" jsonschema:"an import path, such as example.com/ledger/money"`
}

func edgeRows(edges []Edge) string {
	if len(edges) == 0 {
		return "none"
	}
	var rows []string
	for _, e := range edges {
		r := e.Path
		if e.TestOnly {
			r += " (test files only)"
		}
		rows = append(rows, r)
	}
	return strings.Join(rows, "\n")
}

type moduleArg struct {
	Module string `json:"module" jsonschema:"a module path, such as example.com/shop"`
}

type nameArg struct {
	Name string `json:"name" jsonschema:"a function or type name, bare (Save) or qualified"`
}

type funcArg struct {
	Function string `json:"function" jsonschema:"a qualified function name, such as example.com/shop/internal/db.Save"`
}
