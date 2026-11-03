package exec

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Decision is what a prefix rule says about the commands it matches.
type Decision string

const (
	// Allow runs a matching command without asking wherever the approval mode
	// auto-runs read-only calls. It still runs inside the sandbox.
	Allow Decision = "allow"
	// Prompt asks before a matching command runs, unless the mode runs
	// everything.
	Prompt Decision = "prompt"
	// Forbidden refuses a matching command under every mode, with the rule's
	// justification as the reason.
	Forbidden Decision = "forbidden"
)

// PrefixRule decides for every command whose argv starts with Prefix.
type PrefixRule struct {
	// Prefix is matched position by position against argv. Each position
	// lists the words accepted there, so [["git"], ["status", "diff"]] matches
	// "git status -s" and "git diff HEAD" but not "git push".
	Prefix [][]string

	Decision Decision

	// Justification says why, shown in the approval prompt and as the reason
	// a forbidden command was refused.
	Justification string

	// Match and NotMatch are example argvs the rule must and must not match.
	// They are checked when the rules load, so a rule that doesn't do what
	// its author meant fails at startup instead of in a session.
	Match    [][]string
	NotMatch [][]string
}

// Rules is a validated rule set.
type Rules struct {
	rules []PrefixRule
}

// NewRules validates rules, including each rule's examples.
func NewRules(rules []PrefixRule) (*Rules, error) {
	for i, r := range rules {
		if len(r.Prefix) == 0 {
			return nil, fmt.Errorf("exec: shell rule %d has an empty prefix", i)
		}
		for _, alts := range r.Prefix {
			if len(alts) == 0 {
				return nil, fmt.Errorf("exec: shell rule %d (%s) has a position with no words", i, r.String())
			}
		}
		switch r.Decision {
		case Allow, Prompt, Forbidden:
		default:
			return nil, fmt.Errorf("exec: shell rule %d (%s): decision %q, want allow, prompt or forbidden", i, r.String(), r.Decision)
		}
		for _, ex := range r.Match {
			if !r.matches(ex) {
				return nil, fmt.Errorf("exec: shell rule %d (%s) does not match its example %q", i, r.String(), strings.Join(ex, " "))
			}
		}
		for _, ex := range r.NotMatch {
			if r.matches(ex) {
				return nil, fmt.Errorf("exec: shell rule %d (%s) matches %q, listed under notMatch", i, r.String(), strings.Join(ex, " "))
			}
		}
	}
	return &Rules{rules: append([]PrefixRule(nil), rules...)}, nil
}

// String renders the prefix as words, alternatives joined by "|".
func (r PrefixRule) String() string {
	words := make([]string, len(r.Prefix))
	for i, alts := range r.Prefix {
		words[i] = strings.Join(alts, "|")
	}
	return strings.Join(words, " ")
}

func (r PrefixRule) matches(argv []string) bool {
	if len(argv) < len(r.Prefix) {
		return false
	}
	for i, alts := range r.Prefix {
		w := argv[i]
		if i == 0 {
			// "/usr/bin/git" is git as far as a rule is concerned.
			w = filepath.Base(w)
		}
		ok := false
		for _, a := range alts {
			if w == a {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// match returns the rule deciding argv: the longest matching prefix, with the
// most restrictive decision winning a tie. ok is false when no rule matches.
func (rs *Rules) match(argv []string) (PrefixRule, bool) {
	var best PrefixRule
	found := false
	for _, r := range rs.rules {
		if !r.matches(argv) {
			continue
		}
		if !found || len(r.Prefix) > len(best.Prefix) ||
			(len(r.Prefix) == len(best.Prefix) && strictness(r.Decision) > strictness(best.Decision)) {
			best, found = r, true
		}
	}
	return best, found
}

func strictness(d Decision) int {
	switch d {
	case Forbidden:
		return 2
	case Prompt:
		return 1
	}
	return 0
}

// shells are the interpreters whose -c script is split into commands and
// matched piece by piece, rather than matched as "bash -c ...".
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true}

// commandsOf returns the simple commands argv will run. A plain argv is one
// command. A shell given a script with -c (or -lc) yields the script's
// commands, split on &&, ||, ; and |. ok is false when the script uses
// anything the splitter does not understand (redirection, substitution,
// subshells, globs, variables, environment assignments, backslashes), and the
// caller must then treat argv as matching no rule.
func commandsOf(argv []string) (cmds [][]string, ok bool) {
	if len(argv) == 3 && shells[filepath.Base(argv[0])] && (argv[1] == "-c" || argv[1] == "-lc") {
		return splitScript(argv[2])
	}
	if len(argv) == 0 {
		return nil, false
	}
	return [][]string{argv}, true
}

// splitScript tokenizes a shell script conservatively. It accepts plain words,
// single-quoted strings, and double-quoted strings without $, ` or \ in them,
// separated into commands by the four list operators. Everything else makes
// it give up, because a construct it misread is a command a rule never saw.
func splitScript(script string) ([][]string, bool) {
	var cmds [][]string
	var cur []string
	var word strings.Builder
	inWord := false

	endWord := func() {
		if inWord {
			cur = append(cur, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func() bool {
		endWord()
		if len(cur) == 0 {
			return false
		}
		cmds = append(cmds, cur)
		cur = nil
		return true
	}

	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case c == ' ' || c == '\t':
			endWord()
		case c == '\'':
			j := strings.IndexByte(script[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			word.WriteString(script[i+1 : i+1+j])
			inWord = true
			i += j + 1
		case c == '"':
			j := strings.IndexByte(script[i+1:], '"')
			if j < 0 {
				return nil, false
			}
			body := script[i+1 : i+1+j]
			if strings.ContainsAny(body, "$`\\") {
				return nil, false
			}
			word.WriteString(body)
			inWord = true
			i += j + 1
		case c == ';':
			if !endCmd() {
				return nil, false
			}
		case c == '&' || c == '|':
			// && and || and |; a lone & backgrounds a job, which is out.
			if c == '&' && (i+1 >= len(script) || script[i+1] != '&') {
				return nil, false
			}
			if i+1 < len(script) && script[i+1] == c {
				i++
			}
			if !endCmd() {
				return nil, false
			}
		case strings.IndexByte("<>()$`\\*?[]{}~!#\n\r", c) >= 0:
			return nil, false
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	if !endCmd() {
		return nil, false
	}
	for _, cmd := range cmds {
		if strings.Contains(cmd[0], "=") {
			return nil, false
		}
	}
	return cmds, true
}
