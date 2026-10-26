package files

import (
	"strings"
	"testing"
)

const fiveLines = "one\ntwo\nthree\nfour\nfive\n"

// #109: a model that needed one function from a 38 KB file had to read all of
// it, which then rode along in every later step of the turn.
func TestReadLineRangeReturnsThoseLinesWithTheWholeFileHash(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "f.txt", fiveLines)

	text, isErr := call(t, s, "read_file", map[string]any{"path": "f.txt", "start_line": float64(2), "end_line": float64(3)})
	if isErr {
		t.Fatalf("read_file failed: %s", text)
	}
	body := text[strings.Index(text, "\n\n")+2:]
	if body != "two\nthree\n" {
		t.Errorf("body = %q, want lines 2-3", body)
	}
	for _, want := range []string{"hash: " + Hash(fiveLines) + " (whole file)", "lines: 2-3 of 5"} {
		if !strings.Contains(text, want) {
			t.Errorf("header is missing %q:\n%s", want, text)
		}
	}
}

func TestEditWorksWithTheHashFromAPartialRead(t *testing.T) {
	s, root := newTestSource(t)
	p := write(t, root, "f.txt", fiveLines)
	call(t, s, "read_file", map[string]any{"path": "f.txt", "start_line": float64(4)})

	text, isErr := call(t, s, "edit_file", map[string]any{
		"path": "f.txt", "expect_hash": Hash(fiveLines),
		"edits": []any{map[string]any{"old": "four\n", "new": "FOUR\n"}},
	})
	if isErr {
		t.Fatalf("edit with a partial read's hash was refused: %s", text)
	}
	if got := readFile(t, p); got != "one\ntwo\nthree\nFOUR\nfive\n" {
		t.Errorf("file = %q", got)
	}
}

func TestReadLineRangeClampsAndReports(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "f.txt", fiveLines)
	cases := []struct {
		name  string
		args  map[string]any
		lines string
		body  string
	}{
		{"start only reads to the end", map[string]any{"start_line": float64(4)}, "lines: 4-5 of 5", "four\nfive\n"},
		{"end only reads from the top", map[string]any{"end_line": float64(1)}, "lines: 1-1 of 5", "one\n"},
		{"end past the file is clamped", map[string]any{"start_line": float64(5), "end_line": float64(99)}, "lines: 5-5 of 5", "five\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.args["path"] = "f.txt"
			text, isErr := call(t, s, "read_file", tc.args)
			if isErr || !strings.Contains(text, tc.lines) || !strings.HasSuffix(text, "\n\n"+tc.body) {
				t.Errorf("got (isErr=%v):\n%s", isErr, text)
			}
		})
	}
	text, isErr := call(t, s, "read_file", map[string]any{"path": "f.txt", "start_line": float64(9)})
	if !isErr || !strings.Contains(text, "has 5 lines") {
		t.Errorf("a start past the end should be an error naming the length, got (isErr=%v) %s", isErr, text)
	}
}

func TestPlainReadReportsTheLineCount(t *testing.T) {
	s, root := newTestSource(t)
	write(t, root, "f.txt", fiveLines)
	text, _ := call(t, s, "read_file", map[string]any{"path": "f.txt"})
	if !strings.Contains(text, "lines: 5\n") || !strings.HasSuffix(text, "\n\n"+fiveLines) {
		t.Errorf("plain read = %q, want the whole file and its line count", text)
	}
}
