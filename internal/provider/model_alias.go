package provider

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// modelAliases 是别名反查表:alias id(小写)-> 上游裸模型 id。
// 每次 ModelsForAuth 应用 host 配置时全量重建。
var modelAliasMu sync.RWMutex
var modelAliases = map[string]string{}

// ApplyHostModelRules 在插件内应用 host 下发的模型规则。
// factory 凭证的 auth_kind 是 api_key,host 侧 applyOAuthModelAliasForAuth /
// applyExcludedModels 对 apikey 凭证直接跳过,所以 HostConfigSummary 里的
// OAuthModelAlias / ExcludedModels 需要插件自己执行,否则 Claude 桌面端这类
// 只显示 claude-* id 的客户端看不到非 Claude 模型。
//
// 别名语义固定为 fork:原名保留并额外注册一个别名条目(host 传给插件的
// ModelAlias 只有 Name/Alias,不带 fork 标记)。Name 可匹配 registered id
// (factory/x) 或裸模型名 (x)。
func ApplyHostModelRules(resp pluginapi.ModelResponse, host pluginapi.HostConfigSummary) pluginapi.ModelResponse {
	if len(resp.Models) == 0 {
		return resp
	}
	excluded := host.ExcludedModels["factory"]
	aliases := host.OAuthModelAlias["factory"]

	aliasByModel := make(map[string][]string, len(aliases))
	for _, a := range aliases {
		name := strings.TrimSpace(a.Name)
		alias := strings.TrimSpace(a.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		aliasByModel[strings.ToLower(name)] = append(aliasByModel[strings.ToLower(name)], alias)
	}

	next := make(map[string]string, len(resp.Models))
	out := make([]pluginapi.ModelInfo, 0, len(resp.Models)*2)
	seen := make(map[string]struct{}, len(resp.Models)*2)
	for _, m := range resp.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		name := strings.ToLower(strings.TrimSpace(m.Name))
		// 排除规则同时对 registered id 与裸模型名做通配匹配
		if matchesAnyPattern(excluded, key) || (name != "" && matchesAnyPattern(excluded, name)) {
			continue
		}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			out = append(out, m)
		}
		entries := aliasByModel[key]
		if name != "" {
			entries = append(entries, aliasByModel[name]...)
		}
		bare := strings.TrimPrefix(id, "factory/")
		for _, alias := range entries {
			akey := strings.ToLower(alias)
			if akey == key {
				continue
			}
			if _, dup := seen[akey]; dup {
				continue
			}
			seen[akey] = struct{}{}
			clone := m
			clone.ID = alias
			out = append(out, clone)
			next[akey] = bare
		}
	}

	modelAliasMu.Lock()
	modelAliases = next
	modelAliasMu.Unlock()
	resp.Models = out
	return resp
}

// resolveRequestModel 把请求里的模型名解析成上游裸模型 id:
// 先剥 factory/ 前缀;剩下的查别名反查表(claude-factory-* 这类)。
func resolveRequestModel(model string) string {
	if trimmed := strings.TrimPrefix(model, "factory/"); trimmed != model {
		return trimmed
	}
	modelAliasMu.RLock()
	mapped := modelAliases[strings.ToLower(model)]
	modelAliasMu.RUnlock()
	if mapped != "" {
		return mapped
	}
	return model
}

// displayNameFor 生成客户端展示名:catalog 给了名字就用,否则按裸名美化;
// 末尾统一 "(Factory)" 厂商标(已有 "(Factory…)" 标的保留,如 Factory Core),
// Claude 桌面端这类按 display_name 展示的客户端才能分出模型是哪家的。
func displayNameFor(spec ModelSpec) string {
	name := strings.TrimSpace(spec.Name)
	if name == "" || strings.EqualFold(name, spec.ID) {
		name = prettyModelName(spec.ID)
	}
	if !strings.Contains(name, "(Factory") {
		name = strings.TrimSpace(name + " (Factory)")
	}
	return name
}

// prettyBrands 是 token -> 展示拼写表;未收录的词走首字母大写。
var prettyBrands = map[string]string{
	"claude": "Claude", "opus": "Opus", "sonnet": "Sonnet", "haiku": "Haiku",
	"fable": "Fable", "gpt": "GPT", "glm": "GLM", "swe": "SWE",
	"deepseek": "DeepSeek", "kimi": "Kimi", "minimax": "MiniMax",
	"nemotron": "Nemotron", "codex": "Codex", "gemini": "Gemini", "grok": "Grok",
	"muse": "Muse", "astra": "Astra", "luna": "Luna", "sol": "Sol",
	"terra": "Terra", "argon": "Argon", "wonder": "Wonder", "pro": "Pro",
	"mini": "Mini", "max": "Max", "fast": "Fast", "flash": "Flash",
	"slow": "Slow", "lightning": "Lightning", "ultra": "Ultra", "super": "Super",
	"ultrafast": "Ultrafast", "high": "High", "medium": "Medium", "low": "Low",
	"thinking": "Thinking", "none": "None",
}

// hyphenVersionBrands 的品牌名与后跟版本号用连字符(GPT-5.4、GLM-5.2、SWE-1.6)。
var hyphenVersionBrands = map[string]bool{"GPT": true, "GLM": true, "SWE": true}

// prettyModelName 把裸模型 slug 转成可读名:
// 连续数字 token 合成版本号(claude-haiku-5-5 -> Claude Haiku 5.5),
// 短字母数字 token 全大写(k3->K3、v4.1->V4.1、1m->1M),品牌词查表。
func prettyModelName(id string) string {
	if i := strings.Index(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	tokens := strings.Split(id, "-")
	words := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if isDigits(t) && i+1 < len(tokens) && isDigits(tokens[i+1]) {
			ver := t
			for i+1 < len(tokens) && isDigits(tokens[i+1]) {
				i++
				ver += "." + tokens[i]
			}
			words = append(words, ver)
			continue
		}
		if w, ok := prettyBrands[strings.ToLower(t)]; ok {
			words = append(words, w)
			continue
		}
		if isDigits(t) {
			words = append(words, t)
			continue
		}
		if shortAlnum(t) {
			words = append(words, strings.ToUpper(t))
			continue
		}
		words = append(words, upperFirst(t))
	}
	// "GPT"+"5.4" -> "GPT-5.4"
	out := make([]string, 0, len(words))
	for i, w := range words {
		if hyphenVersionBrands[w] && i+1 < len(words) && looksLikeVersion(words[i+1]) {
			words[i+1] = w + "-" + words[i+1]
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// shortAlnum 匹配 v4.1、m3、k3、1m 这类 <=4 字符的字母+数字短 token。
func shortAlnum(s string) bool {
	if len(s) == 0 || len(s) > 4 {
		return false
	}
	hasLetter, hasDigit := false, false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	return hasLetter && hasDigit
}

func looksLikeVersion(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return s[0] >= '0' && s[0] <= '9'
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func matchesAnyPattern(patterns []string, value string) bool {
	for _, p := range patterns {
		if matchWildcard(strings.ToLower(strings.TrimSpace(p)), value) {
			return true
		}
	}
	return false
}

// matchWildcard 与 host applyExcludedModels 的通配语义一致:
// 无 * 精确匹配;*"分段"按顺序匹配,首段为前缀、末段为后缀、中段按序包含。
func matchWildcard(pattern, value string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}
	for i := 1; i < len(parts)-1; i++ {
		segment := parts[i]
		if segment == "" {
			continue
		}
		idx := strings.Index(value, segment)
		if idx < 0 {
			return false
		}
		value = value[idx+len(segment):]
	}
	return true
}
