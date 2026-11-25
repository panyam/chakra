package codefix

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// edit replaces old with new in one file of the module.
type edit struct {
	file, old, new string
}

// task is one case: a bug report, and the edits that bring the bug into
// the correct module in testdata/base. Undoing them is the fix.
type task struct {
	name   string
	report string
	bug    []edit
}

var tasks = []task{
	{
		name:   "sum",
		report: "Cart totals come out low. money.Sum seems to miss an item.",
		bug:    []edit{{"money/money.go", "for i := 0; i < len(xs); i++", "for i := 1; i < len(xs); i++"}},
	},
	{
		name:   "threshold",
		report: "An order of exactly $10.00 gets no discount, and it should.",
		bug:    []edit{{"discount/discount.go", "if total >= Threshold {", "if total > Threshold {"}},
	},
	{
		name:   "nilcart",
		report: "A test in the cart package panics.",
		bug: []edit{{"cart/cart.go", `func (c *Cart) Total() int {
	if c == nil {
		return 0
	}
	return money.Sum(c.Prices)`, `func (c *Cart) Total() int {
	return money.Sum(c.Prices)`}},
	},
	{
		name:   "priciest",
		report: "The report's summary line shows the wrong priciest item.",
		bug:    []edit{{"money/money.go", "if x > best {", "if x < best {"}},
	},
	{
		name:   "twobugs",
		report: "Two things are off in the summary line: discounts round down instead of to the nearest cent, and cents under ten lose their leading zero.",
		bug: []edit{
			{"discount/discount.go", "return total - (total*pct+50)/100", "return total - total*pct/100"},
			{"report/report.go", `"$%d.%02d"`, `"$%d.%d"`},
		},
	},
}

// prompt is what the agent is asked for a task.
func (t task) prompt() string {
	return t.report + " Fix the code so `go test ./...` passes. Don't change any _test.go file."
}

// base is the correct module.
func base() (string, error) {
	out, err := exec.Command("go", "list", "-f", "{{.Dir}}", "github.com/panyam/chakra/examples/agents/codefix").Output()
	if err != nil {
		return "", fmt.Errorf("codefix: finding the fixture: %w", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "testdata", "base"), nil
}

// workspace copies the module into a new directory under parent and brings
// in t's bug. It returns the module's directory.
func (t task) workspace(parent string) (string, error) {
	src, err := base()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, "shopkit")
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		to := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, raw, 0o644)
	})
	if err != nil {
		return "", err
	}
	for _, e := range t.bug {
		if err := replace(filepath.Join(dir, e.file), e.old, e.new); err != nil {
			return "", fmt.Errorf("codefix: task %s: %w", t.name, err)
		}
	}
	return dir, nil
}

// fix undoes t's bug in dir, as a right answer would.
func (t task) fix(dir string) error {
	for _, e := range t.bug {
		if err := replace(filepath.Join(dir, e.file), e.new, e.old); err != nil {
			return err
		}
	}
	return nil
}

// replace swaps the one occurrence of old in path for new.
func replace(path, old, new string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if n := bytes.Count(raw, []byte(old)); n != 1 {
		return fmt.Errorf("%s has %q %d times, want once", path, old, n)
	}
	return os.WriteFile(path, bytes.Replace(raw, []byte(old), []byte(new), 1), 0o644)
}

// runTests runs go test ./... in dir and returns its output and whether it
// passed.
func runTests(dir string) (string, bool) {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// testsUnchanged reports whether every _test.go file in dir matches the
// correct module's.
func testsUnchanged(dir string) (bool, error) {
	src, err := base()
	if err != nil {
		return false, err
	}
	same := true
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		want, _ := os.ReadFile(path)
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || !bytes.Equal(got, want) {
			same = false
		}
		return nil
	})
	return same, err
}
