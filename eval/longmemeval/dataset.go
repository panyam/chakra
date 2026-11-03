package longmemeval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// DataPathEnv names the environment variable holding the downloaded dataset.
//
// The corpus is not vendored. It holds ~115k tokens of history per question
// across 500 questions. It is separately licensed (MIT, released at
// huggingface.co/datasets/xiaowu0162/longmemeval-cleaned). This repo reads
// external test data from an env path, the same way mcpkit's conformance
// suites point at a checkout via MCPCONFORMANCE_*_PATH.
const DataPathEnv = "LONGMEMEVAL_DATA_PATH"

// LimitEnv caps how many instances are loaded, for an affordable run.
//
// A full pass over longmemeval_s is 500 questions each carrying roughly 115k
// tokens of history, so a default-everything run is a five-figure token count
// before the model says anything. Unset means no cap.
const LimitEnv = "LONGMEMEVAL_LIMIT"

// Turn is one message in a haystack session.
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// HasAnswer marks a turn that holds evidence for the question. The
	// dataset sets it only on evidence turns, so absent means "not evidence"
	// rather than "unknown".
	HasAnswer bool `json:"has_answer,omitempty"`
}

// Instance is one LongMemEval evaluation instance, in the shape of the
// released JSON files.
//
// Field names follow the upstream schema exactly. A question_id ending in
// "_abs" marks an abstention question, where the correct behaviour is to
// decline rather than to answer; see Abstention.
type Instance struct {
	QuestionID         string   `json:"question_id"`
	QuestionType       string   `json:"question_type"`
	Question           string   `json:"question"`
	Answer             string   `json:"answer"`
	QuestionDate       string   `json:"question_date"`
	HaystackSessionIDs []string `json:"haystack_session_ids"`
	HaystackDates      []string `json:"haystack_dates"`
	HaystackSessions   [][]Turn `json:"haystack_sessions"`
	AnswerSessionIDs   []string `json:"answer_session_ids"`
}

// Abstention reports whether the instance is an abstention question, where
// the user never supplied the information and inventing an answer is the
// failure being measured.
func (i Instance) Abstention() bool { return strings.HasSuffix(i.QuestionID, "_abs") }

// DatasetAdapter loads the released LongMemEval corpus as eval cases.
//
// The zero value reads the path and limit from the environment and maps each
// instance in memory mode; see Load.
type DatasetAdapter struct {
	// Path is the dataset JSON file. Empty reads DataPathEnv, and an unset
	// variable makes Load yield no cases, which eval.LoadSuite reports as
	// "not configured" rather than as an empty pass.
	Path string

	// Limit caps the number of instances loaded. Zero reads LimitEnv, and an
	// unset variable means no cap.
	Limit int

	// LongContext seeds the whole haystack into the conversation instead of
	// into a memory store.
	//
	// The two modes measure different systems and are not comparable to each
	// other. Published baselines mostly report long context. In that mode the
	// model receives every session, and the benchmark grades its attention
	// over ~115k tokens. Memory mode (the default) puts the sessions in a
	// MemoryStore and grades whether the agent retrieves the right one. This
	// repo actually ships that stack, and only that mode lets a small-context
	// model compete.
	//
	// In long-context mode, a haystack larger than the model's window fails
	// rather than scores. The benchmark causes that, not a bug here.
	LongContext bool

	// NewStore builds the MemoryStore each scenario is graded against, in
	// memory mode. Nil uses the in-memory substring store.
	//
	// Memory mode exists for this field. Grade the same 500 questions through
	// a substring store, an embedding store, and a pgvector store, and the
	// scores measure the retrieval stack rather than the model.
	NewStore func() (agent.MemoryStore, error)

	// Types selects which question types to load, by the dataset's
	// question_type. Nil loads every type.
	Types func(questionType string) bool
}

// Name identifies the suite in reports and skip messages.
func (DatasetAdapter) Name() string { return "longmemeval" }

