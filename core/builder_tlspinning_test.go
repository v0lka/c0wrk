package core

import (
	"context"
	"net/http"
	"net/http/httptest"
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
