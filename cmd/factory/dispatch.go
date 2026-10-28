package main

import (
	"context"
	"encoding/json"
	"errors"
	"factory.local/cliproxyapi-factory/internal/provider"
	"factory.local/cliproxyapi-factory/internal/redact"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var pluginService = &provider.Service{Host: hostTransport{}, Compatibility: currentCompatibility}

func registration() any {
	return map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"metadata": pluginapi.Metadata{
			Name:             "Factory provider",
			Version:          "0.1.2",
			Author:           "leewhite97",
			GitHubRepository: "https://github.com/leewhite97/cliproxyapi-factory",
		},
		"capabilities": map[string]any{
			"auth_provider":           true,
			"model_provider":          true,
			"executor":                true,
			"executor_model_scope":    "oauth",
			"executor_input_formats":  []string{"openai", "openai-response", "claude"},
			"executor_output_formats": []string{"openai", "openai-response", "claude"},
			"quota_provider":          true,
			"management_api":          true,
		},
	}
}
func dispatch(method string, b []byte) (any, error) {
	ctx := context.Background()
	switch method {
	case pluginabi.MethodManagementRegister:
		return pluginapi.ManagementRegistrationResponse{Resources: []pluginapi.ResourceRoute{{Path: "/quota", Menu: "Factory Provider", Description: "Factory 模型集成、额度监控与敏感词策略"}, {Path: "/quota.js"}, {Path: "/panel-auth.js"}}}, nil
	case pluginabi.MethodManagementHandle:
		return managementResource(b)
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(b); err != nil {
			return nil, err
		}
		return registration(), nil
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier, pluginabi.MethodQuotaIdentifier:
		return map[string]any{"identifier": "factory"}, nil
	case pluginabi.MethodQuotaDescribe:
		return pluginapi.QuotaDescribeResponse{
			SupportedProviders: []string{"factory"},
			DisplayName:        "Factory",
			SupportsReset:      false,
		}, nil
	case pluginabi.MethodQuotaFetch:
		var r struct {
			pluginapi.QuotaFetchRequest
			HostCallbackID string `json:"host_callback_id,omitempty"`
		}
		if json.Unmarshal(b, &r) != nil {
			return nil, fmt.Errorf("invalid quota fetch request")
		}
		return pluginService.FetchQuota(ctx, r.HostCallbackID, r.StorageJSON)
	case pluginabi.MethodQuotaReset:
		return nil, &provider.StatusError{Code: "not_supported", Message: "factory: quota reset is not supported", Status: 400}
	case pluginabi.MethodModelStatic:
		return pluginapi.ModelResponse{Provider: "factory", Models: []pluginapi.ModelInfo{}}, nil
	case pluginabi.MethodModelForAuth:
		var r pluginapi.AuthModelRequest
		if json.Unmarshal(b, &r) != nil {
			return nil, fmt.Errorf("invalid model request")
		}
		parsed, e := provider.ParseAuth(pluginapi.AuthParseRequest{RawJSON: r.StorageJSON})
		if e != nil || !parsed.Handled {
			return nil, fmt.Errorf("factory: invalid model credential")
		}
		configMu.RLock()
		catalog := modelCatalog
		configMu.RUnlock()
		return provider.ApplyHostModelRules(catalog.Get(ctx), r.Host), nil
	case pluginabi.MethodAuthParse:
		var r pluginapi.AuthParseRequest
		if json.Unmarshal(b, &r) != nil {
			return nil, fmt.Errorf("invalid auth request")
		}
		return provider.ParseAuth(r)
	case pluginabi.MethodAuthRefresh:
		var r pluginapi.AuthRefreshRequest
		if json.Unmarshal(b, &r) != nil {
			return nil, fmt.Errorf("invalid refresh request")
		}
		p, e := provider.ParseAuth(pluginapi.AuthParseRequest{RawJSON: r.StorageJSON})
		if e != nil {
			return nil, e
		}
		p.Auth.ID = r.AuthID
		return pluginapi.AuthRefreshResponse{Auth: p.Auth}, nil
	case pluginabi.MethodExecutorExecute:
		var r provider.Request
		if json.Unmarshal(b, &r) != nil {
			return nil, fmt.Errorf("invalid execution request")
		}
		return pluginService.Execute(ctx, r)
	case pluginabi.MethodExecutorExecuteStream:
		var r provider.Request
		if json.Unmarshal(b, &r) != nil {
			return nil, fmt.Errorf("invalid streaming request")
		}
		h, e := pluginService.ExecuteStream(ctx, r)
		return map[string]any{"headers": h}, e
	default:
		return nil, &provider.StatusError{Code: "not_implemented", Message: "factory: operation not implemented: " + method, Status: 501}
	}
}
func handleMethod(method string, b []byte) ([]byte, bool) {
	v, e := dispatch(method, b)
	if e != nil {
		var status *provider.StatusError
		if errors.As(e, &status) {
			return errorEnvelope(status.Code, redact.Text(status.Message), status.Status, status.Retryable), true
		}
		return errorEnvelope("factory_error", redact.Text(e.Error()), 502, false), true
	}
	raw, e := okEnvelope(v)
	if e != nil {
		return errorEnvelope("encoding_error", "factory: response encoding failed", 500, false), true
	}
	return raw, false
}
func okEnvelope(v any) ([]byte, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: raw})
}
func errorEnvelope(code, message string, status int, retry bool) []byte {
	b, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: code, Message: message, HTTPStatus: status, Retryable: retry}})
	return b
}
