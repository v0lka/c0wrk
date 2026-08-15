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
// to a discard sink, matching the old enrich test's logger setup. It injects a
// synchronous dispatcher (goAsync) so the detached network probe runs inline —
// the probe completes before the closure returns, so assertions are
// deterministic without any polling or sleeps.
func newLocalProbeBuilder(t *testing.T) *OrchestratorBuilder {
	t.Helper()
	return &OrchestratorBuilder{
		logger:  slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		goAsync: func(fn func()) { fn() },
	}
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

	probe := b.buildLocalModelProbe(cfg, registry, nil)
	// The synchronous dispatcher (injected via newLocalProbeBuilder) runs the
	// network probe inline, so by the time probe() returns the registry is
	// already populated — no polling or sleep required.
	probe("qwen2.5-coder-7b")

	meta, _ := registry.Resolve(context.Background(), "qwen2.5-coder-7b")
	if meta.ContextWindow != 262144 {
		t.Fatalf("probe did not populate registry: context_window=%d, want 262144", meta.ContextWindow)
	}
}

// TestBuildLocalModelProbe_ClampsOutputLimitToWindow verifies that a
// small-context local model gets an OutputLimit clamped relative to its window
// rather than the 32768 cap. Without the clamp, OutputLimit would exceed the
// context window, drive EffectiveMax negative, and silently disable compaction
// (CheckFill returns "ok" while FillPercent returns 100) — the exact regression
// flagged in the review. 8192/4 = 2048.
func TestBuildLocalModelProbe_ClampsOutputLimitToWindow(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "acme-local-7b",
		MaxContextLength: 8192,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := localProbeCfg(t, srv.URL, "acme-local-7b")
	b := newLocalProbeBuilder(t)
	registry := llm.NewModelRegistry(nil)

	probe := b.buildLocalModelProbe(cfg, registry, nil)
	probe("acme-local-7b")

	meta, _ := registry.Resolve(context.Background(), "acme-local-7b")
	if meta.ContextWindow != 8192 {
		t.Fatalf("context_window=%d, want 8192", meta.ContextWindow)
	}
	if meta.OutputLimit != 2048 {
		t.Errorf("OutputLimit not clamped to window/4: got %d, want 2048 (8192/4); "+
			"an unclamped 32768 would exceed the 8192 window and disable compaction", meta.OutputLimit)
	}
	// Sanity: EffectiveMax stays positive so compaction can trigger.
	if eff := meta.ContextWindow - meta.OutputLimit; eff <= 0 {
		t.Errorf("OutputLimit %d >= ContextWindow %d: EffectiveMax non-positive, compaction disabled",
			meta.OutputLimit, meta.ContextWindow)
	}
}

