package codeq

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Index is what the tool server knows about the Go modules under one root.
// It reads source with go/parser alone, so it resolves a call by the
// package name before the dot, not by type. Every row it returns is an
// import path, or an import path, a dot and a name.
type Index struct {
	// Packages maps an import path to its package.
	Packages map[string]*Package
	// Defs maps a qualified name, such as example.com/ledger/money.Add, to
	// where it is declared.
	Defs map[string]string
	// Calls maps a qualified function to the qualified functions it calls.
	Calls map[string][]string
}

// Package is one package in the index.
type Package struct {
	Path   string
	Module string
	// Imports maps an imported path to true when only _test.go files
	// import it.
	Imports map[string]bool
}

// Load indexes every module under root. A module is a directory with a
// go.mod, and its packages are the directories under it with .go files.
func Load(root string) (*Index, error) {
	ix := &Index{Packages: map[string]*Package{}, Defs: map[string]string{}, Calls: map[string][]string{}}
	mods, err := filepath.Glob(filepath.Join(root, "*", "go.mod"))
	if err != nil {
		return nil, err
	}
	for _, gomod := range mods {
		raw, err := os.ReadFile(gomod)
		if err != nil {
			return nil, err
		}
		module := modulePath(raw)
		if module == "" {
			return nil, fmt.Errorf("codeq: %s: no module line", gomod)
		}
		dir := filepath.Dir(gomod)
		err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			pkg := module
			if rel != "." {
				pkg += "/" + filepath.ToSlash(rel)
			}
			return ix.addPackage(path, pkg, module)
		})
		if err != nil {
			return nil, err
		}
	}
	return ix, nil
}

// modulePath reads the module line of a go.mod.
func modulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			return f[1]
		}
	}
	return ""
}

func (ix *Index) addPackage(dir, path, module string) error {
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	if len(files) == 0 {
		return nil
	}
	p := &Package{Path: path, Module: module, Imports: map[string]bool{}}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		test := strings.HasSuffix(f, "_test.go")
		names := map[string]string{}
		for _, im := range file.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			if testOnly, seen := p.Imports[ip]; !seen || testOnly {
				p.Imports[ip] = test
			}
			name := ip[strings.LastIndex(ip, "/")+1:]
			if im.Name != nil {
				name = im.Name.Name
			}
			names[name] = ip
		}
		if test {
			continue
		}
		for _, decl := range file.Decls {
			ix.addDecl(fset, decl, path, names)
		}
	}
	ix.Packages[path] = p
	return nil
}

func (ix *Index) addDecl(fset *token.FileSet, decl ast.Decl, pkg string, imports map[string]string) {
	where := func(n ast.Node) string {
		pos := fset.Position(n.Pos())
		return fmt.Sprintf("%s/%s:%d", pkg, filepath.Base(pos.Filename), pos.Line)
	}
	switch d := decl.(type) {
	case *ast.GenDecl:
		for _, s := range d.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok {
				ix.Defs[pkg+"."+ts.Name.Name] = where(ts)
			}
		}
	case *ast.FuncDecl:
		if d.Recv != nil {
			return
		}
		fn := pkg + "." + d.Name.Name
		ix.Defs[fn] = where(d)
		if d.Body == nil {
			return
		}
		ast.Inspect(d.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var callee string
			switch f := call.Fun.(type) {
			case *ast.Ident:
				callee = pkg + "." + f.Name
			case *ast.SelectorExpr:
				if x, ok := f.X.(*ast.Ident); ok && imports[x.Name] != "" {
					callee = imports[x.Name] + "." + f.Sel.Name
				}
			}
			if callee != "" && !slices.Contains(ix.Calls[fn], callee) {
				ix.Calls[fn] = append(ix.Calls[fn], callee)
			}
			return true
		})
	}
}

// Rows lists every package and every declared name, sorted. A scorer uses
// it to spot an answer naming a row it shouldn't.
func (ix *Index) Rows() []string {
	var rows []string
	for p := range ix.Packages {
		rows = append(rows, p)
	}
	for d := range ix.Defs {
		rows = append(rows, d)
	}
	slices.Sort(rows)
	return rows
}

// PackagesIn lists the packages of module, sorted.
func (ix *Index) PackagesIn(module string) []string {
	var out []string
	for _, p := range ix.Packages {
		if p.Module == module {
			out = append(out, p.Path)
		}
	}
	slices.Sort(out)
	return out
}

// ImportsOf lists what pkg imports, each with whether only tests import it.
func (ix *Index) ImportsOf(pkg string) ([]Edge, error) {
	p, ok := ix.Packages[pkg]
	if !ok {
		return nil, fmt.Errorf("no package %q; packages lists a module's packages", pkg)
	}
	var out []Edge
	for ip, test := range p.Imports {
		out = append(out, Edge{Path: ip, TestOnly: test})
	}
	slices.SortFunc(out, func(a, b Edge) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// ImportersOf lists every indexed package that imports pkg, from any
// module.
func (ix *Index) ImportersOf(pkg string) []Edge {
	var out []Edge
	for _, p := range ix.Packages {
		if test, ok := p.Imports[pkg]; ok {
			out = append(out, Edge{Path: p.Path, TestOnly: test})
		}
	}
	slices.SortFunc(out, func(a, b Edge) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// Edge is one import between packages.
type Edge struct {
	Path     string
	TestOnly bool
}

// Find lists the qualified names whose last part is name, or that equal
// it, with where each is declared.
func (ix *Index) Find(name string) [][2]string {
	var out [][2]string
	for q, where := range ix.Defs {
		if q == name || strings.HasSuffix(q, "."+name) {
			out = append(out, [2]string{q, where})
		}
	}
	slices.SortFunc(out, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	return out
}

// CallersOf lists the functions that call fn. fn has to be qualified, since
// a bare name such as Save could be any package's.
func (ix *Index) CallersOf(fn string) ([]string, error) {
	if !strings.Contains(fn, "/") {
		return nil, fmt.Errorf("callers needs a qualified name, such as example.com/shop/internal/db.Save, and %q isn't one", fn)
	}
	var out []string
	for caller, callees := range ix.Calls {
		if slices.Contains(callees, fn) {
			out = append(out, caller)
		}
	}
	slices.Sort(out)
	return out, nil
}
