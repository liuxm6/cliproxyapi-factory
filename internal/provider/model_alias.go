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
