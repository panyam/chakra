//go:build eval_llm

// This live benchmark runs the adapted LongMemEval scenarios against a real
// model. It grades them with deterministic substring checks, plus an LLM
// judge for the fuzzy categories. It needs a live provider, so it is
// excluded from the default build and CI. It reports a pass rate rather than
// gating, since memory quality is statistical. Run it with
//
//	LONGMEMEVAL_BASE_URL=http://localhost:1234/v1 LONGMEMEVAL_MODEL=your-model \
//	  go test -tags eval_llm ./eval/longmemeval/ -run TestLive -v
package longmemeval

import (
	"context"
	"os"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/chakra/eval"
)

// liveProvider builds a provider from the LONGMEMEVAL_* env. It skips the
// test when no endpoint is configured, so a bare `go test -tags eval_llm`
// still passes.
func liveProvider(t *testing.T) agent.Provider {
	baseURL := os.Getenv("LONGMEMEVAL_BASE_URL")
	model := os.Getenv("LONGMEMEVAL_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set LONGMEMEVAL_BASE_URL and LONGMEMEVAL_MODEL to run the live memory benchmark")
	}
	p, err := agent.NewOpenAIProvider(agent.OpenAIConfig{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  os.Getenv(os.Getenv("LONGMEMEVAL_API_KEY_ENV")),
	})
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	return p
}

func TestLiveLongMemEval(t *testing.T) {
	provider := liveProvider(t)

	// The whole benchmark is now Suite.Run over an adapter. This test used
	// to hold a hand-rolled copy of that loop. It ran each case, applied
	// its scorers, and tallied. It existed because Suite could not express
	// per-case scorers or multi-turn scenarios. That duplication showed
	// that the adapter seam was missing (issue 1015).
	suite, ok, err := eval.LoadSuite(agent.RunnerConfig{Provider: provider}, SmokeAdapter{})
	if err != nil {
		t.Fatalf("load suite: %v", err)
	}
	if !ok {
		t.Skip("longmemeval-smoke yielded no cases")
	}

	report := suite.Run(context.Background())

	// Report, do not gate. The benchmark's job is to surface the pass rate,
	// since memory quality is statistical.
	t.Logf("\n%s", report)
	t.Logf("LongMemEval-derived pass rate: %d/%d", report.Passed, report.Total)
}
