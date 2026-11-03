package lsp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// RepoMapConfig turns on the repository map. A nil pointer on Config leaves it
// off, and off contributes nothing.
type RepoMapConfig struct {
	// TokenBudget caps the rendered map. Zero means DefaultRepoMapBudget.
	//
	// It is counted as bytes/4. That is a rough proxy, and deliberately not a
	// tokenizer. The map is prose for orientation, so being 20% out wastes a
	// little context and never affects correctness.
	TokenBudget int

	// MaxFiles caps how many files are asked about. Zero means
	// DefaultRepoMapMaxFiles.
	//
	// Every file needs a documentSymbol round trip. A server that only
	// answers for open documents also needs an open and a close per file.
	// Without this cap, a large repository would hang the index instead of
	// finishing. The map reports what the cap drops.
	MaxFiles int

	// Exclude names directories to skip, by base name. Nil means
	// DefaultRepoMapExclude.
	Exclude []string
}

// Defaults for the repository map.
const (
	DefaultRepoMapBudget   = 2000
	DefaultRepoMapMaxFiles = 400
)

// DefaultRepoMapExclude lists the usual noise of dependency trees, build
// output, and version control. It is not read from .gitignore, because
// .gitignore answers a different question. files.DefaultExclude gives the same
// reasoning.
var DefaultRepoMapExclude = []string{
	".git", "node_modules", "vendor", "target", "dist", "build",
	".venv", "venv", "__pycache__", ".next", ".cache",
}

// repoMap holds the rendered map and whether it is ready yet.
//
// It is built once in the background rather than per turn. Indexing a tree
// takes a round trip per file, and that is far too slow to run before every
// turn. A map is for orientation, not live state. A slightly stale map is
// worth much more than a fresh one that arrives after the model needed it.
type repoMap struct {
	// stop ends indexing early. Close calls it, so the background pass stops
	// asking a server that is shutting down.
	stop  context.CancelFunc
	mu    sync.Mutex
	text  string
	ready bool
}

func (m *repoMap) get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ready {
		return ""
	}
	return m.text
}

func (m *repoMap) set(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.text, m.ready = text, true
}

// fileEntry is one indexed file: where it is and what it declares.
type fileEntry struct {
	path    string
	symbols []string
	score   float64
}

// buildRepoMap indexes every root and renders the map.
//
// Definitions come from the language server, so they are accurate across
// languages without a parser per language. Edges are lexical. File A
// references file B if A mentions a name that B declares. That is an
// approximation, and it is the right one here. The alternative is a
// references request per symbol. That would take thousands of round trips to
// rank a list nobody reads past the top.
func buildRepoMap(ctx context.Context, p *pool, cfg RepoMapConfig) string {
	budget := cfg.TokenBudget
	if budget <= 0 {
		budget = DefaultRepoMapBudget
	}
	maxFiles := cfg.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultRepoMapMaxFiles
	}
	exclude := cfg.Exclude
	if exclude == nil {
		exclude = DefaultRepoMapExclude
	}

	paths, skipped := collectSourceFiles(p, exclude, maxFiles)
	if len(paths) == 0 {
		return ""
	}

	entries := make([]*fileEntry, 0, len(paths))
	for _, path := range paths {
		if ctx.Err() != nil {
			return ""
		}
		syms := declaredSymbols(ctx, p, path)
		if len(syms) == 0 {
			continue
		}
		entries = append(entries, &fileEntry{path: path, symbols: syms})
	}
	if len(entries) == 0 {
		return ""
	}

	rank(entries)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].score > entries[j].score })
	return render(entries, budget, skipped)
}

// collectSourceFiles walks every root for files a configured server handles.
func collectSourceFiles(p *pool, exclude []string, maxFiles int) (paths []string, skipped int) {
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[e] = true
	}
	for _, root := range p.roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != root && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return fs.SkipDir
				}
				return nil
			}
			if p.forPath(path) == nil {
				return nil
			}
			if len(paths) >= maxFiles {
				skipped++
				return nil
			}
			paths = append(paths, path)
			return nil
		})
	}
	return paths, skipped
}

// declaredSymbols asks the server what a file declares, flattened to names.
//
// It asks without opening the file first, and gopls answers from disk. The
// server keeps every open document type-checked and diagnosed. When the map
// opened every file it indexed, gopls was busy for the whole session. The map
// took 20s instead of 2s, and Close waited behind that work and ended up
// killing the server (#117). If a server answers nothing for a closed file,
// the file is opened for the one request and closed again.
func declaredSymbols(ctx context.Context, p *pool, path string) []string {
	c := p.forPath(path)
	if c == nil {
		return nil
	}
	syms, err := c.documentSymbols(ctx, path)
	if err != nil || len(syms) == 0 {
		syms = c.openedSymbols(ctx, path)
	}
	var out []string
	var walk func([]documentSymbol)
	walk = func(list []documentSymbol) {
		for _, s := range list {
			if name := undecorate(s.Name); name != "" {
				out = append(out, lastSegment(name))
			} else {
				out = append(out, s.Name)
			}
			walk(s.Children)
		}
	}
	walk(syms)
	return out
}

func (c *client) documentSymbols(ctx context.Context, path string) ([]documentSymbol, error) {
	var syms []documentSymbol
	err := c.conn.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(path)},
	}, &syms)
	return syms, err
}

