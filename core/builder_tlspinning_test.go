package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/llmtls"
	"github.com/v0lka/sp4rk/llm"
)

const tlsChatCompletionsBody = `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

const tlsModelsBody = `{"object":"list","data":[{"id":"qwen3","object":"model","created":1,"owned_by":"x"}]}`

// chatReq builds a minimal chat request for the composite model id.
func chatReq(model string) llm.ChatRequest {
	return llm.ChatRequest{
		Model:    model,
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	}
}

// newTLSLLMServer spins an httptest TLS server answering both the chat
// completions and the /v1/models listing endpoints with a self-signed cert.
func newTLSLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(tlsModelsBody))
			return
		}
		_, _ = w.Write([]byte(tlsChatCompletionsBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestBuildRouter_PerProviderTLSPinned verifies the chat path: a provider
// configured with a SPKI pin gets its own pinned HTTP
// client, so the router successfully calls a self-signed HTTPS endpoint —
// while the same endpoint is unreachable without the override.
func TestBuildRouter_PerProviderTLSPinned(t *testing.T) {
	srv := newTLSLLMServer(t)
	pin := llmtls.SPKIFingerprint(srv.Certificate())

	b, err := NewOrchestratorBuilder(&BuilderConfig{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder: %v", err)
	}

	noexpand := func(s string) string { return s }

	cfg := &BuilderConfig{
		ExpandEnvVars: noexpand,
		LLM: BuilderLLMConfig{
			DefaultModel: "selfhosted/qwen3",
			ProviderConfigs: map[string]BuilderProviderConfig{
				"selfhosted": {
					ProviderType:   "openai",
					BaseURL:        srv.URL + "/v1",
					APIKey:         "k",
					Models:         []string{"qwen3"},
					TLSFingerprint: pin,
				},
			},
		},
		Timeouts: BuilderTimeoutsConfig{LLMRequestTimeout: 10},
	}

	router, _, err := b.buildRouter(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildRouter: %v", err)
	}

	// The pinned entry must carry its own client...
	// (indirect assertion: the call succeeds against the self-signed server)
	if _, err := router.Call(context.Background(), chatReq("selfhosted/qwen3")); err != nil {
		t.Fatalf("pinned provider call failed against self-signed server: %v", err)
	}

	// Same endpoint WITHOUT the override: default verification rejects the
	// self-signed certificate — proving the pin client was the enabler.
	cfgPlain := &BuilderConfig{
		ExpandEnvVars: noexpand,
		LLM: BuilderLLMConfig{
			DefaultModel: "selfhosted/qwen3",
			ProviderConfigs: map[string]BuilderProviderConfig{
				"selfhosted": {
					ProviderType: "openai",
					BaseURL:      srv.URL + "/v1",
					APIKey:       "k",
					Models:       []string{"qwen3"},
				},
			},
		},
		Timeouts: BuilderTimeoutsConfig{LLMRequestTimeout: 10},
	}
	routerPlain, _, err := b.buildRouter(context.Background(), cfgPlain)
	if err != nil {
		t.Fatalf("buildRouter (plain): %v", err)
	}
	if _, err := routerPlain.Call(context.Background(), chatReq("selfhosted/qwen3")); err == nil {
		t.Fatal("expected plain provider call to fail against self-signed server")
	}
}

// TestFetchProviderModels_TLSOverride verifies the model-listing path
// ("Fetch Models"): with the provider's SPKI pin the listing succeeds
// against a self-signed endpoint; without it, the fetch errors.
func TestFetchProviderModels_TLSOverride(t *testing.T) {
	srv := newTLSLLMServer(t)
	pin := llmtls.SPKIFingerprint(srv.Certificate())

	b, err := NewOrchestratorBuilder(&BuilderConfig{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder: %v", err)
	}

	cfgOK := &BuilderConfig{
		ExpandEnvVars: func(s string) string { return s },
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"selfhosted": {
					ProviderType:   "openai",
					BaseURL:        srv.URL + "/v1",
					APIKey:         "k",
					Models:         []string{"qwen3"},
					TLSFingerprint: pin,
				},
			},
		},
	}
	names, err := b.fetchProviderModels(context.Background(), "selfhosted", cfgOK)
	if err != nil {
		t.Fatalf("expected pinned listing to succeed, got: %v", err)
	}
	if len(names) != 1 || names[0] != "qwen3" {
		t.Fatalf("unexpected model list: %v", names)
	}

	cfgFail := &BuilderConfig{
		ExpandEnvVars: func(s string) string { return s },
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"selfhosted": {
					ProviderType: "openai",
					BaseURL:      srv.URL + "/v1",
					APIKey:       "k",
					Models:       []string{"qwen3"},
				},
			},
		},
	}
	if _, err := b.fetchProviderModels(context.Background(), "selfhosted", cfgFail); err == nil {
		t.Fatal("expected plain listing to fail against self-signed server")
	}
}

// TestFetchProviderModels_TLSOverrideAnthropic runs the same TLS-override
// scenario through the anthropic-compatible branch (raw GET {base}/v1/models
// with x-api-key headers): with the server's pin the listing succeeds, and
// without the override the anthropic path silently falls back to the
// built-in Claude list instead of erroring.
func TestFetchProviderModels_TLSOverrideAnthropic(t *testing.T) {
	srv := newTLSLLMServer(t)

	b, err := NewOrchestratorBuilder(&BuilderConfig{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder: %v", err)
	}

	pin := llmtls.SPKIFingerprint(srv.Certificate())

	cfgOK := &BuilderConfig{
		ExpandEnvVars: func(s string) string { return s },
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"claude-proxy": {
					ProviderType:   "anthropic",
					BaseURL:        srv.URL,
					APIKey:         "k",
					Models:         []string{"claude-sonnet-4-5"},
					TLSFingerprint: pin,
				},
			},
		},
	}
	names, err := b.fetchProviderModels(context.Background(), "claude-proxy", cfgOK)
	if err != nil {
		t.Fatalf("expected pinned anthropic listing to succeed, got: %v", err)
	}
	if len(names) != 1 || names[0] != "qwen3" {
		t.Fatalf("unexpected anthropic model list: %v", names)
	}

	// Without the override the TLS handshake fails; the anthropic branch
	// degrades to the built-in list rather than surfacing the error.
	cfgFail := &BuilderConfig{
		ExpandEnvVars: func(s string) string { return s },
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"claude-proxy": {
					ProviderType: "anthropic",
					BaseURL:      srv.URL,
					APIKey:       "k",
					Models:       []string{"claude-sonnet-4-5"},
				},
			},
		},
	}
	fallback, err := b.fetchProviderModels(context.Background(), "claude-proxy", cfgFail)
	if err != nil {
		t.Fatalf("anthropic listing must fall back to the built-in list, got error: %v", err)
	}
	builtin := llm.BuiltInModelNames("anthropic-api")
	if len(fallback) == 0 || len(fallback) != len(builtin) {
		t.Fatalf("expected fallback to the built-in anthropic list (%d entries), got: %v", len(builtin), fallback)
	}
}

// --- Proxy-wins rule (ADR-051) ---------------------------------------------

// pinnedTransportPresent reports whether client's transport chain carries a
// TLS client config with a VerifyPeerCertificate callback (the llmtls pinned
// transport marker). Shared by the proxy-wins assertions below.
func pinnedTransportPresent(client *http.Client) bool {
	if client == nil || client.Transport == nil {
		return false
	}
	ht, ok := client.Transport.(*http.Transport)
	if !ok {
		return false
	}
	return ht.TLSClientConfig != nil && ht.TLSClientConfig.VerifyPeerCertificate != nil
}

// providerDoGet performs a context-bound GET and closes the body; shared by
// the proxy-wins tests below.
func providerDoGet(t *testing.T, httpClient *http.Client, target string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return nil
}

// TestProviderEntry_ProxyWinsOverPin is the direct assertion of the
// proxy-wins rule (ADR-051) at the entry-construction layer: with an active
// proxy the pin is inert — providerEntryFromConfig attaches NO per-entry
// HTTPClient — while without a proxy the same config yields a pinned client
// (regression guard for ADR-050).
func TestProviderEntry_ProxyWinsOverPin(t *testing.T) {
	srv := newTLSLLMServer(t)
	pin := llmtls.SPKIFingerprint(srv.Certificate())
	noexpand := func(s string) string { return s }
	shared := &http.Client{}

	// Proxy active: pin is inert; the entry dials through the router's
	// shared (proxy-derived) client exactly as before the pin existed.
	entry := providerEntryFromConfig("selfhosted", BuilderProviderConfig{
		ProviderType:   "openai",
		BaseURL:        srv.URL + "/v1",
		APIKey:         "k",
		Models:         []string{"qwen3"},
		TLSFingerprint: pin,
	}, shared, true /* proxyActive */, noexpand, nil)
	if entry.HTTPClient != nil {
		t.Fatal("expected NO per-entry pinned client while a proxy is configured (proxy-wins rule)")
	}

	// Without a proxy the same config yields the pinned client (ADR-050).
	entryDirect := providerEntryFromConfig("selfhosted", BuilderProviderConfig{
		ProviderType:   "openai",
		BaseURL:        srv.URL + "/v1",
		APIKey:         "k",
		Models:         []string{"qwen3"},
		TLSFingerprint: pin,
	}, shared, false /* proxyActive */, noexpand, nil)
	if !pinnedTransportPresent(entryDirect.HTTPClient) {
		t.Fatal("expected a pinned per-entry client without a proxy")
	}
	if err := providerDoGet(t, entryDirect.HTTPClient, srv.URL+"/v1/models"); err != nil {
		t.Fatalf("pinned client failed against self-signed server: %v", err)
	}
}

// TestFetchProviderModels_ProxyWinsOverPin is the behavioral check of the
// proxy-wins rule (ADR-051) on the Fetch Models path. The builder's proxy
// client is a custom RoundTripper standing in for the configured proxy: it
// answers with the model listing without touching the origin. With the pin
// configured AND the proxy active, the listing must succeed THROUGH the
// proxy client — llmtls.Client would have replaced this custom RoundTripper
// with a default-transport clone that dials the self-signed origin directly
// and fails, so success here proves the pin was not applied.
func TestFetchProviderModels_ProxyWinsOverPin(t *testing.T) {
	srv := newTLSLLMServer(t)
	pin := llmtls.SPKIFingerprint(srv.Certificate())

	rt := &recordingTransport{body: tlsModelsBody}
	proxyClient := &http.Client{Transport: rt}

	b, err := NewOrchestratorBuilder(&BuilderConfig{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewOrchestratorBuilder: %v", err)
	}
	b.mu.Lock()
	b.proxyClient = proxyClient
	b.mu.Unlock()

	cfg := &BuilderConfig{
		ExpandEnvVars: func(s string) string { return s },
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"selfhosted": {
					ProviderType:   "openai",
					BaseURL:        srv.URL + "/v1",
					APIKey:         "k",
					Models:         []string{"qwen3"},
					TLSFingerprint: pin,
				},
			},
		},
	}

	names, err := b.fetchProviderModels(context.Background(), "selfhosted", cfg)
	if err != nil {
		t.Fatalf("listing must go through the proxy client with the pin ignored (proxy-wins), got: %v", err)
	}
	if len(names) != 1 || names[0] != "qwen3" {
		t.Fatalf("unexpected model list: %v", names)
	}
	if rt.calls != 1 {
		t.Fatalf("expected the proxy transport to be used exactly once, got %d", rt.calls)
	}
}

// recordingTransport is a RoundTripper stand-in for a configured proxy: it
// counts calls and answers every request with a fixed body.
type recordingTransport struct {
	body  string
	calls int
}

func (rt *recordingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	rt.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(rt.body)),
	}, nil
}
