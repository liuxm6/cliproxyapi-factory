package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"factory.local/cliproxyapi-factory/internal/protocol"
	"factory.local/cliproxyapi-factory/internal/redact"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type Request struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id"`
	HostCallbackID string `json:"host_callback_id"`
}
type Transport interface {
	Do(context.Context, string, string, string, http.Header, []byte) (int, http.Header, []byte, error)
}
type Service struct {
	Host          Transport
	Compatibility func() protocol.Compatibility
	once          sync.Once
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	streamMu      sync.Mutex
	stopped       bool
	streams       map[string]StreamHost
}

func (s *Service) init() {
	s.once.Do(func() {
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.streams = make(map[string]StreamHost)
	})
}
func (s *Service) Shutdown() {
	s.init()
	s.streamMu.Lock()
	s.stopped = true
	s.cancel()
	active := make(map[string]StreamHost, len(s.streams))
	for id, host := range s.streams {
		active[id] = host
	}
	s.streamMu.Unlock()
	for id, host := range active {
		host.Close(context.Background(), id)
	}
	s.wg.Wait()
}

type Credential struct {
	Type     string `json:"type"`
	Key      string `json:"api_key"`
	Org      string `json:"orgId,omitempty"`
	Endpoint string `json:"apiEndpoint,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Label    string `json:"label,omitempty"`
	// proxy_url is honored by the host only on executor traffic. quota_proxy_url
	// is plugin-local: FetchQuota dials it directly because the host callback
	// never receives the credential proxy (upstream host gap).
	ProxyURL      string `json:"proxy_url,omitempty"`
	QuotaProxyURL string `json:"quota_proxy_url,omitempty"`
}
type StatusError struct {
	Code, Message string
	Status        int
	Retryable     bool
}

func (e *StatusError) Error() string { return e.Message }
func credential(raw []byte) (Credential, error) {
	var c Credential
	if json.Unmarshal(raw, &c) != nil || c.Type != "factory" || !strings.HasPrefix(c.Key, "fk-") {
		return c, &StatusError{"invalid_credential", "factory: valid API-key credential required", 401, false}
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://api.factory.ai"
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Host != "api.factory.ai" && u.Host != "api.eu.factory.ai") {
		return c, &StatusError{"invalid_endpoint", "factory: unsupported credential endpoint", 400, false}
	}
	c.Endpoint = strings.TrimRight(c.Endpoint, "/")
	return c, nil
}

var registry = builtin.Registry()

func (s *Service) prepare(r Request, stream bool) (Credential, protocol.Route, string, []byte, error) {
	c, e := credential(r.StorageJSON)
	if e != nil {
		return c, protocol.Route{}, "", nil, e
	}
	model := strings.TrimPrefix(r.Model, "factory/")
	route, e := protocol.Resolve(model)
	if e != nil {
		return c, route, model, nil, &StatusError{"unsupported_model", e.Error(), 404, false}
	}
	from := r.Format
	if from == "" {
		from = r.SourceFormat
	}
	body := r.Payload
	if len(body) == 0 {
		body = r.OriginalRequest
	}
	b, e := translateRequest(from, route.Format, model, body, stream)
	if e != nil {
		return c, route, model, nil, e
	}
	// OriginalRequest carries the client's intent even when the host has already
	// converted Payload to a different executor protocol.
	intent, intentFormat := body, from
	if len(r.OriginalRequest) > 0 {
		intent = r.OriginalRequest
		if r.SourceFormat != "" {
			intentFormat = r.SourceFormat
		}
	}
	b, e = normalizeFactoryParameters(model, intentFormat, route.Format, intent, b)
	if e != nil {
		return c, route, model, nil, e
	}
	if route.Format == "claude" {
		b, e = normalizeClaudeParameters(model, intentFormat, intent, b)
		if e != nil {
			return c, route, model, nil, e
		}
	}
	compat := protocol.Compatibility{}
	if s.Compatibility != nil {
		compat = s.Compatibility()
	}
	b, e = protocol.GatewayRequestWithCompatibility(b, route.Format, compat)
	return c, route, model, b, e
}
func translateRequest(from, to, model string, body []byte, stream bool) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("factory: invalid request JSON")
	}
	var out []byte
	if from == to {
		out = append([]byte(nil), body...)
	} else {
		f, t := tr.FromString(from), tr.FromString(to)
		if to == "openai-response" && !registry.HasRequestTransformer(f, t) {
			t = tr.FormatCodex
		}
		if registry.HasRequestTransformer(f, t) {
			out = registry.TranslateRequest(f, t, model, body, stream)
		} else {
			mid := tr.FormatOpenAI
			if !registry.HasRequestTransformer(f, mid) || !registry.HasRequestTransformer(mid, t) {
				return nil, fmt.Errorf("factory: unsupported translation %s -> %s", from, to)
			}
			out = registry.TranslateRequest(f, mid, model, body, stream)
			out = registry.TranslateRequest(mid, t, model, out, stream)
		}
	}
	var d map[string]any
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if dec.Decode(&d) != nil || d == nil {
		return nil, fmt.Errorf("factory: request translation failed")
	}
	d["model"] = model
	d["stream"] = stream
	return json.Marshal(d)
}
func response(ctx context.Context, from, to, model string, original, translated, body []byte) ([]byte, error) {
	var err error
	body, err = nativeNonStreamInput(from, to, body)
	if err != nil {
		return nil, err
	}
	if from == to {
		return body, nil
	}
	f, t := tr.FromString(from), tr.FromString(to)
	if from == "openai-response" && !registry.HasNonStreamResponseTransformer(t, f) {
		f = tr.FormatCodex
	}
	if !registry.HasNonStreamResponseTransformer(t, f) {
		return nil, fmt.Errorf("factory: unsupported response translation %s -> %s", from, to)
	}
	out := registry.TranslateNonStream(ctx, f, t, model, original, translated, body, nil)
	if !json.Valid(out) {
		return nil, fmt.Errorf("factory: response translation failed")
	}
	return out, nil
}
func upstreamError(status int, b []byte, key string) *StatusError {
	if len(b) > 4096 {
		b = b[:4096]
	}
	code := "upstream_failure"
	switch status {
	case 401:
		code = "credential_invalid"
	case 403:
		code = "gateway_forbidden"
	case 404:
		code = "model_or_endpoint_not_found"
	case 429:
		code = "rate_limit"
	}
	return &StatusError{code, fmt.Sprintf("factory upstream HTTP %d: %s", status, redact.Text(string(b), key)), status, status == 429 || status >= 500}
}
func (s *Service) Execute(ctx context.Context, r Request) (pluginapi.ExecutorResponse, error) {
	format := r.Format
	if format == "" {
		format = r.SourceFormat
	}
	resolved, errRoute := protocol.Resolve(strings.TrimPrefix(r.Model, "factory/"))
	useStream := errRoute == nil && resolved.Format != format && (resolved.Format == "claude" || resolved.Format == "openai-response")
	c, route, model, b, e := s.prepare(r, useStream)
	if e != nil {
		return pluginapi.ExecutorResponse{}, e
	}
	h := protocol.HeadersWithSession(c.Key, c.Org, route, requestSession(r))
	h.Set("Accept", "application/json")
	if useStream {
		h.Set("Accept", "text/event-stream")
	}
	status, _, body, e := s.Host.Do(ctx, "POST", r.HostCallbackID, c.Endpoint+route.Path, h, b)
	if e != nil {
		return pluginapi.ExecutorResponse{}, transportError(e, c.Key)
	}
	if status < 200 || status >= 300 {
		capture403(status, r, route, b, body, c.Key)
		return pluginapi.ExecutorResponse{}, upstreamError(status, body, c.Key)
	}
	out, e := response(ctx, route.Format, format, model, r.OriginalRequest, b, body)
	return pluginapi.ExecutorResponse{Payload: out}, e
}