// openedSymbols is documentSymbols for a server that needs the document open.
// A file the agent already had open is left open. A file opened here is
// closed again, so the map leaves the server no extra documents to maintain.
func (c *client) openedSymbols(ctx context.Context, path string) []documentSymbol {
	c.mu.Lock()
	_, wasOpen := c.open[path]
	c.mu.Unlock()
	if _, err := c.sync(path); err != nil {
		return nil
	}
	if !wasOpen {
		defer c.closeDocument(path)
	}
	syms, err := c.documentSymbols(ctx, path)
	if err != nil {
		return nil
	}
	return syms
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// rank scores files by PageRank over the lexical reference graph.
//
// Without a ranking this would be a listing, not a map. A tree of every file
// under a budget is list_files truncated at an arbitrary point. A new
// contributor first needs the files that the rest of the code depends on.
// Finding those is a question about the graph. File size and recency are
// cheaper signals, but they are worse proxies.
func rank(entries []*fileEntry) {
	// Which file declares a name. A name declared in several files is ambiguous
	// and gives no signal about direction, so it is dropped rather than
	// pointed at a guess.
	owner := map[string]int{}
	ambiguous := map[string]bool{}
	for i, e := range entries {
		for _, s := range e.symbols {
			if len(s) < 4 {
				// Short names collide constantly across languages (Get, New,
				// id), so they would wire nearly every file to nearly every
				// other and flatten the ranking into noise.
				continue
			}
			if _, seen := owner[s]; seen {
				ambiguous[s] = true
				continue
			}
			owner[s] = i
		}
	}

	out := make([][]int, len(entries))
	for i, e := range entries {
		body, err := os.ReadFile(e.path)
		if err != nil {
			continue
		}
		text := string(body)
		seen := map[int]bool{}
		for name, j := range owner {
			if i == j || ambiguous[name] || seen[j] {
				continue
			}
			if mentions(text, name) {
				seen[j] = true
				out[i] = append(out[i], j)
			}
		}
	}

	n := len(entries)
	score := make([]float64, n)
	next := make([]float64, n)
	for i := range score {
		score[i] = 1 / float64(n)
	}
	const damping = 0.85
	for iter := 0; iter < 20; iter++ {
		for i := range next {
			next[i] = (1 - damping) / float64(n)
		}
		for i, links := range out {
			if len(links) == 0 {
				// A file that references nothing spreads its weight evenly
				// rather than losing it, which keeps the total stable.
				share := score[i] / float64(n)
				for j := range next {
					next[j] += damping * share
				}
				continue
			}
			share := score[i] / float64(len(links))
			for _, j := range links {
				next[j] += damping * share
			}
		}
		copy(score, next)
	}
	for i, e := range entries {
		e.score = score[i]
	}
}

// mentions reports whether text contains name as a whole identifier, so Get
// does not match Getter and a substring cannot inflate a file's rank.
func mentions(text, name string) bool {
	from := 0
	for {
		i := strings.Index(text[from:], name)
		if i < 0 {
			return false
		}
		i += from
		before := byte(' ')
		if i > 0 {
			before = text[i-1]
		}
		after := byte(' ')
		if end := i + len(name); end < len(text) {
			after = text[end]
		}
		if !identByte(before) && !identByte(after) {
			return true
		}
		from = i + len(name)
	}
}

func identByte(b byte) bool {
	r := rune(b)
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// render writes the ranked map within the budget and says what it left out.
//
// Imagine a capped listing that doesn't say it was capped. It reads as the
// complete contents of the repository. list_files and search_files already
// refuse to make that mistake.
func render(entries []*fileEntry, budget, skipped int) string {
	var b strings.Builder
	b.WriteString("## Repository map\n\n")
	b.WriteString("The files the rest of the code leans on most, and what they declare.\n")
	b.WriteString("Ranked, not complete: use list_files and search_files for the rest.\n\n")

	shown := 0
	for _, e := range entries {
		line := fmt.Sprintf("%s\n  %s\n", e.path, strings.Join(capSymbols(e.symbols, 12), ", "))
		if (b.Len()+len(line))/4 > budget {
			break
		}
		b.WriteString(line)
		shown++
	}
	if shown == 0 {
		return ""
	}
	if rest := len(entries) - shown; rest > 0 {
		fmt.Fprintf(&b, "\n%d more file(s) not shown, ranked lower.\n", rest)
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "%d file(s) were never indexed: the scan stopped at its file cap.\n", skipped)
	}
	return b.String()
}

func capSymbols(syms []string, max int) []string {
	if len(syms) <= max {
		return syms
	}
	out := append([]string{}, syms[:max]...)
	return append(out, fmt.Sprintf("and %d more", len(syms)-max))
}

// startRepoMap indexes in the background and reports the map when it is ready.
//
// The context is detached from construction on purpose. Indexing outlives
// New. Tying it to the caller's context would cancel the map the moment a
// startup context was released.
func startRepoMap(p *pool, cfg RepoMapConfig) *repoMap {
	ctx, cancel := context.WithTimeout(context.Background(), repoMapTimeout)
	m := &repoMap{stop: cancel}
	go func() {
		defer cancel()
		m.set(buildRepoMap(ctx, p, cfg))
	}()
	return m
}

// repoMapTimeout bounds the indexing pass. A tree too large to index inside it
// yields no map rather than an endless background scan.
const repoMapTimeout = 5 * time.Minute

// writeFile is a thin helper so tests can build a tree without importing the
// file tools. Constraint C4 forbids that import, and
// scripts/check-ext-isolation.sh enforces it.
func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }
