package longmemeval

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// The grading prompts below are copied verbatim from LongMemEval's
// src/evaluation/evaluate_qa.py at commit
// 9e0b455f4ef0e2ab8f2e582289761153549043fc (MIT License, Copyright (c) 2024
// Di Wu). testdata/evaluate_qa.py holds the upstream function, and
// TestOfficialPromptsMatchUpstream checks these strings against it. Each %s
// stands for one {} in the Python template, in the order question, answer,
// response.
const (
	officialPlain = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. \n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."

	officialTemporal = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. In addition, do not penalize off-by-one errors for the number of days. If the question asks for the number of days/weeks/months, etc., and the model makes off-by-one errors (e.g., predicting 19 days when the answer is 18), the model's response is still correct. \n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."

	officialUpdate = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response contains some previous information along with an updated answer, the response should be considered as correct as long as the updated answer is the required answer.\n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."

	officialPreference = "I will give you a question, a rubric for desired personalized response, and a response from a model. Please answer yes if the response satisfies the desired response. Otherwise, answer no. The model does not need to reflect all the points in the rubric. The response is correct as long as it recalls and utilizes the user's personal information correctly.\n\nQuestion: %s\n\nRubric: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."

	officialAbstention = "I will give you an unanswerable question, an explanation, and a response from a model. Please answer yes if the model correctly identifies the question as unanswerable. The model could say that the information is incomplete, or some other information is given but the asked information is not.\n\nQuestion: %s\n\nExplanation: %s\n\nModel Response: %s\n\nDoes the model correctly identify the question as unanswerable? Answer yes or no only."
)

// officialPrompt builds the grading prompt evaluate_qa.py would send for this
// instance and response. Upstream marks abstention by "_abs" anywhere in the
// question_id, and so does this. An unknown question_type is an error, where
// upstream raises NotImplementedError.
func officialPrompt(inst Instance, response string) (string, error) {
	tmpl := officialAbstention
	if !strings.Contains(inst.QuestionID, "_abs") {
		switch inst.QuestionType {
		case "single-session-user", "single-session-assistant", "multi-session":
			tmpl = officialPlain
		case "temporal-reasoning":
			tmpl = officialTemporal
		case "knowledge-update":
			tmpl = officialUpdate
		case "single-session-preference":
			tmpl = officialPreference
		default:
			return "", fmt.Errorf("longmemeval: no official grading prompt for question_type %q", inst.QuestionType)
		}
	}
	return fmt.Sprintf(tmpl, inst.Question, inst.Answer, response), nil
}

// officialScorer grades a response the way evaluate_qa.py does. It sends the
// prompt as one user message with no system prompt, at temperature 0 and at
// most 10 tokens, and passes when "yes" appears anywhere in the lowercased
// reply. Unlike eval.Judge, the judge sees only the final answer and never
// the transcript, so the tool calls that led to it can't sway the grade.
//
// It is built in every build so CI can test it against a stub. Only the
// choice to use it sits behind the eval_llm tag; see appendOfficial.
func officialScorer(p agent.Provider, inst Instance) eval.Scorer {
	return scoreFunc(func(r eval.Result) eval.Score {
		fail := func(detail string) eval.Score {
			return eval.Score{Name: "Official", Detail: detail}
		}
		if r.Turn == nil {
			return fail("the run produced no answer to grade")
		}
		prompt, err := officialPrompt(inst, r.Turn.Text)
		if err != nil {
			return fail(err.Error())
		}
		zero := 0.0
		resp, err := p.Generate(context.Background(), agent.ProviderRequest{
			Messages:    []agent.Message{{Role: agent.RoleUser, Text: prompt}},
			Temperature: &zero,
			MaxTokens:   10,
		})
		if err != nil {
			return fail("judge model error: " + err.Error())
		}
		if resp == nil {
			return fail("judge returned no reply")
		}
		reply := strings.TrimSpace(resp.Text)
		if strings.Contains(strings.ToLower(reply), "yes") {
			return eval.Score{Name: "Official", Pass: true, Value: 1, Detail: reply}
		}
		return fail(reply)
	})
}

// scoreFunc adapts a function to eval.Scorer.
type scoreFunc func(eval.Result) eval.Score

func (f scoreFunc) Score(r eval.Result) eval.Score { return f(r) }
