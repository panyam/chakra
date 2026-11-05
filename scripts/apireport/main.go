// Command apireport prints a module's exported API, one line per symbol, in
// the shape of Go's own api/*.txt files. `make api-report` diffs its output
// against each module's checked-in api.txt, and `make api-update` rewrites
// them (#143).
//
// Usage: go run ./scripts/apireport DIR
//
// It reads the AST rather than type-checking, so it needs no build and gives
// the same output on every OS. Files are read whatever their build tags, and
// identical lines are merged. A symbol declared per platform with two
// different signatures shows both. Test files, main packages, internal
// packages, testdata and nested modules are left out. Doc comments, parameter
// names and constant values are left out too, so editing them doesn't change
// the report.
//
// An embedded field is listed as itself ("embedded agent.Base"). Methods it
// promotes are not listed, since that needs type-checking across modules. A
// change to them shows in the embedded type's own module.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: apireport DIR")
		os.Exit(2)
	}
	lines, err := moduleAPI(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "apireport:", err)
		os.Exit(1)
	}
	w := bufio.NewWriter(os.Stdout)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "apireport:", err)
		os.Exit(1)
	}
}

// moduleAPI returns the sorted, deduplicated API lines of the module rooted
// at dir.
func moduleAPI(dir string) ([]string, error) {
	modPath, err := modulePath(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	files := map[string][]string{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == dir {
				return nil
			}
			name := d.Name()
			if name == "testdata" || name == "vendor" || name == "internal" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			files[filepath.Dir(p)] = append(files[filepath.Dir(p)], p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for pkgDir, paths := range files {
		rel, err := filepath.Rel(dir, pkgDir)
		if err != nil {
			return nil, err
		}
		importPath := modPath
		if rel != "." {
			importPath = path.Join(modPath, filepath.ToSlash(rel))
		}
		lines, err := packageAPI(importPath, paths)
		if err != nil {
			return nil, err
		}
		for _, l := range lines {
			seen[l] = true
		}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	slices.Sort(out)
	return out, nil
}

func modulePath(gomod string) (string, error) {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`), nil
		}
	}
	return "", fmt.Errorf("%s: no module line", gomod)
}

// packageAPI lists the exported API of one directory's files. A main package
// has no importable API, so it yields nothing.
func packageAPI(importPath string, paths []string) ([]string, error) {
	fset := token.NewFileSet()
	var lines []string
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		if f.Name.Name == "main" {
			continue
		}
		e := emitter{fset: fset, prefix: "pkg " + importPath + ", "}
		for _, d := range f.Decls {
			e.decl(d)
		}
		lines = append(lines, e.lines...)
	}
	return lines, nil
}

type emitter struct {
	fset   *token.FileSet
	prefix string
	lines  []string
}

func (e *emitter) add(format string, args ...any) {
	e.lines = append(e.lines, e.prefix+fmt.Sprintf(format, args...))
}

func (e *emitter) decl(d ast.Decl) {
	switch d := d.(type) {
	case *ast.FuncDecl:
		e.funcDecl(d)
	case *ast.GenDecl:
		e.genDecl(d)
	}
}

func (e *emitter) funcDecl(d *ast.FuncDecl) {
	if !d.Name.IsExported() {
		return
	}
	sig := e.signature(d.Type)
	if d.Recv == nil {
		e.add("func %s%s%s", d.Name.Name, e.typeParams(d.Type.TypeParams), sig)
		return
	}
	recv := d.Recv.List[0].Type
	if !ast.IsExported(baseName(recv)) {
		return
	}
	e.add("method (%s) %s%s", e.expr(recv), d.Name.Name, sig)
}

func (e *emitter) genDecl(d *ast.GenDecl) {
	// Within a const group, a spec with no type repeats the previous one's
	// (the iota pattern), so the type carries forward.
	var lastType ast.Expr
	for _, s := range d.Specs {
		switch s := s.(type) {
		case *ast.TypeSpec:
			e.typeSpec(s)
		case *ast.ValueSpec:
			kind := "var"
			if d.Tok == token.CONST {
				kind = "const"
				if s.Type != nil || len(s.Values) > 0 {
					lastType = s.Type
				}
			} else {
				lastType = s.Type
			}
			for _, n := range s.Names {
				if !n.IsExported() {
					continue
				}
				if lastType != nil {
					e.add("%s %s %s", kind, n.Name, e.expr(lastType))
				} else {
					e.add("%s %s", kind, n.Name)
				}
			}
		}
	}
}

func (e *emitter) typeSpec(s *ast.TypeSpec) {
	if !s.Name.IsExported() {
		return
	}
	name := s.Name.Name + e.typeParams(s.TypeParams)
	if s.Assign.IsValid() {
		e.add("type %s = %s", name, e.expr(s.Type))
		return
	}
	switch t := s.Type.(type) {
	case *ast.StructType:
		e.add("type %s struct", name)
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 {
				if ast.IsExported(baseName(f.Type)) {
					e.add("type %s struct, embedded %s", s.Name.Name, e.expr(f.Type))
				}
				continue
			}
			for _, n := range f.Names {
				if n.IsExported() {
					e.add("type %s struct, %s %s", s.Name.Name, n.Name, e.expr(f.Type))
				}
			}
		}
	case *ast.InterfaceType:
		e.add("type %s interface", name)
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				e.add("type %s interface, embedded %s", s.Name.Name, e.expr(m.Type))
				continue
			}
			for _, n := range m.Names {
				if !n.IsExported() {
					e.add("type %s interface, unexported methods", s.Name.Name)
					continue
				}
				e.add("type %s interface, %s%s", s.Name.Name, n.Name, e.signature(m.Type.(*ast.FuncType)))
			}
		}
	default:
		e.add("type %s %s", name, e.expr(s.Type))
	}
}

// signature prints a func type's parameters and results by type alone, so
// renaming a parameter doesn't change the report.
func (e *emitter) signature(ft *ast.FuncType) string {
	s := "(" + e.types(ft.Params) + ")"
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return s
	}
	res := e.types(ft.Results)
	if len(ft.Results.List) == 1 && len(ft.Results.List[0].Names) <= 1 {
		return s + " " + res
	}
	return s + " (" + res + ")"
}

func (e *emitter) types(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		t := e.expr(f.Type)
		for range max(len(f.Names), 1) {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, ", ")
}

func (e *emitter) typeParams(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		names := make([]string, len(f.Names))
		for i, n := range f.Names {
			names[i] = n.Name
		}
		parts = append(parts, strings.Join(names, ", ")+" "+e.expr(f.Type))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// expr prints a type expression. A func type is printed by signature, so its
// parameter names don't show, and a struct or interface literal on one line.
func (e *emitter) expr(x ast.Expr) string {
	if ft, ok := x.(*ast.FuncType); ok {
		return "func" + e.signature(ft)
	}
	var b bytes.Buffer
	if err := printer.Fprint(&b, e.fset, x); err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// baseName is the type name under pointers, generic instantiations and
// package qualifiers: Foo for *Foo, pkg.Foo or Foo[T].
func baseName(x ast.Expr) string {
	for {
		switch t := x.(type) {
		case *ast.StarExpr:
			x = t.X
		case *ast.IndexExpr:
			x = t.X
		case *ast.IndexListExpr:
			x = t.X
		case *ast.SelectorExpr:
			return t.Sel.Name
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}
