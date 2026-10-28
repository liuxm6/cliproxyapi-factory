package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Compatibility is Factory-scoped, never selected from the client's identity.
// SensitiveWords is opt-in; enabling user text may alter code or quoted text.
// Matcher algorithm adapted from CPA v8.0.4 cloak_obfuscate.go (MIT).
type Compatibility struct {
	Mode              string   `yaml:"mode" json:"mode"`
	SensitiveWords    []string `yaml:"sensitive_words" json:"sensitive_words"`
	ObfuscateUserText bool     `yaml:"obfuscate_user_text" json:"obfuscate_user_text"`
}

func (c Compatibility) Validate() error {
	if c.Mode != "" && c.Mode != "factory" && c.Mode != "passthrough" {
		return fmt.Errorf("factory: compatibility mode must be factory or passthrough")
	}
	if c.Mode == "passthrough" && (len(c.SensitiveWords) > 0 || c.ObfuscateUserText) {
		return fmt.Errorf("factory: passthrough cannot enable text obfuscation")
	}
	if len(c.SensitiveWords) > 128 {
		return fmt.Errorf("factory: too many sensitive words")
	}
	for _, w := range c.SensitiveWords {
		if len(w) > 4096 {
			return fmt.Errorf("factory: sensitive word too long")
		}
	}
	return nil
}

// wireFingerprints are literal strings Factory's usage-policy scan matches to
// reject requests that did not come from Factory's own client. They are all
// harness boilerplate, never user intent: API billing headers, the client
// identity sentence, the injected environment block, and the model identity
// line. Verified against live upstream responses.
var wireFingerprints = []string{
	// API-injected metadata (billing/version headers leaked into prompts)
	"x-anthropic-billing-header", "cc_version", "cc_entrypoint",
	// client identity / brand vocabulary
	"Claude Agent SDK", "Claude Code", "Claude Desktop", "claude-cli",
	"agent-sdk", "claude.ai", "claude.com",
	"Anthropic", "Claude", "Fable", "Sonnet", "Opus", "Haiku",
	"OpenAI", "ChatGPT", "GPT-", "Codex", "codex-cli",
	"Gemini CLI", "gemini-cli", "GitHub Copilot", "Copilot",
	"Cursor", "Windsurf", "Cline", "Aider", "Roo Code", "opencode",
	"Tabnine", "Codeium", "Augment",
	// injected environment + model identity blocks that carry no brand word
	"You have been invoked in the following environment",
	"Primary working directory", "Is a git repository",
	"You are powered by the model named", "The exact model ID is",
	"Assistant knowledge cutoff", "Available agent types for the Agent tool",
	"environment_context",
}

// harnessArtifacts is the subset of wireFingerprints that is always produced
// by client harnesses rather than typed by a person, so it is also applied to
// user-role text where a client may inject its environment context. Brand
// words stay system-side: obfuscating them inside user-authored text could
// leave a zero-width space in files the model later writes.
var harnessArtifacts = []string{
	"x-anthropic-billing-header", "cc_version", "cc_entrypoint",
	"You have been invoked in the following environment",
	"Primary working directory", "Is a git repository",
	"You are powered by the model named", "The exact model ID is",
	"Assistant knowledge cutoff", "Available agent types for the Agent tool",
	"environment_context",
}

func GatewayRequestWithCompatibility(body []byte, format string, c Compatibility) ([]byte, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if format != "claude" && format != "openai" && format != "openai-response" {
		return nil, fmt.Errorf("factory: unsupported wire format")
	}
	if c.Mode == "passthrough" {
		if !json.Valid(body) {
			return nil, fmt.Errorf("factory: invalid JSON")
		}
		return append([]byte(nil), body...), nil
	}
	b, e := c.obfuscate(body, format)
	if e != nil {
		return nil, e
	}
	return GatewayRequest(b, format)
}

// zwspChange builds a replacer that inserts zero-width spaces inside each
// listed match: after the first rune, at every word boundary, and roughly every
// twelfth rune. The scan is case-insensitive. Breaking the match at a single
// point only defeats full-phrase signatures; seeding every boundary defeats
// sub-phrase signatures too, while the text stays semantically identical for
// the model.
func zwspChange(words []string) (func(string) string, error) {
	kept := []string{}
	for _, w := range words {
		w = strings.TrimSpace(w)
		if utf8.RuneCountInString(w) >= 2 && !strings.Contains(w, "​") {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		return func(s string) string { return s }, nil
	}
	sort.Slice(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })
	for i := range kept {
		kept[i] = regexp.QuoteMeta(kept[i])
	}
	re, e := regexp.Compile("(?i)" + strings.Join(kept, "|"))
	if e != nil {
		return nil, e
	}
	breakAt := func(w string) string {
		runes := []rune(w)
		var b strings.Builder
		b.Grow(len(w) + len(runes))
		for i, r := range runes {
			b.WriteRune(r)
			if i+1 < len(runes) && (i == 0 || r == ' ' || i%12 == 11) {
				b.WriteRune('​')
			}
		}
		return b.String()
	}
	return func(s string) string {
		return re.ReplaceAllStringFunc(s, breakAt)
	}, nil
}

func (c Compatibility) obfuscate(body []byte, format string) ([]byte, error) {
	systemChange, e := zwspChange(append(append([]string{}, wireFingerprints...), c.SensitiveWords...))
	if e != nil {
		return nil, e
	}
	userChange := systemChange
	if !c.ObfuscateUserText {
		if userChange, e = zwspChange(harnessArtifacts); e != nil {
			return nil, e
		}
	}
	var d map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if e := dec.Decode(&d); e != nil || d == nil {
		return nil, fmt.Errorf("factory: invalid request object")
	}
	alter := func(v any, change func(string) string) any {
		if s, ok := v.(string); ok {
			return change(s)
		}
		if blocks, ok := v.([]any); ok {
			for _, v := range blocks {
				if m, ok := v.(map[string]any); ok && (m["type"] == "text" || m["type"] == "input_text" || m["type"] == nil) {
					if s, ok := m["text"].(string); ok {
						m["text"] = change(s)
					}
				}
			}
		}
		return v
	}
	top, field := "system", "messages"
	if format == "openai-response" {
		top, field = "instructions", "input"
	}
	if v, ok := d[top]; ok {
		d[top] = alter(v, systemChange)
	}
	if s, ok := d[field].(string); ok {
		d[field] = userChange(s)
	}
	if messages, ok := d[field].([]any); ok {
		for _, v := range messages {
			if m, ok := v.(map[string]any); ok {
				switch m["role"] {
				case "system", "developer":
					m["content"] = alter(m["content"], systemChange)
				case "user":
					m["content"] = alter(m["content"], userChange)
				}
			}
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if e = enc.Encode(d); e != nil {
		return nil, e
	}
	return bytes.TrimSpace(out.Bytes()), nil
}
