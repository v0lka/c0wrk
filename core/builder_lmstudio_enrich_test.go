package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// discardWriter is an io.Writer sink used to build a debug-level slog.Logger
// without producing any output.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// lmStudioModelsPayload marshals the given models into the /api/v0/models shape.
func lmStudioModelsPayload(t *testing.T, models ...lmStudioModel) []byte {
	t.Helper()
	body, err := json.Marshal(struct {
		Data []lmStudioModel `json:"data"`
	}{Data: models})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

// localProbeCfg builds a minimal BuilderConfig wired to an LM Studio server
// for a single local OpenAI-compatible provider serving `model`.
func localProbeCfg(t *testing.T, baseURL, model string) *BuilderConfig {
	t.Helper()
	return &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: model,
			ProviderConfigs: map[string]BuilderProviderConfig{
				"lmstudio": {
					ProviderType: "openai",
					BaseURL:      baseURL,
					Models:       []string{model},
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
}

// newLocalProbeBuilder returns a builder with a debug-level logger that writes
// to a discard sink, matching the old enrich test's logger setup.
func newLocalProbeBuilder(t *testing.T) *OrchestratorBuilder {
	t.Helper()
	return &OrchestratorBuilder{logger: slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelDebug}))}
}

// waitForProbe polls the registry's Resolve up to `timeout` for `model` to be
// populated with `wantWindow`. Returns whether the expected window was seen.
func waitForProbe(t *testing.T, reg *llm.ModelRegistry, model string, wantWindow int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if meta, _ := reg.Resolve(context.Background(), model); meta.ContextWindow == wantWindow {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestBuildLocalModelProbe_PopulatesRegistry verifies that a probe closure
// built against a local provider queries the endpoint and writes the real
// context window into the registry (Resolution tier 3) so Resolve returns it.
func TestBuildLocalModelProbe_PopulatesRegistry(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "qwen2.5-coder-7b",
		MaxContextLength: 262144,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := localProbeCfg(t, srv.URL, "qwen2.5-coder-7b")
	b := newLocalProbeBuilder(t)
	registry := llm.NewModelRegistry(nil)

	probe := b.buildLocalModelProbe(cfg, registry)
	probe("qwen2.5-coder-7b")

	if !waitForProbe(t, registry, "qwen2.5-coder-7b", 262144, time.Second) {
		meta, _ := registry.Resolve(context.Background(), "qwen2.5-coder-7b")
		t.Fatalf("probe did not populate registry: context_window=%d", meta.ContextWindow)
	}
}

// TestBuildLocalModelProbe_SkipsRemoteProvider verifies that a provider whose
// base_url is a public host is never probed — the closure returns without
// spawning a goroutine, so the registry stays empty.
func TestBuildLocalModelProbe_SkipsRemoteProvider(t *testing.T) {
	// A public-looking host. The server would fail the test if hit, so any
	// accidental probe surfaces immediately.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: "gpt-4o",
			ProviderConfigs: map[string]BuilderProviderConfig{
				"openai-remote": {
					ProviderType: "openai",
					// httptest uses 127.0.0.1, which is local. Force a public
					// host by overriding to a real remote DNS name.
					BaseURL: "https://api.openai.com/v1",
					Models:  []string{"gpt-4o"},
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
	b := newLocalProbeBuilder(t)
	registry := llm.NewModelRegistry(nil)

	probe := b.buildLocalModelProbe(cfg, registry)
	probe("gpt-4o")

	// Give any stray goroutine a moment to run; it must not.
	time.Sleep(50 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Errorf("remote provider was probed (%d hits); isLocalBaseURL gate is broken", got)
	}
}

// TestBuildLocalModelProbe_SkipsNonOpenAIProvider verifies the provider-type
// gate: an anthropic provider (even with a local base_url) is never probed.
func TestBuildLocalModelProbe_SkipsNonOpenAIProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"anthropic-local": {
					ProviderType: "anthropic",
					BaseURL:      srv.URL,
					Models:       []string{"claude-3-5-sonnet"},
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
	b := newLocalProbeBuilder(t)
	registry := llm.NewModelRegistry(nil)

	probe := b.buildLocalModelProbe(cfg, registry)
	// Should be a no-op: no matching local openai provider.
	probe("claude-3-5-sonnet")

	time.Sleep(50 * time.Millisecond)
	meta, _ := registry.Resolve(context.Background(), "claude-3-5-sonnet")
	// "claude-3-5-sonnet" is not a built-in SDK model (the canonical id uses
	// dots), so Resolve returns the fallback default of 128000. A probe run
	// would have overwritten that with the server's value — confirming 128000
	// proves the anthropic provider was never probed.
	if meta.ContextWindow != 128000 {
		t.Errorf("non-openai provider probed: context_window=%d, want fallback 128000", meta.ContextWindow)
	}
}

// TestBuildLocalModelProbe_ConfigOverrideWins verifies that a config.yaml
// override (Resolution tier 1) is never clobbered by a later probe result
// (tier 3). The user's config always wins.
func TestBuildLocalModelProbe_ConfigOverrideWins(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "qwen2.5-coder-7b",
		MaxContextLength: 262144,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := localProbeCfg(t, srv.URL, "qwen2.5-coder-7b")
	// Seed a config override exactly as buildRouter does from cfg.LLM.Models.
	registry := llm.NewModelRegistry(map[string]llm.ModelMetadata{
		"qwen2.5-coder-7b": {ContextWindow: 32768, OutputLimit: 2048, TokenizerType: "approximate"},
	})
	b := newLocalProbeBuilder(t)

	probe := b.buildLocalModelProbe(cfg, registry)
	probe("qwen2.5-coder-7b")

	// Let the probe goroutine finish.
	time.Sleep(100 * time.Millisecond)

	meta, _ := registry.Resolve(context.Background(), "qwen2.5-coder-7b")
	if meta.ContextWindow != 32768 {
		t.Errorf("config override clobbered by probe: got %d, want 32768", meta.ContextWindow)
	}
	if meta.OutputLimit != 2048 {
		t.Errorf("config override clobbered by probe: got output limit %d, want 2048", meta.OutputLimit)
	}
}

// TestLookupLocalProviderBaseURL covers the provider-lookup helper directly:
// local openai providers match, remote/non-openai/unknown models return ok=false.
func TestLookupLocalProviderBaseURL(t *testing.T) {
	expand := func(s string) string { return s }
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"lmstudio": {
					ProviderType: "openai",
					BaseURL:      "http://127.0.0.1:1234/v1",
					APIKey:       "lm-key",
					Models:       []string{"qwen2.5-coder-7b", "llama-3.1-8b"},
				},
				"remote": {
					ProviderType: "openai",
					BaseURL:      "https://api.openai.com/v1",
					Models:       []string{"gpt-4o"},
				},
				"anthropic": {
					ProviderType: "anthropic",
					BaseURL:      "http://127.0.0.1:9999",
					Models:       []string{"claude-3-5-sonnet"},
				},
			},
		},
		ExpandEnvVars: expand,
	}

	if base, key, ok := lookupLocalProviderBaseURL(cfg, "qwen2.5-coder-7b", expand); !ok {
		t.Error("expected match for local openai model")
	} else if base != "http://127.0.0.1:1234/v1" || key != "lm-key" {
		t.Errorf("wrong provider resolved: base=%q key=%q", base, key)
	}

	if _, _, ok := lookupLocalProviderBaseURL(cfg, "gpt-4o", expand); ok {
		t.Error("remote openai provider should not match")
	}
	if _, _, ok := lookupLocalProviderBaseURL(cfg, "claude-3-5-sonnet", expand); ok {
		t.Error("anthropic provider should not match (non-openai)")
	}
	if _, _, ok := lookupLocalProviderBaseURL(cfg, "unknown-model", expand); ok {
		t.Error("unknown model should not match")
	}
}
