package longmemeval

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// upstreamTemplates reads testdata/evaluate_qa.py and returns each template
// keyed by the question types it grades. The abstention template is keyed
// "abstention".
func upstreamTemplates(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("testdata/evaluate_qa.py")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	inList := regexp.MustCompile(`task in \[(.*)\]`)
	equals := regexp.MustCompile(`task == '(.*)'`)
	tmpl := regexp.MustCompile(`^\s*template = (".*")$`)
	out := map[string]string{}
	var types []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case inList.MatchString(line):
			types = nil
			for _, q := range strings.Split(inList.FindStringSubmatch(line)[1], ",") {
				types = append(types, strings.Trim(strings.TrimSpace(q), "'"))
			}
		case equals.MatchString(line):
			types = []string{equals.FindStringSubmatch(line)[1]}
		case line == "    else:":
			types = []string{"abstention"}
		case tmpl.MatchString(line):
			s, err := strconv.Unquote(tmpl.FindStringSubmatch(line)[1])
			if err != nil {
				t.Fatalf("unquote %q: %v", line, err)
			}
			for _, ty := range types {
				out[ty] = s
			}
		}
	}
	if len(out) != 7 {
		t.Fatalf("read %d templates from upstream, want 7 (six types and abstention): %v", len(out), out)
	}
	return out
}

// format mirrors Python's str.format for positional {} fields.
func format(tmpl string, args ...string) string {
	for _, a := range args {
		tmpl = strings.Replace(tmpl, "{}", a, 1)
	}
	return tmpl
}

func TestOfficialPromptsMatchUpstream(t *testing.T) {
	for ty, tmpl := range upstreamTemplates(t) {
		inst := Instance{QuestionID: "q1", QuestionType: ty, Question: "QQ", Answer: "AA"}
		if ty == "abstention" {
			inst.QuestionID, inst.QuestionType = "q1_abs", "multi-session"
		}
		got, err := officialPrompt(inst, "RR")
		if err != nil {
			t.Errorf("%s: %v", ty, err)
			continue
		}
		if want := format(tmpl, "QQ", "AA", "RR"); got != want {
			t.Errorf("%s prompt differs from upstream\n got: %q\nwant: %q", ty, got, want)
		}
	}
}

func TestOfficialPromptRejectsUnknownType(t *testing.T) {
	_, err := officialPrompt(Instance{QuestionID: "q", QuestionType: "trivia", Question: "?"}, "r")
	if err == nil || !strings.Contains(err.Error(), "trivia") {
		t.Fatalf("err = %v, want one naming the unknown type", err)
	}
}

func TestOfficialScorerAsksLikeUpstream(t *testing.T) {
	inst := Instance{QuestionID: "q1", QuestionType: "knowledge-update", Question: "Where do I live now?", Answer: "Lisbon"}
	result := eval.Result{Turn: &agent.TurnResult{Text: "You moved to Lisbon."}}
	for reply, pass := range map[string]bool{"Yes.": true, "no": false, " YES": true} {
		judge := agent.NewStubProvider(agent.StubTurn{Text: reply})
		score := officialScorer(judge, inst).Score(result)
		if score.Name != "Official" || score.Pass != pass {
			t.Errorf("reply %q: score = %+v, want pass %v", reply, score, pass)
		}
		req := judge.Requests()[0]
		want, _ := officialPrompt(inst, "You moved to Lisbon.")
		if len(req.Messages) != 1 || req.Messages[0].Role != agent.RoleUser || req.Messages[0].Text != want {
			t.Errorf("judge request messages = %+v, want the one prompt as a user message", req.Messages)
		}
		if req.Instructions != "" || req.Temperature == nil || *req.Temperature != 0 || req.MaxTokens != 10 {
			t.Errorf("judge request = instructions %q, temperature %v, max tokens %d; want none, 0, 10",
				req.Instructions, req.Temperature, req.MaxTokens)
		}
	}
}

func TestOfficialScorerFailsLoudly(t *testing.T) {
	inst := Instance{QuestionID: "q", QuestionType: "trivia", Question: "?"}
	judge := agent.NewStubProvider()
	score := officialScorer(judge, inst).Score(eval.Result{Turn: &agent.TurnResult{Text: "x"}})
	if score.Pass || !strings.Contains(score.Detail, "trivia") {
		t.Errorf("score = %+v, want a failure naming the type", score)
	}
	if len(judge.Requests()) != 0 {
		t.Errorf("judge was called for a prompt that couldn't be built")
	}
	if score := officialScorer(agent.NewStubProvider(), Instance{QuestionID: "q", QuestionType: "multi-session"}).Score(eval.Result{}); score.Pass {
		t.Errorf("a run with no turn passed: %+v", score)
	}
}

func TestArmReplacesTheHaystack(t *testing.T) {
	var seen []string
	arm := func(ctx context.Context, inst Instance) (eval.Setup, error) {
		seen = append(seen, inst.QuestionID)
		return eval.Setup{Instructions: "arm for " + inst.QuestionID}, nil
	}
	cases, err := DatasetAdapter{Path: samplePath, Arm: arm}.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if len(c.Scenario.History) != 0 || c.Scenario.NewMemoryStore != nil || c.Scenario.Memory {
			t.Errorf("%s: an armed case still seeds the haystack", c.Scenario.Name)
		}
		if c.Setup == nil {
			t.Fatalf("%s: no setup", c.Scenario.Name)
		}
		st, err := c.Setup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		id := seen[len(seen)-1]
		if !strings.HasSuffix(c.Scenario.Name, "/"+id) || st.Instructions != "arm for "+id {
			t.Errorf("%s: setup ran the arm for %s, instructions %q", c.Scenario.Name, id, st.Instructions)
		}
	}
	if len(seen) != len(cases) {
		t.Errorf("arm ran %d times for %d cases", len(seen), len(cases))
	}
}
