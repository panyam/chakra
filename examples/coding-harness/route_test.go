package main

import (
	"context"
	"strings"
	"testing"

	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
)

func TestMetricsLineSplitsTokensByModel(t *testing.T) {
	m := metrics{Steps: 3, ByModel: map[string]agent.Usage{
		"gpt-5.5":      {InputTokens: 900, CachedInputTokens: 100, OutputTokens: 50},
		"gpt-5.4-mini": {InputTokens: 300, OutputTokens: 20},
	}}
	if got := m.String(); !strings.Contains(got, " gpt-5.4-mini=300/0/20 gpt-5.5=900/100/50") {
		t.Errorf("metrics line = %q, want both models in sorted order as in/cached/out", got)
	}
	if got := (metrics{Steps: 1}).String(); strings.Contains(got, "=0/0/0") {
		t.Errorf("an unrouted run printed a by-model split: %q", got)
	}
}

func TestRouteNeedsALiveModel(t *testing.T) {
	_, err := exercise(context.Background(), options{routeTo: "openai", routeFrom: 2})
	if err == nil || !strings.Contains(err.Error(), "needs a live model") {
		t.Fatalf("err = %v, want the scripted run to refuse routing", err)
	}
}

func TestRouteExtensionNamesBothModels(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	cfg, err := host.LoadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model.Model = "gpt-5.5"
	ext, err := profile{routeTo: "openai", routeFrom: 2, routeModel: "gpt-5.4-mini"}.routeExtension(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := ext.StepStages()[0]
	for step, want := range map[int]string{1: "gpt-5.5", 2: "gpt-5.4-mini", 5: "gpt-5.4-mini"} {
		v, err := st.Run(context.Background(), agent.StepInfo{Step: step}, agent.StepView{})
		if err != nil {
			t.Fatal(err)
		}
		if v.ProviderName != want || (step > 1) != (v.Provider != nil) {
			t.Errorf("step %d: named %q routed=%v, want %q", step, v.ProviderName, v.Provider != nil, want)
		}
	}
}
