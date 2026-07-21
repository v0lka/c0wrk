package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// enrichCfg builds a minimal BuilderConfig wired to an LM Studio server so the
// enrich helper can be exercised in isolation.
func enrichCfg(t *testing.T, baseURL string, providerModels []string) *BuilderConfig {
	t.Helper()
	return &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"lmstudio": {
					ProviderType: "openai",
					BaseURL:      baseURL,
					Models:       providerModels,
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
}

func newEnrichBuilder(t *testing.T) *OrchestratorBuilder {
	t.Helper()
	return &OrchestratorBuilder{logger: slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelDebug}))}
}

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

// TestEnrichOverridesFromLMStudio_CapacityEnrichment verifies that an enabled
// model reported by LM Studio with only a capacity (no loaded instances) gets
// its real context window instead of an empty override.
func TestEnrichOverridesFromLMStudio_CapacityEnrichment(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "qwen2.5-coder-7b",
		MaxContextLength: 262144,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := enrichCfg(t, srv.URL, []string{"qwen2.5-coder-7b"})
	overrides := make(map[string]llm.ModelMetadata)

	newEnrichBuilder(t).enrichOverridesFromLMStudio(context.Background(), cfg, overrides, nil)

	got, ok := overrides["qwen2.5-coder-7b"]
	if !ok {
		t.Fatalf("expected qwen2.5-coder-7b in overrides, got: %v", overrides)
	}
	if got.ContextWindow != 262144 {
		t.Errorf("context window: got %d, want 262144", got.ContextWindow)
	}
	if got.OutputLimit != 4096 {
		t.Errorf("output limit: got %d, want 4096", got.OutputLimit)
	}
	if got.TokenizerType != "approximate" {
		t.Errorf("tokenizer type: got %q, want approximate", got.TokenizerType)
	}
}

// TestEnrichOverridesFromLMStudio_RuntimePriority verifies that a loaded model
// gets its runtime context_length (16384) rather than its capacity (262144).
func TestEnrichOverridesFromLMStudio_RuntimePriority(t *testing.T) {
	loaded := lmStudioModel{
		ID:               "llama-3.1-8b",
		MaxContextLength: 262144,
	}
	loaded.LoadedInstances = append(loaded.LoadedInstances, struct {
		Config struct {
			ContextLength int `json:"context_length"`
		} `json:"config"`
	}{})
	loaded.LoadedInstances[0].Config.ContextLength = 16384

	body := lmStudioModelsPayload(t, loaded)
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := enrichCfg(t, srv.URL, []string{"llama-3.1-8b"})
	overrides := make(map[string]llm.ModelMetadata)

	newEnrichBuilder(t).enrichOverridesFromLMStudio(context.Background(), cfg, overrides, nil)

	got := overrides["llama-3.1-8b"]
	if got.ContextWindow != 16384 {
		t.Errorf("runtime window: got %d, want 16384", got.ContextWindow)
	}
}

// TestEnrichOverridesFromLMStudio_ConfigYamlPriority verifies that a model
// already overridden via config.yaml (cfg.LLM.Models) is NEVER overwritten by
// the LM Studio probe — the user's config wins.
func TestEnrichOverridesFromLMStudio_ConfigYamlPriority(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "qwen2.5-coder-7b",
		MaxContextLength: 262144,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := enrichCfg(t, srv.URL, []string{"qwen2.5-coder-7b"})
	// Pre-populate overrides exactly as buildRouter does from cfg.LLM.Models.
	overrides := map[string]llm.ModelMetadata{
		"qwen2.5-coder-7b": {ContextWindow: 32768, OutputLimit: 2048, TokenizerType: "approximate"},
	}

	newEnrichBuilder(t).enrichOverridesFromLMStudio(context.Background(), cfg, overrides, nil)

	got := overrides["qwen2.5-coder-7b"]
	if got.ContextWindow != 32768 {
		t.Errorf("config.yaml override clobbered: got context window %d, want 32768", got.ContextWindow)
	}
	if got.OutputLimit != 2048 {
		t.Errorf("config.yaml override clobbered: got output limit %d, want 2048", got.OutputLimit)
	}
}

