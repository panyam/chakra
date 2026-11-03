package exec

import (
	"strings"
	"testing"
)

func words(s string) []string { return strings.Fields(s) }

func TestRulesLongestPrefixAndStrictestTie(t *testing.T) {
	rs, err := NewRules([]PrefixRule{
		{Prefix: [][]string{{"git"}}, Decision: Prompt},
		{Prefix: [][]string{{"git"}, {"status", "diff", "log"}}, Decision: Allow},
		{Prefix: [][]string{{"git"}, {"push"}, {"--force"}}, Decision: Forbidden},
		{Prefix: [][]string{{"go"}, {"test"}}, Decision: Allow},
		{Prefix: [][]string{{"go"}, {"test"}}, Decision: Prompt},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]Decision{
		"git status -s":                Allow,
		"/usr/bin/git diff HEAD":       Allow,
		"git push":                     Prompt,
		"git push --force origin main": Forbidden,
		"go test ./...":                Prompt, // equal length: the stricter wins
		"gitx status":                  "",
		"go":                           "",
		"git statusx":                  Prompt,
	}
	for cmd, want := range cases {
		r, ok := rs.match(words(cmd))
		got := Decision("")
		if ok {
			got = r.Decision
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", cmd, got, want)
		}
	}
}

func TestRulesExamplesAreCheckedAtLoad(t *testing.T) {
	_, err := NewRules([]PrefixRule{{
		Prefix: [][]string{{"git"}, {"status"}}, Decision: Allow,
		Match: [][]string{words("git diff")},
	}})
	if err == nil || !strings.Contains(err.Error(), "does not match its example") {
		t.Errorf("a rule missing its own match example loaded: %v", err)
	}
	_, err = NewRules([]PrefixRule{{
		Prefix: [][]string{{"git"}}, Decision: Allow,
		NotMatch: [][]string{words("git push")},
	}})
	if err == nil || !strings.Contains(err.Error(), "notMatch") {
		t.Errorf("a rule matching its notMatch example loaded: %v", err)
	}
	for _, bad := range []PrefixRule{
		{Decision: Allow},
		{Prefix: [][]string{{"git"}, {}}, Decision: Allow},
		{Prefix: [][]string{{"git"}}, Decision: "maybe"},
	} {
		if _, err := NewRules([]PrefixRule{bad}); err == nil {
			t.Errorf("rule %+v loaded", bad)
		}
	}
}

func TestCommandsOfSplitsPlainScripts(t *testing.T) {
	cases := map[string][]string{
		"go test ./... && go vet ./...": {"go test ./...", "go vet ./..."},
		"git status; git diff":          {"git status", "git diff"},
		"go test ./... | tail -n 20":    {"go test ./...", "tail -n 20"},
		"make lint || true":             {"make lint", "true"},
		`grep -n 'a b' "file one.go"`:   {"grep -n a b file one.go"},
		"go test -run=TestX ./pkg":      {"go test -run=TestX ./pkg"},
	}
	for script, want := range cases {
		cmds, ok := commandsOf([]string{"bash", "-lc", script})
		if !ok {
			t.Errorf("%q: refused", script)
			continue
		}
		var got []string
		for _, c := range cmds {
			got = append(got, strings.Join(c, " "))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%q: got %q, want %q", script, got, want)
		}
	}
	// A quoted 'a b' must split as one word, not two.
	cmds, _ := commandsOf([]string{"sh", "-c", `grep 'a b' f`})
	if len(cmds[0]) != 3 {
		t.Errorf("quoted word split: %q", cmds[0])
	}
}

// TestCommandsOfRefusesWhatItCannotRead lists the constructs that could hide
// a command from the rules. Each must make the splitter give up. The call is
// then treated as matching nothing, and it needs approval.
func TestCommandsOfRefusesWhatItCannotRead(t *testing.T) {
	for _, script := range []string{
		"echo $(rm -rf ~)",
		"echo $HOME",
		"rm $TARGET",
		"echo `rm -rf ~`",
		`echo "$HOME"`,
		"git status > /etc/passwd",
		"cat < /etc/shadow",
		"(rm -rf ~)",
		"{ rm -rf ~; }",
		"rm -rf *",
		"sleep 10 &",
		"FOO=bar rm -rf ~",
		`git status\
rm -rf ~`,
		"git status\nrm -rf ~",
		"echo 'unterminated",
		"git status;",
		"; git status",
		"git status && && ls",
		"echo hi # rm -rf ~",
		`echo "a\"b"`,
		"rm -rf ~",
	} {
		if cmds, ok := commandsOf([]string{"bash", "-c", script}); ok {
			t.Errorf("%q: accepted as %q", script, cmds)
		}
	}
}

func TestCommandsOfLeavesOtherArgvAlone(t *testing.T) {
	for _, argv := range [][]string{
		{"bash", "script.sh"},
		{"bash", "-c", "ls", "extra"},
		{"python3", "-c", "print(1)"},
	} {
		cmds, ok := commandsOf(argv)
		if !ok || len(cmds) != 1 || strings.Join(cmds[0], " ") != strings.Join(argv, " ") {
			t.Errorf("%q: got %q, %v; want it matched as one argv", argv, cmds, ok)
		}
	}
}