// TestBuildLocalModelProbe_OpenAIFallbackPopulatesRegistry verifies the OpenAI
// fallback path end-to-end through buildLocalModelProbe: when the LM Studio
// native endpoint is absent (404 on /api/v0/models) but the server exposes
// max_model_len on /v1/models, the discovered window is written to the
// registry (SetRuntimeMetadata) and surfaced by Resolve.
//
// Note: httptest binds 127.0.0.1, so this test does NOT by itself prove that a
// public-host provider is probed — the request is served by a loopback address
// regardless of the BaseURL string. The locality-not-gated property is
// asserted at the correct layer by TestLookupOpenAIProviderBaseURL, which
// matches a public-host openai provider without filtering by host locality.
func TestBuildLocalModelProbe_OpenAIFallbackPopulatesRegistry(t *testing.T) {
	// A discriminating server: 404 on the LM Studio native path, serves
	// max_model_len on /v1/models.
	body, err := json.Marshal(struct {
		Data []openAIModelEntry `json:"data"`
	}{
		Data: []openAIModelEntry{{ID: "Qwen/Qwen2.5-Coder-32B-Instruct", MaxModelLen: 131072}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	model := "Qwen/Qwen2.5-Coder-32B-Instruct"
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: model,
			// The server binds 127.0.0.1; what this exercises is the OpenAI
			// fallback path, not host-locality gating.
			ProviderConfigs: map[string]BuilderProviderConfig{
				"vllm-public": {
					ProviderType: "openai",
					BaseURL:      srv.URL + "/v1",
					Models:       []string{model},
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
	b := newLocalProbeBuilder(t)
	registry := llm.NewModelRegistry(nil)

	probe := b.buildLocalModelProbe(cfg, registry, nil)
	probe(model)

	meta, _ := registry.Resolve(context.Background(), model)
	if meta.ContextWindow != 131072 {
		t.Fatalf("OpenAI fallback did not populate registry: context_window=%d, want 131072", meta.ContextWindow)
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
					Models:       []string{"acme-test-anthropic-model"},
				},
			},
		},
		ExpandEnvVars: func(s string) string { return s },
	}
	b := newLocalProbeBuilder(t)
	registry := llm.NewModelRegistry(nil)

	probe := b.buildLocalModelProbe(cfg, registry, nil)
	// Should be a no-op: no matching local openai provider.
	probe("acme-test-anthropic-model")

	// The synchronous dispatcher runs any spawned probe inline, so by the time
	// probe() returns the registry already reflects any probe side effects.
	meta, _ := registry.Resolve(context.Background(), "acme-test-anthropic-model")
	// "acme-test-anthropic-model" is intentionally not a built-in SDK model —
	// including after separator-insensitive fuzzy matching — so Resolve returns
	// the fallback default of 128000. A probe run would have overwritten that
	// with the server's value — confirming 128000 proves the anthropic provider
	// was never probed.
	if meta.ContextWindow != 128000 {
		t.Errorf("non-openai provider probed: context_window=%d, want fallback 128000", meta.ContextWindow)
	}
}

// TestBuildLocalModelProbe_ConfigOverrideWins verifies that a config.yaml
// override (Resolution tier 1) is never clobbered by a later probe result
// (runtime tier 1.5). The user's config always wins.
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

	probe := b.buildLocalModelProbe(cfg, registry, nil)
	// The synchronous dispatcher runs the probe inline, so by the time probe()
	// returns the override-vs-cache precedence is already settled.
	probe("qwen2.5-coder-7b")

	meta, _ := registry.Resolve(context.Background(), "qwen2.5-coder-7b")
	if meta.ContextWindow != 32768 {
		t.Errorf("config override clobbered by probe: got %d, want 32768", meta.ContextWindow)
	}
	if meta.OutputLimit != 2048 {
		t.Errorf("config override clobbered by probe: got output limit %d, want 2048", meta.OutputLimit)
	}
}

// TestBuildLocalModelProbe_RuntimeWindowBeatsBuiltinCatalog is the regression
// test for the self-hosted context-window bug: a well-known checkpoint served
// by LM Studio at a RUNTIME context length below the catalog maximum. The
// catalog entry "qwen/qwen3.6-35b-a3b" pins ContextWindow=262144; when the
// server reports a runtime window of 32768, the probe's observed value must
// win — both for the executor's compaction math and the status-bar max.
// (Before the runtime tier existed, the probe wrote tier 3 and was
// permanently shadowed by the built-in tier 2.)
func TestBuildLocalModelProbe_RuntimeWindowBeatsBuiltinCatalog(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:                  "qwen/qwen3.6-35b-a3b",
		MaxContextLength:    262144, // catalog capability
		LoadedContextLength: 32768,  // runtime window LM Studio actually serves
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := localProbeCfg(t, srv.URL, "qwen/qwen3.6-35b-a3b")
	// No user override: registry starts from the built-in catalog.
	registry := llm.NewModelRegistry(nil)
	b := newLocalProbeBuilder(t)

	probe := b.buildLocalModelProbe(cfg, registry, nil)
	probe("qwen/qwen3.6-35b-a3b")

	meta, ok := registry.ResolveLocal("qwen/qwen3.6-35b-a3b")
	if !ok {
		t.Fatal("expected model to resolve")
	}
	if meta.ContextWindow != 32768 {
		t.Errorf("runtime window shadowed by catalog: got %d, want 32768", meta.ContextWindow)
	}
	if meta.OutputLimit != 8192 {
		t.Errorf("OutputLimit not clamped to window/4: got %d, want 8192", meta.OutputLimit)
	}
}

// TestBuildLocalModelProbe_PartialOverridePlusProbeSurfacesWindow covers the
// exact reported configuration: a user override pinning ONLY the output limit
// (context window left to inherit) for a self-hosted, built-in-catalog model.
// buildRouter must seed the override PARTIAL, so the unset window inherits
// from the probe's observed runtime tier — not from the catalog spec that the
// old merge-with-ResolveBuiltInModel seeding pinned into tier 1.
func TestBuildLocalModelProbe_PartialOverridePlusProbeSurfacesWindow(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:                  "qwen/qwen3.6-35b-a3b",
		MaxContextLength:    262144,
		LoadedContextLength: 32768,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := localProbeCfg(t, srv.URL, "qwen/qwen3.6-35b-a3b")
	// Partial override exactly as buildRouter now seeds it from a config.yaml
	// entry that sets only output_limit (ContextWindow 0 = inherit).
	registry := llm.NewModelRegistry(map[string]llm.ModelMetadata{
		"qwen/qwen3.6-35b-a3b": {OutputLimit: 65536},
	})
	b := newLocalProbeBuilder(t)

	probe := b.buildLocalModelProbe(cfg, registry, nil)
	probe("qwen/qwen3.6-35b-a3b")

	meta, _ := registry.ResolveLocal("qwen/qwen3.6-35b-a3b")
	if meta.ContextWindow != 32768 {
		t.Errorf("window did not inherit from runtime probe: got %d, want 32768", meta.ContextWindow)
	}
	if meta.OutputLimit != 65536 {
		t.Errorf("user-pinned output limit not authoritative: got %d, want 65536", meta.OutputLimit)
	}
}

// TestBuildLocalModelProbe_OnWindowCallbackFires verifies the probe notifies
// its caller with the discovered window, so the orchestrator-side wiring can
// refresh the emitter's display window (status bar) mid-task.
func TestBuildLocalModelProbe_OnWindowCallbackFires(t *testing.T) {
	body := lmStudioModelsPayload(t, lmStudioModel{
		ID:               "qwen2.5-coder-7b",
		MaxContextLength: 262144,
	})
	srv, _, _ := newLMStudioServer(t, http.StatusOK, body)

	cfg := localProbeCfg(t, srv.URL, "qwen2.5-coder-7b")
	registry := llm.NewModelRegistry(nil)
	b := newLocalProbeBuilder(t)

	var gotModel string
	var gotWindow int
	probe := b.buildLocalModelProbe(cfg, registry, func(model string, window int) {
		gotModel, gotWindow = model, window
	})
	probe("qwen2.5-coder-7b")

	if gotModel != "qwen2.5-coder-7b" {
		t.Errorf("callback model = %q, want qwen2.5-coder-7b", gotModel)
	}
	if gotWindow != 262144 {
		t.Errorf("callback window = %d, want 262144", gotWindow)
	}
}

// TestBuildLocalModelProbe_OnWindowCallbackNotFiredOnMiss verifies the
// callback stays silent when the server reports no window (cloud providers),
// so the display window is not clobbered with a zero/garbage value.
func TestBuildLocalModelProbe_OnWindowCallbackNotFiredOnMiss(t *testing.T) {
	// /v1/models-style listing without any window field (a genuine cloud API).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
	}))
	t.Cleanup(srv.Close)

	cfg := localProbeCfg(t, srv.URL, "gpt-4o")
	registry := llm.NewModelRegistry(nil)
	b := newLocalProbeBuilder(t)

	called := false
	probe := b.buildLocalModelProbe(cfg, registry, func(string, int) { called = true })
	probe("gpt-4o")

	if called {
		t.Error("callback fired although no window was discovered")
	}
}