// TestEnrichOverridesFromLMStudio_NotFoundIsNonFatal verifies that a non-LM-Studio
// endpoint (404) does not panic, does not add overrides, and returns cleanly.
func TestEnrichOverridesFromLMStudio_NotFoundIsNonFatal(t *testing.T) {
	srv, _, _ := newLMStudioServer(t, http.StatusNotFound, []byte(`{"error":"not found"}`))

	cfg := enrichCfg(t, srv.URL, []string{"qwen2.5-coder-7b"})
	overrides := make(map[string]llm.ModelMetadata)

	// Must not panic; overrides must stay empty.
	newEnrichBuilder(t).enrichOverridesFromLMStudio(context.Background(), cfg, overrides, nil)

	if len(overrides) != 0 {
		t.Errorf("expected no overrides from 404 endpoint, got %d: %v", len(overrides), overrides)
	}
}

// TestEnrichOverridesFromLMStudio_SkipsNonOpenAI verifies that only providers
// with ProviderType "openai" are probed.
func TestEnrichOverridesFromLMStudio_SkipsNonOpenAI(t *testing.T) {
	// Server that would fail the test if hit (404 path still logs, so we make
	// it return a 500 to be obviously wrong if probed).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"anthropic": {
					ProviderType: "anthropic",
					BaseURL:      srv.URL,
					Models:       []string{"claude-3-5-sonnet"},
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
	overrides := make(map[string]llm.ModelMetadata)

	newEnrichBuilder(t).enrichOverridesFromLMStudio(context.Background(), cfg, overrides, nil)

	if len(overrides) != 0 {
		t.Errorf("anthropic provider should not be probed, got overrides: %v", overrides)
	}
}

