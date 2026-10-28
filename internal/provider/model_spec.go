package provider

import (
	"encoding/json"
	"factory.local/cliproxyapi-factory/internal/model"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// ModelSpec owns metadata and the existing execution constraints. Unknown
// discovery has a route but no invented limits or multimodal/thinking support.
type ModelSpec struct {
	ID                                                              string
	Route                                                           model.Route
	Name                                                            string
	Input                                                           []string
	Context                                                         int64
	Tokens                                                          int64
	Reasoning                                                       bool
	ModernClaude, MiniMax, Sonnet46, Codex, StrictGPT, ThinkingOnly bool
	MinimalToLow                                                    bool
	Levels                                                          []string
}

var modelSpecs = loadModelSpecs()

func loadModelSpecs() map[string]ModelSpec {
	var entries []struct {
		ID, Name  string
		Input     []string
		Context   int64 `json:"contextWindow"`
		Tokens    int64 `json:"maxTokens"`
		Reasoning bool
	}
	if e := json.Unmarshal(catalog, &entries); e != nil {
		panic("invalid embedded Factory catalog")
	}
	specs := map[string]ModelSpec{}
	for _, e := range entries {
		r, _ := model.Resolve(e.ID)
		s := ModelSpec{ID: e.ID, Name: e.Name, Route: r, Input: e.Input, Context: e.Context, Tokens: e.Tokens, Reasoning: e.Reasoning}
		if e.Reasoning {
			s.Levels = []string{"none", "low", "medium", "high", "xhigh"}
		}
		specs[e.ID] = s
	}
	for _, id := range []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-opus-5-5-fast", "gpt-6.1-sol", "glm-5.3", "kimi-k3"} {
		if _, ok := specs[id]; !ok {
			r, _ := model.Resolve(id)
			specs[id] = ModelSpec{ID: id, Name: id, Route: r, Input: []string{"text"}}
		}
	}
	set := func(id string, change func(*ModelSpec)) { s := specs[id]; change(&s); specs[id] = s }
	for _, id := range []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-opus-5-5-fast"} {
		set(id, func(s *ModelSpec) {
			s.ModernClaude = true
			s.Reasoning = true
			s.ThinkingOnly = s.ID != "claude-sonnet-5-5"
			s.Levels = []string{"low", "medium", "high", "xhigh", "max"}
			if !s.ThinkingOnly {
				s.Levels = append([]string{"none"}, s.Levels...)
			}
		})
	}
	set("claude-sonnet-4-6", func(s *ModelSpec) { s.Sonnet46 = true; s.Levels = []string{"none", "low", "medium", "high", "max"} })
	set("minimax-m3", func(s *ModelSpec) {
		s.MiniMax = true
		s.Levels = []string{"none", "low", "medium", "high", "xhigh", "max"}
	})
	set("gpt-5.3-codex", func(s *ModelSpec) { s.Codex = true; s.Levels = []string{"none", "low", "medium", "high", "xhigh"} })
	set("gpt-6.1-sol", func(s *ModelSpec) {
		s.StrictGPT = true
		s.ThinkingOnly = true
		s.Reasoning = true
		s.Levels = []string{"low", "medium", "high", "xhigh", "max"}
	})
	set("glm-5.3", func(s *ModelSpec) {
		s.ThinkingOnly = true
		s.Reasoning = true
		s.Levels = []string{"low", "medium", "high", "xhigh"}
	})
	for _, id := range []string{"gpt-5.3-codex", "gpt-6.1-sol", "kimi-k3", "nemotron-3-ultra"} {
		set(id, func(s *ModelSpec) { s.MinimalToLow = true })
	}
	return specs
}
func modelSpec(id string) ModelSpec {
	if s, ok := modelSpecs[id]; ok {
		return s
	}
	r, _ := model.Resolve(id)
	return ModelSpec{ID: id, Name: id, Route: r, Input: []string{"text"}}
}
func (s ModelSpec) permitsEffort(level string) bool {
	for _, l := range s.Levels {
		if l == level {
			return true
		}
	}
	return false
}
func (s ModelSpec) info() pluginapi.ModelInfo {
	m := pluginapi.ModelInfo{ID: "factory/" + s.ID, Object: "model", OwnedBy: "factory", Type: "factory", DisplayName: displayNameFor(s), Name: s.ID, ContextLength: s.Context, MaxCompletionTokens: s.Tokens, SupportedInputModalities: append([]string(nil), s.Input...), SupportedOutputModalities: []string{"text"}, SupportedParameters: []string{"stream"}}
	if s.Context > 0 || s.Reasoning {
		m.SupportedParameters = append(m.SupportedParameters, "tools", "tool_choice")
	}
	if s.Reasoning && len(s.Levels) > 0 {
		m.Thinking = &pluginapi.ThinkingSupport{Min: 1024, Max: 64000, ZeroAllowed: s.permitsEffort("none"), DynamicAllowed: true, Levels: append([]string(nil), s.Levels...)}
	}
	return m
}
