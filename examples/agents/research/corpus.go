package research

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/panyam/chakra"
)

// pages holds the snapshots: Wikipedia and Wikivoyage pages as plain text,
// and older Wikipedia revisions. SOURCES.md lists each with its licence.
//
//go:embed testdata/pages
var pages embed.FS

// page is one snapshot. A file holds a header of "key: value" lines, a line
// "---", then the text.
type page struct {
	url    string
	title  string
	source string
	text   string
	words  map[string]int
}

// corpus is every page, by URL, with what search needs to rank them.
type corpus struct {
	pages []*page
	byURL map[string]*page
	// df counts the pages each word appears in.
	df map[string]int
}

// The sources a page can come from. A sub-agent researches one of them.
const (
	wikipedia  = "wikipedia"
	wikivoyage = "wikivoyage"
	archive    = "archive"
)

var sources = []string{wikipedia, wikivoyage, archive}

func loadCorpus() (*corpus, error) {
	c := &corpus{byURL: map[string]*page{}, df: map[string]int{}}
	err := fs.WalkDir(pages, "testdata/pages", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".txt") {
			return err
		}
		b, err := pages.ReadFile(path)
		if err != nil {
			return err
		}
		p, err := parsePage(string(b))
		if err != nil {
			return fmt.Errorf("research: %s: %w", path, err)
		}
		if c.byURL[canonical(p.url)] != nil {
			return fmt.Errorf("research: %s: a second page for %s", path, p.url)
		}
		c.pages = append(c.pages, p)
		c.byURL[canonical(p.url)] = p
		for w := range p.words {
			c.df[w]++
		}
		return nil
	})
	return c, err
}

// canonical is the form a URL is looked up by, so a URL a model writes
// with %28 for a parenthesis, http for https, or a space for an underscore
// still finds its page.
func canonical(u string) string {
	u = strings.TrimSpace(u)
	if v, err := url.PathUnescape(u); err == nil {
		u = v
	}
	u = strings.Replace(u, "http://", "https://", 1)
	u = strings.ReplaceAll(u, " ", "_")
	return strings.TrimSuffix(u, "/")
}

// page is the page at u, or nil.
func (c *corpus) page(u string) *page { return c.byURL[canonical(u)] }

func parsePage(s string) (*page, error) {
	head, text, ok := strings.Cut(s, "\n---\n")
	if !ok {
		return nil, fmt.Errorf("no --- line after the header")
	}
	p := &page{text: strings.TrimSpace(text)}
	for _, line := range strings.Split(head, "\n") {
		k, v, _ := strings.Cut(line, ": ")
		switch k {
		case "url":
			p.url = v
		case "title":
			p.title = v
		case "source":
			p.source = v
		}
	}
	if p.url == "" || p.title == "" || !slices.Contains(sources, p.source) {
		return nil, fmt.Errorf("the header needs url, title and a source from %v", sources)
	}
	p.words = map[string]int{}
	for _, w := range tokens(p.title + " " + p.title + " " + p.title + " " + p.text) {
		p.words[w]++
	}
	return p, nil
}

var wordRE = regexp.MustCompile(`[\p{L}\p{N}]+`)

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields("a an and are as at be by did do does for from has have how in is it its of on or the this to was were what when where which who whom why with") {
		stop[w] = true
	}
}

func tokens(s string) []string {
	var out []string
	for _, w := range wordRE.FindAllString(strings.ToLower(s), -1) {
		if !stop[w] {
			out = append(out, w)
		}
	}
	return out
}

// hit is one search result.
type hit struct {
	p     *page
	score float64
}

// search ranks the pages by the query's words, each weighted by how rare it
// is, and returns the best n. source, when not empty, keeps only that
// source's pages.
func (c *corpus) search(query, source string, n int) []hit {
	q := tokens(query)
	var hits []hit
	for _, p := range c.pages {
		if source != "" && p.source != source {
			continue
		}
		s := 0.0
		for _, w := range q {
			if tf := p.words[w]; tf > 0 {
				s += math.Log(1+float64(tf)) * math.Log(1+float64(len(c.pages))/float64(c.df[w]))
			}
		}
		if s > 0 {
			hits = append(hits, hit{p, s})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return strings.Compare(a.p.url, b.p.url)
	})
	return hits[:min(n, len(hits))]
}

type searchArgs struct {
	Query  string `json:"query" jsonschema:"Words to look for."`
	Source string `json:"source,omitempty" jsonschema:"Keep only pages from one source: wikipedia, wikivoyage or archive. Empty searches them all."`
}

type fetchArgs struct {
	URL    string `json:"url" jsonschema:"The URL of a page, as search returned it."`
	Offset int    `json:"offset,omitempty" jsonschema:"Where to start reading, in characters. Zero is the top of the page."`
}

// pageChunk is how much of a page one fetch returns, in characters.
const pageChunk = 2000

// chunk is the part of text from offset, about pageChunk characters long,
// ending at a line or word break, and where the next part starts. next is
// zero after the last part.
func chunk(text string, offset int) (part string, next int) {
	offset = max(0, min(offset, len(text)))
	end := offset + pageChunk
	if end >= len(text) {
		return text[offset:], 0
	}
	if i := strings.LastIndexAny(text[offset:end], "\n "); i > pageChunk/2 {
		end = offset + i + 1
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	return text[offset:end], end
}

// tools serves search and fetch over c. source, when not empty, fixes
// search to that source and fetch to its pages, for a sub-agent that
// researches one source. search returns titles and URLs alone, and fetch
// returns a page 2000 characters at a time, as a search engine and a long
// web page would.
func (c *corpus) tools(source string) (*agent.FuncSource, error) {
	s := agent.NewFuncSource()
	desc := "Search the pages and return up to five results, each a title, a URL and its source."
	if source != "" {
		desc = "Search the " + source + " pages and return up to five results, each a title and a URL."
	}
	err := agent.AddFunc(s, "search", desc, func(_ context.Context, in searchArgs) (string, error) {
		src := in.Source
		if source != "" {
			src = source
		}
		if src != "" && !slices.Contains(sources, src) {
			return "", fmt.Errorf("no source %q: use wikipedia, wikivoyage or archive", src)
		}
		hits := c.search(in.Query, src, 5)
		if len(hits) == 0 {
			return "No pages match.", nil
		}
		var b strings.Builder
		for i, h := range hits {
			fmt.Fprintf(&b, "%d. %s\n   %s (%s)\n", i+1, h.p.title, h.p.url, h.p.source)
		}
		return b.String(), nil
	}, agent.ReadOnly())
	if err != nil {
		return nil, err
	}
	err = agent.AddFunc(s, "fetch", "Return the text of the page at a URL, 2000 characters at a time. Pass offset to read further down.", func(_ context.Context, in fetchArgs) (string, error) {
		p := c.page(in.URL)
		if p == nil || (source != "" && p.source != source) {
			return "", fmt.Errorf("no page at %s: fetch only the URLs search returns", in.URL)
		}
		part, next := chunk(p.text, in.Offset)
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n%s\n\n%s\n\n", p.title, p.url, part)
		if next > 0 {
			fmt.Fprintf(&b, "[characters %d to %d of %d. Fetch with offset %d for more.]", max(0, min(in.Offset, len(p.text))), next, len(p.text), next)
		} else {
			fmt.Fprintf(&b, "[end of page, %d characters]", len(p.text))
		}
		return b.String(), nil
	}, agent.ReadOnly())
	return s, err
}