// TestEnrichOverridesFromLMStudio_SkipsEmptyBaseURL verifies that an "openai"
// provider with an empty BaseURL is skipped (no panic).
func TestEnrichOverridesFromLMStudio_SkipsEmptyBaseURL(t *testing.T) {
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"openai-no-base": {
					ProviderType: "openai",
					BaseURL:      "",
					Models:       []string{"gpt-4"},
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
	overrides := make(map[string]llm.ModelMetadata)

	newEnrichBuilder(t).enrichOverridesFromLMStudio(context.Background(), cfg, overrides, nil)

	if len(overrides) != 0 {
		t.Errorf("empty-baseURL provider should be skipped, got overrides: %v", overrides)
	}
}

// TestEnrichOverridesFromLMStudio_UnreportedModelSkipped verifies that a model
// enabled in config but not present in the LM Studio probe response is not
// added to overrides (we only enrich what LM Studio actually reports).
func TestEnrichOverridesFromLMStudio_UnreportedModelSkipped(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "qwen2.5-coder-7b",
		MaxContextLength: 262144,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	// "llama-3.1-8b" is enabled but not reported by the server.
	cfg := enrichCfg(t, srv.URL, []string{"qwen2.5-coder-7b", "llama-3.1-8b"})
	overrides := make(map[string]llm.ModelMetadata)

	newEnrichBuilder(t).enrichOverridesFromLMStudio(context.Background(), cfg, overrides, nil)

	if _, ok := overrides["qwen2.5-coder-7b"]; !ok {
		t.Errorf("expected reported model qwen2.5-coder-7b in overrides, got: %v", overrides)
	}
	if _, ok := overrides["llama-3.1-8b"]; ok {
		t.Errorf("unreported model llama-3.1-8b should not be in overrides, got: %v", overrides)
	}
}

// TestResolveLMStudioOverrides_CachesOnBuilder verifies that
// resolveLMStudioOverrides probes the endpoint once and stores the result on
// the builder (the cache that buildRouter reads via mergeLMStudioOverrides).
func TestResolveLMStudioOverrides_CachesOnBuilder(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "qwen2.5-coder-7b",
		MaxContextLength: 262144,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := enrichCfg(t, srv.URL, []string{"qwen2.5-coder-7b"})
	b := newEnrichBuilder(t)

	b.resolveLMStudioOverrides(context.Background(), cfg)

	b.mu.RLock()
	cached := b.lmStudioOverrides
	b.mu.RUnlock()
	got, ok := cached["qwen2.5-coder-7b"]
	if !ok {
		t.Fatalf("expected cached override for qwen2.5-coder-7b, got: %v", cached)
	}
	if got.ContextWindow != 262144 {
		t.Errorf("cached context window: got %d, want 262144", got.ContextWindow)
	}
}

// TestMergeLMStudioOverrides_ConfigWins verifies that an override already in
// the map (simulating a config.yaml entry) is never clobbered by the cached
// LM Studio probe result — config.yaml priority is enforced here.
func TestMergeLMStudioOverrides_ConfigWins(t *testing.T) {
	b := newEnrichBuilder(t)
	// Seed the cache as resolveLMStudioOverrides would.
	b.mu.Lock()
	b.lmStudioOverrides = map[string]llm.ModelMetadata{
		"qwen2.5-coder-7b": {ContextWindow: 262144, OutputLimit: 4096, TokenizerType: "approximate"},
	}
	b.mu.Unlock()

	// Simulate the config-derived overrides that buildRouter builds first.
	overrides := map[string]llm.ModelMetadata{
		"qwen2.5-coder-7b": {ContextWindow: 32768, OutputLimit: 2048, TokenizerType: "approximate"},
	}
	b.mergeLMStudioOverrides(overrides)

	got := overrides["qwen2.5-coder-7b"]
	if got.ContextWindow != 32768 {
		t.Errorf("config.yaml override clobbered: got context window %d, want 32768", got.ContextWindow)
	}
	if got.OutputLimit != 2048 {
		t.Errorf("config.yaml override clobbered: got output limit %d, want 2048", got.OutputLimit)
	}
}

// TestMergeLMStudioOverrides_AddsUncachedModels verifies that a model present
// in the cache but NOT in the config-derived overrides is layered in.
func TestMergeLMStudioOverrides_AddsUncachedModels(t *testing.T) {
	b := newEnrichBuilder(t)
	b.mu.Lock()
	b.lmStudioOverrides = map[string]llm.ModelMetadata{
		"qwen2.5-coder-7b": {ContextWindow: 262144, OutputLimit: 4096, TokenizerType: "approximate"},
		"llama-3.1-8b":     {ContextWindow: 16384, OutputLimit: 4096, TokenizerType: "approximate"},
	}
	b.mu.Unlock()

	overrides := map[string]llm.ModelMetadata{
		"qwen2.5-coder-7b": {ContextWindow: 32768, OutputLimit: 2048, TokenizerType: "approximate"},
	}
	b.mergeLMStudioOverrides(overrides)

	if got := overrides["qwen2.5-coder-7b"]; got.ContextWindow != 32768 {
		t.Errorf("config override clobbered: got %d, want 32768", got.ContextWindow)
	}
	if got := overrides["llama-3.1-8b"]; got.ContextWindow != 16384 {
		t.Errorf("uncached model not added: got context window %d, want 16384", got.ContextWindow)
	}
}

// TestEnrichOverridesFromLMStudio_5xxLoggedNotFatal verifies that a provider
// returning a 5xx is skipped (its error surfaces via the probe) without
// aborting enrichment of other providers or panicking.
func TestEnrichOverridesFromLMStudio_5xxLoggedNotFatal(t *testing.T) {
	// First provider: a 5xx (momentarily-unwell LM Studio).
	badSrv, _, _ := newLMStudioServer(t, http.StatusInternalServerError, []byte(`{"error":"boom"}`))
	// Second provider: a healthy LM Studio reporting a model.
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "llama-3.1-8b",
		MaxContextLength: 131072,
	})
	goodSrv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: map[string]BuilderProviderConfig{
				"lmstudio-broken": {
					ProviderType: "openai",
					BaseURL:      badSrv.URL,
					Models:       []string{"qwen2.5-coder-7b"},
				},
				"lmstudio-healthy": {
					ProviderType: "openai",
					BaseURL:      goodSrv.URL,
					Models:       []string{"llama-3.1-8b"},
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
	overrides := make(map[string]llm.ModelMetadata)

	newEnrichBuilder(t).enrichOverridesFromLMStudio(context.Background(), cfg, overrides, nil)

	// The broken provider must contribute nothing.
	if _, ok := overrides["qwen2.5-coder-7b"]; ok {
		t.Errorf("5xx provider should not add overrides, got: %v", overrides)
	}
	// The healthy provider must still be enriched.
	if got := overrides["llama-3.1-8b"]; got.ContextWindow != 131072 {
		t.Errorf("healthy provider not enriched: got context window %d, want 131072", got.ContextWindow)
	}
}