// TestLookupOpenAIProviderBaseURL covers the provider-lookup helper directly:
// any OpenAI provider (local or public host) matches; non-OpenAI and unknown
// models return ok=false. Host locality is deliberately NOT filtered (Variant B).
func TestLookupOpenAIProviderBaseURL(t *testing.T) {
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

	if base, key, ok := lookupOpenAIProviderBaseURL(cfg, "qwen2.5-coder-7b", expand); !ok {
		t.Error("expected match for local openai model")
	} else if base != "http://127.0.0.1:1234/v1" || key != "lm-key" {
		t.Errorf("wrong provider resolved: base=%q key=%q", base, key)
	}

	// Variant B: a public-host OpenAI provider now MATCHES (it is probed; the
	// probe is a harmless no-op if the listing omits the window field).
	if _, _, ok := lookupOpenAIProviderBaseURL(cfg, "gpt-4o", expand); !ok {
		t.Error("public-host openai provider should match under Variant B (locality is not gated)")
	}
	if _, _, ok := lookupOpenAIProviderBaseURL(cfg, "claude-3-5-sonnet", expand); ok {
		t.Error("anthropic provider should not match (non-openai)")
	}
	if _, _, ok := lookupOpenAIProviderBaseURL(cfg, "unknown-model", expand); ok {
		t.Error("unknown model should not match")
	}
}