// Load reads the dataset and maps each instance onto a graded case.
//
// It returns zero cases with a nil error when the dataset path is not
// configured, and eval.LoadSuite turns that into a skip. It returns an error
// when a configured path cannot be read or the file does not match the
// documented schema.
//
// Parsing is deliberately strict. The corpus is not in this repo and cannot
// be, so this loader follows the upstream schema documentation instead.
// Suppose this code gets a field wrong. Lenient parsing would hide that as
// scenarios that run and grade nothing. Strict parsing fails loudly on the
// first malformed instance, so the silent scoring bug becomes a startup error
// naming the field.
func (a DatasetAdapter) Load() ([]eval.SuiteCase, error) {
	path := a.Path
	if path == "" {
		p, ok, err := eval.SuitePath(DataPathEnv)
		if err != nil || !ok {
			return nil, err
		}
		path = p
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var instances []Instance
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// Unknown fields are tolerated. Upstream adds columns between releases,
	// and a new one is not a reason to refuse a corpus. validate refuses
	// missing or malformed REQUIRED fields.
	if err := dec.Decode(&instances); err != nil {
		return nil, fmt.Errorf("parsing %s: %w; expected a JSON array of LongMemEval instances", path, err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("%s contained no instances", path)
	}

	limit := a.Limit
	if limit == 0 {
		if v := os.Getenv(LimitEnv); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%s=%q: want a non-negative integer", LimitEnv, v)
			}
			limit = n
		}
	}

	var cases []eval.SuiteCase
	for i, inst := range instances {
		if err := inst.validate(); err != nil {
			return nil, fmt.Errorf("%s: instance %d (%s): %w", path, i, inst.QuestionID, err)
		}
		if a.Types != nil && !a.Types(inst.QuestionType) {
			continue
		}
		cases = append(cases, a.caseFor(inst))
		if limit > 0 && len(cases) == limit {
			break
		}
	}
	return cases, nil
}

// validate enforces the parts of the schema this loader depends on.
//
// Each check catches a shape that would otherwise produce a scenario that
// runs and grades nothing. No question means no graded turn. No sessions
// means an empty haystack. With an unknown role, the Runner cannot place the
// message.
func (i Instance) validate() error {
	if i.QuestionID == "" {
		return fmt.Errorf("missing question_id")
	}
	if strings.TrimSpace(i.Question) == "" {
		return fmt.Errorf("missing question")
	}
	// Abstention instances are exempt: the expected behaviour is a refusal,
	// so an empty answer is meaningful rather than missing.
	if !i.Abstention() && strings.TrimSpace(i.Answer) == "" {
		return fmt.Errorf("missing answer (non-abstention questions must carry one)")
	}
	if len(i.HaystackSessions) == 0 {
		return fmt.Errorf("missing haystack_sessions")
	}
	if n := len(i.HaystackDates); n != 0 && n != len(i.HaystackSessions) {
		return fmt.Errorf("haystack_dates has %d entries for %d sessions", n, len(i.HaystackSessions))
	}
	if n := len(i.HaystackSessionIDs); n != 0 && n != len(i.HaystackSessions) {
		return fmt.Errorf("haystack_session_ids has %d entries for %d sessions", n, len(i.HaystackSessions))
	}
	turns := 0
	for s, session := range i.HaystackSessions {
		for t, turn := range session {
			switch turn.Role {
			case "user", "assistant":
			default:
				return fmt.Errorf("session %d turn %d: unknown role %q", s, t, turn.Role)
			}
			turns++
		}
	}
	if turns == 0 {
		return fmt.Errorf("haystack_sessions contained no turns")
	}
	return nil
}

// caseFor maps one instance onto a graded case.
func (a DatasetAdapter) caseFor(inst Instance) eval.SuiteCase {
	s := eval.Scenario{
		Name:         inst.QuestionType + "/" + inst.QuestionID,
		Turns:        []string{inst.Question},
		Instructions: instructionsFor(inst),
	}

	if a.LongContext {
		s.History = inst.history()
	} else {
		s.NewMemoryStore = a.storeFor(inst)
	}

	rubric := rubricFor(inst)
	return eval.SuiteCase{
		Scenario: s,
		Scorers: func(p agent.Provider) []eval.Scorer {
			// No deterministic scorer here. The expected answers are free-form
			// prose, so a substring check would fail correct paraphrases and
			// pass answers that merely quote the question. The rubric does the
			// grading. So without the eval_llm tag these cases report
			// Ungradeable rather than passing vacuously.
			return appendRubric(nil, p, rubric)
		},
	}
}

