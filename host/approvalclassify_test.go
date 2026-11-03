package host

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/chakra"
	"github.com/panyam/mcpkit/core"
)

type classifierExt struct {
	BaseExtension
	name        string
	classifiers []ApprovalClassifier
}

func (e classifierExt) Name() string                              { return e.name }
func (e classifierExt) ApprovalClassifiers() []ApprovalClassifier { return e.classifiers }

func classifyRm(_ context.Context, info agent.ToolCallInfo) (agent.CallClass, bool) {
	if info.Call.Name != "shell" {
		return agent.CallClass{}, false
	}
	if strings.Contains(string(info.Call.Args.Raw()), "-rf") {
		return agent.CallClass{Disposition: agent.DispositionDestructive, Reason: "recursive delete"}, true
	}
	return agent.CallClass{Disposition: agent.DispositionReadOnly, Key: "ls"}, true
}

// TestExtensionClassifierReachesTheGate checks the wiring. The config builds
// a gate. The host collects an extension's classifier and hands it to that
// gate, and the classifier's reason shows in the default prompt. The gate's
// own decisions are tested in the agent package.
func TestExtensionClassifierReachesTheGate(t *testing.T) {
	a := appWith(t,
		classifierExt{name: "noop", classifiers: []ApprovalClassifier{nil}},
		classifierExt{name: "shell", classifiers: []ApprovalClassifier{classifyRm}},
	)
	var prompts []string
	ask := func(ctx context.Context, info agent.ToolCallInfo) (bool, error) {
		prompts = append(prompts, a.renderApproval(ctx, info))
		return false, nil
	}
	gate := (&ApprovalConfig{Mode: "read-only-auto"}).buildApproval(ask, nil, nil, a.classifyApproval)

	run := func(args string) bool {
		ran := false
		info := agent.ToolCallInfo{Call: agent.ToolCall{Name: "shell", Args: core.NewRawJSON([]byte(args))}, Destructive: true}
		_, _ = gate.WrapToolCall(context.Background(), info, func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
			ran = true
			return &core.ToolResult{}, nil
		})
		return ran
	}
	if !run(`{"cmd":"ls"}`) {
		t.Error("a call the extension classified read-only was asked about under read-only-auto")
	}
	if run(`{"cmd":"rm -rf build"}`) {
		t.Error("a destructive call ran without an ask")
	}
	if len(prompts) != 1 || !strings.HasSuffix(prompts[0], "(recursive delete)") {
		t.Errorf("prompts = %q, want one ending in the classifier's reason", prompts)
	}
}
