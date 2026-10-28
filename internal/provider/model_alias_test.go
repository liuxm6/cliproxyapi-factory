package provider

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func testModelResponse() pluginapi.ModelResponse {
	return pluginapi.ModelResponse{
		Provider: "factory",
		Models: []pluginapi.ModelInfo{
			{ID: "factory/claude-opus-5-5", Name: "claude-opus-5-5"},
			{ID: "factory/gpt-6-astra", Name: "gpt-6-astra"},
			{ID: "factory/gpt-5.4", Name: "gpt-5.4"},
		},
	}
}

func modelIDs(resp pluginapi.ModelResponse) []string {
	out := make([]string, 0, len(resp.Models))
	for _, m := range resp.Models {
		out = append(out, m.ID)
	}
	return out
}

func TestApplyHostModelRulesAliasForks(t *testing.T) {
	resp := ApplyHostModelRules(testModelResponse(), pluginapi.HostConfigSummary{
		OAuthModelAlias: map[string][]pluginapi.ModelAlias{
			"factory": {{Name: "factory/gpt-6-astra", Alias: "claude-factory-gpt-6-astra"}},
		},
	})
	ids := modelIDs(resp)
	want := []string{"factory/claude-opus-5-5", "factory/gpt-6-astra", "claude-factory-gpt-6-astra", "factory/gpt-5.4"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
	if got := resolveRequestModel("claude-factory-gpt-6-astra"); got != "gpt-6-astra" {
		t.Fatalf("alias resolve = %q, want gpt-6-astra", got)
	}
}

func TestApplyHostModelRulesAliasMatchesBareName(t *testing.T) {
	resp := ApplyHostModelRules(testModelResponse(), pluginapi.HostConfigSummary{
		OAuthModelAlias: map[string][]pluginapi.ModelAlias{
			"factory": {{Name: "gpt-6-astra", Alias: "claude-factory-gpt-6-astra"}},
		},
	})
	found := false
	for _, id := range modelIDs(resp) {
		if id == "claude-factory-gpt-6-astra" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bare-name alias not applied: %v", modelIDs(resp))
	}
}

func TestApplyHostModelRulesExcluded(t *testing.T) {
	resp := ApplyHostModelRules(testModelResponse(), pluginapi.HostConfigSummary{
		ExcludedModels: map[string][]string{
			"factory": {"factory/gpt-*"},
		},
	})
	ids := modelIDs(resp)
	if len(ids) != 1 || ids[0] != "factory/claude-opus-5-5" {
		t.Fatalf("ids = %v, want only claude model", ids)
	}
}

func TestApplyHostModelRulesExcludedBareName(t *testing.T) {
	resp := ApplyHostModelRules(testModelResponse(), pluginapi.HostConfigSummary{
		ExcludedModels: map[string][]string{
			"factory": {"*astra"},
		},
	})
	ids := modelIDs(resp)
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want 2 models", ids)
	}
}

func TestResolveRequestModel(t *testing.T) {
	ApplyHostModelRules(testModelResponse(), pluginapi.HostConfigSummary{
		OAuthModelAlias: map[string][]pluginapi.ModelAlias{
			"factory": {{Name: "factory/gpt-6-astra", Alias: "claude-factory-gpt-6-astra"}},
		},
	})
	cases := map[string]string{
		"factory/gpt-6-astra":          "gpt-6-astra",
		"claude-factory-gpt-6-astra":   "gpt-6-astra",
		"CLAUDE-FACTORY-GPT-6-ASTRA":   "gpt-6-astra",
		"claude-opus-5-5":              "claude-opus-5-5",
		"factory/claude-opus-5-5":      "claude-opus-5-5",
		"unrelated":                    "unrelated",
	}
	for in, want := range cases {
		if got := resolveRequestModel(in); got != want {
			t.Fatalf("resolveRequestModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchWildcard(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"factory/gpt-6-astra", "factory/gpt-6-astra", true},
		{"factory/gpt-*", "factory/gpt-6-astra", true},
		{"*-astra", "factory/gpt-6-astra", true},
		{"*astra*", "factory/gpt-6-astra", true},
		{"*gpt*astra", "factory/gpt-6-astra", true},
		{"gpt-*", "factory/gpt-6-astra", false},
		{"factory/claude-*", "factory/gpt-6-astra", false},
		{"", "factory/gpt-6-astra", false},
	}
	for _, c := range cases {
		if got := matchWildcard(c.pattern, c.value); got != c.want {
			t.Fatalf("matchWildcard(%q,%q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}