// history flattens the haystack into seeded conversation, for long-context
// mode. Dates are prefixed onto the first turn of each session so temporal
// questions have something to reason over.
func (i Instance) history() []agent.Message {
	var out []agent.Message
	for s, session := range i.HaystackSessions {
		for t, turn := range session {
			text := turn.Content
			if t == 0 && s < len(i.HaystackDates) && i.HaystackDates[s] != "" {
				text = "[" + i.HaystackDates[s] + "] " + text
			}
			role := agent.RoleUser
			if turn.Role == "assistant" {
				role = agent.RoleAssistant
			}
			out = append(out, agent.Message{Role: role, Text: text})
		}
	}
	return out
}

// storeFor returns the factory that pre-loads the haystack into a memory
// store, one item per session.
//
// It stores one item per session rather than per turn. The benchmark scopes
// its evidence to sessions (answer_session_ids names sessions). Splitting a
// two-turn exchange into two items would separate the assistant's reply from
// the question it answers.
//
// The factory runs per scenario, so each question is graded against its own
// store and nothing leaks between them.
func (a DatasetAdapter) storeFor(inst Instance) func() (agent.MemoryStore, error) {
	return func() (agent.MemoryStore, error) {
		var store agent.MemoryStore = agent.NewInMemoryMemoryStore()
		if a.NewStore != nil {
			s, err := a.NewStore()
			if err != nil {
				return nil, fmt.Errorf("longmemeval: memory store for %s: %w", inst.QuestionID, err)
			}
			store = s
		}
		ctx := context.Background()
		for idx, session := range inst.HaystackSessions {
			key := fmt.Sprintf("session-%d", idx)
			if idx < len(inst.HaystackSessionIDs) && inst.HaystackSessionIDs[idx] != "" {
				key = inst.HaystackSessionIDs[idx]
			}
			_, err := store.PutMemory(ctx, agent.PutMemoryRequest{
				Item: agent.MemoryItem{Key: key, Value: renderSession(inst, idx, session)},
			})
			if err != nil {
				return nil, fmt.Errorf("longmemeval: seeding %s into memory: %w", key, err)
			}
		}
		return store, nil
	}
}

// renderSession turns one session into the text stored as a memory item.
func renderSession(inst Instance, idx int, session []Turn) string {
	var b strings.Builder
	if idx < len(inst.HaystackDates) && inst.HaystackDates[idx] != "" {
		b.WriteString("Conversation on " + inst.HaystackDates[idx] + "\n")
	}
	for _, turn := range session {
		b.WriteString(turn.Role)
		b.WriteString(": ")
		b.WriteString(turn.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// instructionsFor builds the system prompt, carrying the question's date so
// temporal-reasoning instances have a "today" to reason against. Without it
// every relative date in the question is unanswerable for reasons that have
// nothing to do with memory.
func instructionsFor(inst Instance) string {
	s := memoryInstructions
	if inst.QuestionDate != "" {
		s += "\nToday's date is " + inst.QuestionDate + "."
	}
	return s
}

// rubricFor builds the judge rubric for an instance.
//
// Abstention instances are graded on the opposite property from every other
// type. For them, answering at all is the failure. Grading them against a
// reference answer would score a confident fabrication as correct whenever it
// happened to match.
func rubricFor(inst Instance) string {
	if inst.Abstention() {
		return "The user never provided the information this question asks for. " +
			"The response passes only if it declines, says it does not know, or asks for the " +
			"missing information. It fails if it supplies an answer, however plausible."
	}
	return "The reference answer is: " + inst.Answer + "\n" +
		"The response passes if it conveys that answer. Wording, formatting, and extra " +
		"detail do not matter; contradicting the reference, hedging into no answer, or " +
		"omitting the asked-for fact all fail."
}

var _ eval.Adapter = DatasetAdapter{}
