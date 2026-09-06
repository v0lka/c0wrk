package backend

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/c0wrk/core/smallllm"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/skills"
	_ "modernc.org/sqlite"
)

// mockBuilder records calls to appBuilder methods for test assertions.
type mockBuilder struct {
	mu                        sync.Mutex
	rebuildJudgeCalls         int
	rebuildRouterCalls        int
	rebuildProxyCalls         int
	updateSearchToolCalls     int
	updateSecPolicyCalls      int
	updateShellBlacklistCalls int
	reconfigureMCPCalls       int
	listProviderModelsCalls   int
	setMCPWorkDirCalls        int
	optimizePromptCalls       int
	getBaseSkillDirsCalls     int
	generateCommitMsgCalls    int
	getBaseAgentDirsCalls     int

	// Configurable return values for methods that have them.
	rebuildRouterErr        error
	rebuildProxyErr         error
	reconfigureMCPErr       error
	updateShellBlacklistErr error
	listProviderModelsRes   []string
	listProviderModelsErr   error
	optimizePromptRes       *core.OptimizePromptResult
	optimizePromptErr       error
	generateCommitMsgRes    string
	generateCommitMsgErr    error
	generateCommitMsgDiff   string

	// rebuildRouterHook, when non-nil, runs inside RebuildRouter while the
	// call is being recorded. Tests use it to block the rebuild phase (e.g.
	// to assert readers are not convoyed behind it) or to observe ordering.
	// It must not call back into mockBuilder methods that take m.mu.
	rebuildRouterHook func(*core.BuilderConfig)

	// rebuildRouterCfgs records the default model of each config passed to
	// RebuildRouter, in call order (guarded by m.mu).
	rebuildRouterCfgs []string

	getSkillDescriptorsCalls int
	getSkillDescriptorsRes   []skills.SkillDescriptor

	getAgentDescriptorsCalls int
	getAgentDescriptorsRes   []agents.AgentDescriptor

	// registry, when non-nil, is returned by ModelRegistry so tests can
	// exercise the metadata-enrichment path of GetConfig/collectAllModels.
	registry *llm.ModelRegistry
}

func (m *mockBuilder) RebuildJudge(_ *core.BuilderConfig) {
	m.mu.Lock()
	m.rebuildJudgeCalls++
	m.mu.Unlock()
}
func (m *mockBuilder) RebuildRouter(cfg *core.BuilderConfig) error {
	m.mu.Lock()
	m.rebuildRouterCalls++
	m.rebuildRouterCfgs = append(m.rebuildRouterCfgs, cfg.LLM.DefaultModel)
	m.mu.Unlock()
	if m.rebuildRouterHook != nil {
		m.rebuildRouterHook(cfg)
	}
	return m.rebuildRouterErr
}

// routerCfgSnapshot returns a copy of the default models passed to
// RebuildRouter so far, in call order. Safe for concurrent use.
func (m *mockBuilder) routerCfgSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.rebuildRouterCfgs))
	copy(out, m.rebuildRouterCfgs)
	return out
}
func (m *mockBuilder) RebuildProxy(_ context.Context, _ *core.BuilderConfig) error {
	m.mu.Lock()
	m.rebuildProxyCalls++
	m.mu.Unlock()
	return m.rebuildProxyErr
}
func (m *mockBuilder) UpdateSearchTool(_ *core.BuilderConfig) {
	m.mu.Lock()
	m.updateSearchToolCalls++
	m.mu.Unlock()
}
func (m *mockBuilder) UpdateSecurityPolicies(_ *core.BuilderConfig) {
	m.mu.Lock()
	m.updateSecPolicyCalls++
	m.mu.Unlock()
}
func (m *mockBuilder) UpdateShellBlacklist(_ *core.BuilderConfig) error {
	m.mu.Lock()
	m.updateShellBlacklistCalls++
	m.mu.Unlock()
	return m.updateShellBlacklistErr
}
func (m *mockBuilder) ReconfigureMCP(_ context.Context, _ *core.BuilderConfig) error {
	m.mu.Lock()
	m.reconfigureMCPCalls++
	m.mu.Unlock()
	return m.reconfigureMCPErr
}
func (m *mockBuilder) ListProviderModels(_ context.Context, _ string, _ *core.BuilderConfig) ([]string, error) {
	m.mu.Lock()
	m.listProviderModelsCalls++
	m.mu.Unlock()
	return m.listProviderModelsRes, m.listProviderModelsErr
}
func (m *mockBuilder) SetMCPWorkDir(_ string) {
	m.mu.Lock()
	m.setMCPWorkDirCalls++
	m.mu.Unlock()
}
func (m *mockBuilder) OptimizePrompt(_ context.Context, _ string) (*core.OptimizePromptResult, error) {
	m.mu.Lock()
	m.optimizePromptCalls++
	m.mu.Unlock()
	return m.optimizePromptRes, m.optimizePromptErr
}
func (m *mockBuilder) GenerateCommitMessage(_ context.Context, diff string) (string, error) {
	m.mu.Lock()
	m.generateCommitMsgCalls++
	m.generateCommitMsgDiff = diff
	m.mu.Unlock()
	return m.generateCommitMsgRes, m.generateCommitMsgErr
}
func (m *mockBuilder) GetBaseSkillDirs() []string {
	m.mu.Lock()
	m.getBaseSkillDirsCalls++
	m.mu.Unlock()
	return nil
}
func (m *mockBuilder) GetSkillDescriptors(string) []skills.SkillDescriptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getSkillDescriptorsCalls++
	return m.getSkillDescriptorsRes
}
func (m *mockBuilder) GetBaseAgentDirs() []string {
	m.mu.Lock()
	m.getBaseAgentDirsCalls++
	m.mu.Unlock()
	return nil
}
func (m *mockBuilder) GetAgentDescriptors(string) []agents.AgentDescriptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getAgentDescriptorsCalls++
	return m.getAgentDescriptorsRes
}
func (m *mockBuilder) ModelRegistry() *llm.ModelRegistry {
	// Tests that exercise the metadata-enrichment path (GetConfig AllModels)
	// set m.registry; the default nil mirrors the pre-init startup window.
	return m.registry
}

func (m *mockBuilder) JudgeAvailable() bool {
	return false
}

// newTestAPI creates a FrontendAPI backed by a mock builder and a temp config.
func newTestAPI(t *testing.T) (*FrontendAPI, *mockBuilder, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "claude-3-opus"
	cfg.LLM.Anthropic.APIKey = "sk-test-original"
	cfg.LLM.Anthropic.Models = []string{"claude-3-opus"}

	mock := &mockBuilder{}
	f := &FrontendAPI{
		config:          cfg,
		configPath:      cfgPath,
		agentDir:        dir,
		builderOverride: mock,
	}
	return f, mock, cfgPath
}

// --- UpdateLLMConfig ---

func TestUpdateLLMConfig_PersistsAndRebuilds(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		DefaultModel: "claude-3-sonnet",
		Anthropic:    &ProviderConfigRequest{Models: []string{"claude-3-sonnet", "claude-3-opus"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert builder calls.
	if mock.rebuildJudgeCalls != 1 {
		t.Errorf("RebuildJudge called %d times, want 1", mock.rebuildJudgeCalls)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}

	// Assert config persisted.
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Fatal("config file not persisted")
	}

	// Assert in-memory config updated.
	if f.config.LLM.DefaultModel != "claude-3-sonnet" {
		t.Errorf("default_model = %q, want claude-3-sonnet", f.config.LLM.DefaultModel)
	}
}

func TestUpdateLLMConfig_MaskedKeyNotOverwritten(t *testing.T) {
	f, _, _ := newTestAPI(t)
	original := f.config.LLM.Anthropic.APIKey

	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		DefaultModel: "claude-3-sonnet",
		Anthropic:    &ProviderConfigRequest{APIKey: maskedAPIKey, Models: []string{"claude-3-sonnet"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.config.LLM.Anthropic.APIKey != original {
		t.Errorf("API key was overwritten by masked value: got %q", f.config.LLM.Anthropic.APIKey)
	}
}

func TestUpdateLLMConfig_AnthropicCompatible(t *testing.T) {
	t.Run("adds provider and persists", func(t *testing.T) {
		f, _, _ := newTestAPI(t)

		err := f.UpdateLLMConfig(LLMFullConfigRequest{
			DefaultModel: "claude-sonnet-4-20250514",
			AnthropicCompatible: map[string]ProviderConfigRequest{
				"my-proxy": {
					BaseURL: "https://my-anthropic-proxy.example.com",
					APIKey:  "proxy-key",
					Models:  []string{"claude-sonnet-4-20250514"},
				},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, ok := f.config.LLM.AnthropicCompatible["my-proxy"]
		if !ok {
			t.Fatal("expected 'my-proxy' in anthropic_compatible after update")
		}
		if got.BaseURL != "https://my-anthropic-proxy.example.com" {
			t.Errorf("base_url = %q, want 'https://my-anthropic-proxy.example.com'", got.BaseURL)
		}
		if got.APIKey != "proxy-key" {
			t.Errorf("api_key = %q, want 'proxy-key'", got.APIKey)
		}
		if len(got.Models) != 1 || got.Models[0] != "claude-sonnet-4-20250514" {
			t.Errorf("models = %v, want [claude-sonnet-4-20250514]", got.Models)
		}
	})

	t.Run("masked key preserves existing value", func(t *testing.T) {
		f, _, _ := newTestAPI(t)
		// Seed an existing anthropic_compatible provider with a real key.
		f.config.LLM.AnthropicCompatible = map[string]config.AnthropicCompatibleConfig{
			"my-proxy": {APIKey: "real-proxy-key", BaseURL: "https://proxy.example.com", Models: []string{"claude-sonnet-4-20250514"}},
		}

		err := f.UpdateLLMConfig(LLMFullConfigRequest{
			AnthropicCompatible: map[string]ProviderConfigRequest{
				"my-proxy": {APIKey: maskedAPIKey, BaseURL: "https://proxy.example.com", Models: []string{"claude-sonnet-4-20250514"}},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.config.LLM.AnthropicCompatible["my-proxy"].APIKey != "real-proxy-key" {
			t.Errorf("api key overwritten by masked sentinel: got %q", f.config.LLM.AnthropicCompatible["my-proxy"].APIKey)
		}
	})
}

func TestUpdateLLMConfig_PerProviderFields(t *testing.T) {
	tests := []struct {
		provider string
		req      LLMFullConfigRequest
	}{
		{"openai_compatible", LLMFullConfigRequest{OpenAICompatible: map[string]ProviderConfigRequest{"openai_compatible": {Models: []string{"test-model"}}}}},
		{"chatgpt", LLMFullConfigRequest{ChatGPT: &ProviderConfigRequest{Models: []string{"test-model"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			f, mock, _ := newTestAPI(t)
			err := f.UpdateLLMConfig(tt.req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mock.rebuildJudgeCalls != 1 {
				t.Errorf("RebuildJudge not called for provider %s", tt.provider)
			}
		})
	}
}

func TestUpdateLLMConfig_NilConfig(t *testing.T) {
	f := &FrontendAPI{}
	err := f.UpdateLLMConfig(LLMFullConfigRequest{DefaultModel: "claude-3-sonnet"})
	if err == nil {
		t.Fatal("expected error when config is nil")
	}
}

func TestUpdateLLMConfig_RollsBackWhenPersistFails(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)
	f.configPath = filepath.Join(filepath.Dir(cfgPath), "missing", "config.yaml")

	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		DefaultModel: "claude-3-sonnet",
		Anthropic:    &ProviderConfigRequest{Models: []string{"claude-3-sonnet"}},
	})
	if err == nil {
		t.Fatal("expected persist failure")
	}
	if got := f.config.LLM.DefaultModel; got != "claude-3-opus" {
		t.Errorf("default_model after failed persist = %q, want claude-3-opus", got)
	}
	if got := f.config.LLM.Anthropic.Models; !slices.Equal(got, []string{"claude-3-opus"}) {
		t.Errorf("anthropic models after failed persist = %v, want [claude-3-opus]", got)
	}
	if mock.rebuildJudgeCalls != 0 {
		t.Errorf("RebuildJudge calls after failed persist = %d, want 0", mock.rebuildJudgeCalls)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter calls after failed persist = %d, want 0", mock.rebuildRouterCalls)
	}
}

// TestUpdateLLMConfig_RejectsDanglingDefaultOnProviderRemoval verifies that
// deleting the provider that owns an already-valid default model without a
// replacement is rejected before it can mutate memory, YAML, or the router.
func TestUpdateLLMConfig_RejectsDanglingDefaultOnProviderRemoval(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	// Seed a persisted, valid composite default whose provider is about to be
	// removed. A byte-for-byte YAML comparison catches an accidental save.
	f.config.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{
		"lmstudio": {BaseURL: "http://localhost:1234/v1", Models: []string{"gpt-4"}},
	}
	f.config.LLM.DefaultModel = "lmstudio/gpt-4"
	if err := config.Save(f.config, cfgPath); err != nil {
		t.Fatalf("failed to save initial config: %v", err)
	}
	beforeYAML, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("failed to read initial config: %v", err)
	}

	err = f.UpdateLLMConfig(LLMFullConfigRequest{
		OpenAICompatible: map[string]ProviderConfigRequest{},
	})
	if err == nil {
		t.Fatal("expected dangling default replacement to be rejected")
	}
	if !strings.Contains(err.Error(), "default_model") {
		t.Errorf("UpdateLLMConfig(provider removal) error = %q, want diagnostic mentioning default_model", err)
	}

	if got := f.config.LLM.DefaultModel; got != "lmstudio/gpt-4" {
		t.Errorf("default_model after rejected provider removal = %q, want lmstudio/gpt-4", got)
	}
	if _, ok := f.config.LLM.OpenAICompatible["lmstudio"]; !ok {
		t.Error("openai_compatible provider was removed despite rejected update")
	}
	if mock.rebuildJudgeCalls != 0 {
		t.Errorf("RebuildJudge calls after rejected provider removal = %d, want 0", mock.rebuildJudgeCalls)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter calls after rejected provider removal = %d, want 0", mock.rebuildRouterCalls)
	}
	afterYAML, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("failed to read config after rejected update: %v", err)
	}
	if !slices.Equal(afterYAML, beforeYAML) {
		t.Error("config YAML changed after rejected provider removal")
	}
}

// TestUpdateLLMConfig_RejectsDanglingDefaultOnModelDisabled verifies that
// removing the model behind an existing default without a replacement is
// rejected atomically.
func TestUpdateLLMConfig_RejectsDanglingDefaultOnModelDisabled(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		Anthropic: &ProviderConfigRequest{Models: []string{"claude-3-sonnet"}},
	})
	if err == nil {
		t.Fatal("expected disabling the default model to be rejected")
	}
	if got := f.config.LLM.DefaultModel; got != "claude-3-opus" {
		t.Errorf("default_model after rejected model replacement = %q, want claude-3-opus", got)
	}
	if got := f.config.LLM.Anthropic.Models; !slices.Equal(got, []string{"claude-3-opus"}) {
		t.Errorf("anthropic models after rejected model replacement = %v, want [claude-3-opus]", got)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter calls after rejected model replacement = %d, want 0", mock.rebuildRouterCalls)
	}
}

// TestUpdateLLMConfig_AcceptsReplacementDefaultWithNewModels verifies that a
// single request can replace an existing default and its backing models.
func TestUpdateLLMConfig_AcceptsReplacementDefaultWithNewModels(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		DefaultModel: "claude-3-sonnet",
		Anthropic:    &ProviderConfigRequest{Models: []string{"claude-3-sonnet"}},
	})
	if err != nil {
		t.Fatalf("UpdateLLMConfig(replacement default) unexpected error: %v", err)
	}
	if got := f.config.LLM.DefaultModel; got != "claude-3-sonnet" {
		t.Errorf("default_model after replacement = %q, want claude-3-sonnet", got)
	}
	if got := f.config.LLM.Anthropic.Models; !slices.Equal(got, []string{"claude-3-sonnet"}) {
		t.Errorf("anthropic models after replacement = %v, want [claude-3-sonnet]", got)
	}
	if mock.rebuildJudgeCalls != 1 {
		t.Errorf("RebuildJudge calls after replacement = %d, want 1", mock.rebuildJudgeCalls)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter calls after replacement = %d, want 1", mock.rebuildRouterCalls)
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load persisted replacement config: %v", err)
	}
	if got := persisted.LLM.DefaultModel; got != "claude-3-sonnet" {
		t.Errorf("persisted default_model after replacement = %q, want claude-3-sonnet", got)
	}
}

// TestUpdateLLMConfig_AllowsInitialEmptyDefault verifies that first-run
// partial setup remains allowed until a user selects a default model.
func TestUpdateLLMConfig_AllowsInitialEmptyDefault(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.LLM.DefaultModel = ""

	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		Anthropic: &ProviderConfigRequest{Models: []string{"claude-3-sonnet"}},
	})
	if err != nil {
		t.Fatalf("UpdateLLMConfig(initial empty default) unexpected error: %v", err)
	}
	if got := f.config.LLM.DefaultModel; got != "" {
		t.Errorf("default_model after initial partial setup = %q, want empty", got)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter calls for initial partial setup = %d, want 1", mock.rebuildRouterCalls)
	}
}

// TestUpdateLLMConfig_PreservesDefaultWhenStillResolvable ensures the
// re-validation step does NOT clear a default that still resolves (e.g. when
// only API keys change), guarding against a regression that wipes valid state.
func TestUpdateLLMConfig_PreservesDefaultWhenStillResolvable(t *testing.T) {
	f, _, _ := newTestAPI(t)
	want := f.config.LLM.DefaultModel

	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		Anthropic: &ProviderConfigRequest{APIKey: "sk-new", Models: []string{"claude-3-opus"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if f.config.LLM.DefaultModel != want {
		t.Errorf("default_model = %q, want %q (should be preserved)", f.config.LLM.DefaultModel, want)
	}
}

// --- GetModelConfig ---

func TestGetModelConfig_KnownModelNoOverride(t *testing.T) {
	f, _, _ := newTestAPI(t)

	// gpt-4o is a stable built-in entry: ContextWindow=128000, OutputLimit=16384.
	resp, err := f.GetModelConfig("gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.HasOverride {
		t.Error("expected HasOverride=false for a model with no config entry")
	}
	if resp.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want 128000", resp.ContextWindow)
	}
	if resp.OutputLimit != 16384 {
		t.Errorf("OutputLimit = %d, want 16384", resp.OutputLimit)
	}
	if resp.DefaultContextWindow != 128000 {
		t.Errorf("DefaultContextWindow = %d, want 128000", resp.DefaultContextWindow)
	}
	if resp.DefaultOutputLimit != 16384 {
		t.Errorf("DefaultOutputLimit = %d, want 16384", resp.DefaultOutputLimit)
	}
}

func TestGetModelConfig_UnknownModel(t *testing.T) {
	f, _, _ := newTestAPI(t)

	resp, err := f.GetModelConfig("acme-unknown-model-xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unknown model → fallback defaults (128000/32768), both effective and default.
	if resp.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want fallback 128000", resp.ContextWindow)
	}
	if resp.OutputLimit != 32768 {
		t.Errorf("OutputLimit = %d, want fallback 32768", resp.OutputLimit)
	}
}

func TestGetModelConfig_WithOverride(t *testing.T) {
	f, _, _ := newTestAPI(t)
	// Seed a partial override: only context window set, output limit 0 = inherit.
	f.config.LLM.Models = map[string]config.ModelOverride{
		"gpt-4o": {ContextWindow: 200000, OutputLimit: 0},
	}

	resp, err := f.GetModelConfig("gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.HasOverride {
		t.Error("expected HasOverride=true when a config entry exists")
	}
	// Effective context window is the override value.
	if resp.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want override 200000", resp.ContextWindow)
	}
	// Output limit 0 inherits the built-in default (16384).
	if resp.OutputLimit != 16384 {
		t.Errorf("OutputLimit = %d, want inherited default 16384", resp.OutputLimit)
	}
	// Defaults are still the built-in values.
	if resp.DefaultContextWindow != 128000 || resp.DefaultOutputLimit != 16384 {
		t.Errorf("defaults = %d/%d, want 128000/16384", resp.DefaultContextWindow, resp.DefaultOutputLimit)
	}
}

func TestGetModelConfig_NilConfig(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.GetModelConfig("gpt-4o"); err == nil {
		t.Fatal("expected error when config is nil")
	}
}

// --- SetModelConfig ---

func TestSetModelConfig_BothFieldsNonDefault(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	err := f.SetModelConfig("gpt-4o", ModelConfigRequest{
		ContextWindow: 200000,
		OutputLimit:   99999,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// In-memory override stores both values.
	override, ok := f.config.LLM.Models["gpt-4o"]
	if !ok {
		t.Fatal("expected gpt-4o override entry after set")
	}
	if override.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", override.ContextWindow)
	}
	if override.OutputLimit != 99999 {
		t.Errorf("OutputLimit = %d, want 99999", override.OutputLimit)
	}

	// Router was rebuilt so the override takes effect.
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}

	// Persisted to disk: re-read and verify field-level values.
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	got, ok := reloaded.LLM.Models["gpt-4o"]
	if !ok {
		t.Fatal("expected gpt-4o override in persisted config")
	}
	if got.ContextWindow != 200000 || got.OutputLimit != 99999 {
		t.Errorf("persisted override = %+v, want {200000 99999}", got)
	}
}

func TestSetModelConfig_PartialOverrideOmitsDefaultField(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)

	// Set only output limit to a non-default value; context window equals the
	// built-in default (128000) so it must be stored as 0 / omitted.
	err := f.SetModelConfig("gpt-4o", ModelConfigRequest{
		ContextWindow: 128000, // == built-in default → stored as 0
		OutputLimit:   50000,  // != default → stored
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	override := f.config.LLM.Models["gpt-4o"]
	if override.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (default omitted)", override.ContextWindow)
	}
	if override.OutputLimit != 50000 {
		t.Errorf("OutputLimit = %d, want 50000", override.OutputLimit)
	}

	// Persisted file reflects the omission: context_window absent, only
	// output_limit present.
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	got := reloaded.LLM.Models["gpt-4o"]
	if got.ContextWindow != 0 {
		t.Errorf("persisted ContextWindow = %d, want 0 (omitempty)", got.ContextWindow)
	}
	if got.OutputLimit != 50000 {
		t.Errorf("persisted OutputLimit = %d, want 50000", got.OutputLimit)
	}
}

func TestSetModelConfig_AllDefaultsRemovesEntry(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)
	// Seed an existing override that will be cleared.
	f.config.LLM.Models = map[string]config.ModelOverride{
		"gpt-4o": {ContextWindow: 200000, OutputLimit: 99999},
	}

	// Setting both fields to the built-in default must remove the entry.
	err := f.SetModelConfig("gpt-4o", ModelConfigRequest{
		ContextWindow: 128000,
		OutputLimit:   16384,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := f.config.LLM.Models["gpt-4o"]; ok {
		t.Error("expected gpt-4o override entry removed when all fields match defaults")
	}

	// Persisted file no longer contains the model key.
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if _, ok := reloaded.LLM.Models["gpt-4o"]; ok {
		t.Error("expected gpt-4o override absent from persisted config")
	}
}

func TestSetModelConfig_NilConfig(t *testing.T) {
	f := &FrontendAPI{}
	err := f.SetModelConfig("gpt-4o", ModelConfigRequest{ContextWindow: 200000})
	if err == nil {
		t.Fatal("expected error when config is nil")
	}
}

func TestSetModelConfig_RejectsNonPositiveValues(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	// Both fields are independently validated: a zero/negative on either is
	// rejected before any mutation or persistence happens.
	for _, tc := range []struct {
		name string
		req  ModelConfigRequest
	}{
		{"zero context window", ModelConfigRequest{ContextWindow: 0, OutputLimit: 4096}},
		{"negative context window", ModelConfigRequest{ContextWindow: -1, OutputLimit: 4096}},
		{"zero output limit", ModelConfigRequest{ContextWindow: 128000, OutputLimit: 0}},
		{"negative output limit", ModelConfigRequest{ContextWindow: 128000, OutputLimit: -5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := f.SetModelConfig("gpt-4o", tc.req)
			if err == nil {
				t.Fatal("expected error for non-positive model config value")
			}
		})
	}

	// Nothing was mutated, persisted, or rebuilt.
	if _, ok := f.config.LLM.Models["gpt-4o"]; ok {
		t.Error("expected no override entry written on validation failure")
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

// --- Metadata fields (TokenizerType / Family / Protocol / Capabilities) ---

func TestGetModelConfig_KnownModelReturnsBuiltInMetadata(t *testing.T) {
	f, _, _ := newTestAPI(t)

	resp, err := f.GetModelConfig("gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// gpt-4o built-in: tok=tiktoken/o200k_base, family=openai_flagship,
	// protocol=chat_completions (detected), caps={Attachment, Temperature, ToolCall}.
	if resp.TokenizerType != "tiktoken/o200k_base" {
		t.Errorf("TokenizerType = %q, want tiktoken/o200k_base", resp.TokenizerType)
	}
	if resp.Family != "openai_flagship" {
		t.Errorf("Family = %q, want openai_flagship", resp.Family)
	}
	if resp.Protocol != "chat_completions" {
		t.Errorf("Protocol = %q, want chat_completions", resp.Protocol)
	}
	if !resp.Capabilities.Attachment || resp.Capabilities.Reasoning ||
		!resp.Capabilities.Temperature || !resp.Capabilities.ToolCall {
		t.Errorf("Capabilities = %+v, want {Attachment Temperature ToolCall}", resp.Capabilities)
	}
	// Defaults mirror effective (no override).
	if resp.DefaultTokenizerType != resp.TokenizerType {
		t.Errorf("DefaultTokenizerType = %q, want %q", resp.DefaultTokenizerType, resp.TokenizerType)
	}
	if resp.DefaultFamily != resp.Family {
		t.Errorf("DefaultFamily = %q, want %q", resp.DefaultFamily, resp.Family)
	}
}

func TestGetModelConfig_UnknownModelSurfacesDetectedMetadata(t *testing.T) {
	f, _, _ := newTestAPI(t)

	resp, err := f.GetModelConfig("acme-unknown-model-xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unknown model → fallback defaults + detected family ("default") and
	// protocol ("chat_completions").
	if resp.TokenizerType != "approximate" {
		t.Errorf("TokenizerType = %q, want approximate", resp.TokenizerType)
	}
	if resp.Family != "default" {
		t.Errorf("Family = %q, want default (detected)", resp.Family)
	}
	if resp.Protocol != "chat_completions" {
		t.Errorf("Protocol = %q, want chat_completions (detected)", resp.Protocol)
	}
}

func TestGetModelConfig_WithMetadataOverride(t *testing.T) {
	f, _, _ := newTestAPI(t)
	// Override tokenizer, family, protocol, and capabilities.
	f.config.LLM.Models = map[string]config.ModelOverride{
		"gpt-4o": {
			TokenizerType: "approximate",
			Family:        "anthropic",
			Protocol:      "anthropic",
			Capabilities:  &llm.ModelCapabilities{Attachment: false, Reasoning: true, Temperature: false, ToolCall: false},
		},
	}

	resp, err := f.GetModelConfig("gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.TokenizerType != "approximate" {
		t.Errorf("TokenizerType = %q, want override approximate", resp.TokenizerType)
	}
	if resp.Family != "anthropic" {
		t.Errorf("Family = %q, want override anthropic", resp.Family)
	}
	if resp.Protocol != "anthropic" {
		t.Errorf("Protocol = %q, want override anthropic", resp.Protocol)
	}
	if resp.Capabilities.Reasoning != true || resp.Capabilities.Attachment != false {
		t.Errorf("Capabilities = %+v, want {Reasoning}", resp.Capabilities)
	}
	// Defaults still the built-in values.
	if resp.DefaultTokenizerType != "tiktoken/o200k_base" {
		t.Errorf("DefaultTokenizerType = %q, want tiktoken/o200k_base", resp.DefaultTokenizerType)
	}
}

func TestSetModelConfig_MetaDataFieldsNonDefault(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	err := f.SetModelConfig("gpt-4o", ModelConfigRequest{
		ContextWindow: 128000, // == default → omitted
		OutputLimit:   16384,  // == default → omitted
		TokenizerType: "approximate",
		Family:        "mistral",
		Protocol:      "responses",
		Capabilities:  &llm.ModelCapabilities{Attachment: false, Reasoning: false, Temperature: false, ToolCall: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	override := f.config.LLM.Models["gpt-4o"]
	if override.ContextWindow != 0 || override.OutputLimit != 0 {
		t.Errorf("CW/OL = %d/%d, want 0/0 (defaults omitted)", override.ContextWindow, override.OutputLimit)
	}
	if override.TokenizerType != "approximate" {
		t.Errorf("TokenizerType = %q, want approximate", override.TokenizerType)
	}
	if override.Family != "mistral" {
		t.Errorf("Family = %q, want mistral", override.Family)
	}
	if override.Protocol != "responses" {
		t.Errorf("Protocol = %q, want responses", override.Protocol)
	}
	if override.Capabilities == nil {
		t.Fatal("expected non-nil Capabilities override")
	}
	if override.Capabilities.ToolCall != true || override.Capabilities.Attachment != false {
		t.Errorf("Capabilities = %+v, want {ToolCall}", *override.Capabilities)
	}

	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}

	// Persisted round-trip.
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	got := reloaded.LLM.Models["gpt-4o"]
	if got.TokenizerType != "approximate" {
		t.Errorf("persisted TokenizerType = %q, want approximate", got.TokenizerType)
	}
	if got.Family != "mistral" {
		t.Errorf("persisted Family = %q, want mistral", got.Family)
	}
	if got.Protocol != "responses" {
		t.Errorf("persisted Protocol = %q, want responses", got.Protocol)
	}
	if got.Capabilities == nil {
		t.Fatal("expected persisted non-nil Capabilities")
	}
}

func TestSetModelConfig_MetaDataFieldsDefaultOmitted(t *testing.T) {
	f, _, _ := newTestAPI(t)

	// Send the built-in default values for the metadata fields → they must
	// NOT be stored (sentinel "" / nil = inherit default).
	err := f.SetModelConfig("gpt-4o", ModelConfigRequest{
		ContextWindow: 200000,                                                                                        // != default → stored
		OutputLimit:   16384,                                                                                         // == default → omitted
		TokenizerType: "tiktoken/o200k_base",                                                                         // == default → omitted
		Family:        "openai_flagship",                                                                             // == default → omitted
		Protocol:      "chat_completions",                                                                            // == default → omitted
		Capabilities:  &llm.ModelCapabilities{Attachment: true, Reasoning: false, Temperature: true, ToolCall: true}, // == default → omitted
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	override := f.config.LLM.Models["gpt-4o"]
	if override.TokenizerType != "" {
		t.Errorf("TokenizerType = %q, want \"\" (default omitted)", override.TokenizerType)
	}
	if override.Family != "" {
		t.Errorf("Family = %q, want \"\" (default omitted)", override.Family)
	}
	if override.Protocol != "" {
		t.Errorf("Protocol = %q, want \"\" (default omitted)", override.Protocol)
	}
	if override.Capabilities != nil {
		t.Errorf("Capabilities = %+v, want nil (default omitted)", override.Capabilities)
	}
	// Only context window differs.
	if override.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", override.ContextWindow)
	}
}

func TestSetModelConfig_RejectsInvalidEnumValues(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	for _, tc := range []struct {
		name string
		req  ModelConfigRequest
	}{
		{"invalid tokenizer", ModelConfigRequest{
			ContextWindow: 128000, OutputLimit: 16384, TokenizerType: "bogus-tok",
		}},
		{"invalid family", ModelConfigRequest{
			ContextWindow: 128000, OutputLimit: 16384, Family: "bogus-family",
		}},
		{"invalid protocol", ModelConfigRequest{
			ContextWindow: 128000, OutputLimit: 16384, Protocol: "bogus-proto",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := f.SetModelConfig("gpt-4o", tc.req)
			if err == nil {
				t.Fatal("expected error for invalid enum value")
			}
		})
	}

	// Nothing was mutated or rebuilt.
	if _, ok := f.config.LLM.Models["gpt-4o"]; ok {
		t.Error("expected no override entry on validation failure")
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

func TestSetModelConfig_AllMetadataDefaultsRemovesEntry(t *testing.T) {
	f, _, _ := newTestAPI(t)
	// Seed an entry with metadata overrides.
	f.config.LLM.Models = map[string]config.ModelOverride{
		"gpt-4o": {TokenizerType: "approximate", Family: "mistral"},
	}

	// Setting every field to the built-in default removes the entry entirely.
	err := f.SetModelConfig("gpt-4o", ModelConfigRequest{
		ContextWindow: 128000,
		OutputLimit:   16384,
		TokenizerType: "tiktoken/o200k_base",
		Family:        "openai_flagship",
		Protocol:      "chat_completions",
		Capabilities:  &llm.ModelCapabilities{Attachment: true, Reasoning: false, Temperature: true, ToolCall: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := f.config.LLM.Models["gpt-4o"]; ok {
		t.Error("expected gpt-4o override removed when all fields match defaults")
	}
}

// --- UpdateSearchSettings ---

func TestUpdateSearchSettings_PersistsAndRebuilds(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	err := f.UpdateSearchSettings(SearchSettingsRequest{Provider: "exa", APIKey: "key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.updateSearchToolCalls != 1 {
		t.Errorf("UpdateSearchTool called %d times, want 1", mock.updateSearchToolCalls)
	}
	if f.config.Search.Provider != "exa" {
		t.Errorf("provider = %q, want exa", f.config.Search.Provider)
	}
}

// --- UpdateProxySettings ---

func TestUpdateProxySettings_PersistsAndRebuilds(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	err := f.UpdateProxySettings(ProxySettingsRequest{
		Enabled: true,
		URL:     "http://proxy:3128",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.rebuildProxyCalls != 1 {
		t.Errorf("RebuildProxy called %d times, want 1", mock.rebuildProxyCalls)
	}
	if !f.config.Proxy.Enabled {
		t.Error("Proxy.Enabled not set")
	}
}

func TestUpdateProxySettings_NilConfig(t *testing.T) {
	f := &FrontendAPI{}
	err := f.UpdateProxySettings(ProxySettingsRequest{Enabled: true})
	if err == nil {
		t.Fatal("expected error when config is nil")
	}
}

// TestUpdateProxySettings_MaskedURLPreserved verifies that round-tripping the
// masked proxy URL (proxy.MaskURL replaces the password with "***") does NOT
// overwrite the real, password-bearing URL. The frontend stores the displayed
// (masked) URL and sends it back when only another field is edited; without
// the preserve guard the real password would be silently replaced with "***".
func TestUpdateProxySettings_MaskedURLPreserved(t *testing.T) {
	f, _, _ := newTestAPI(t)
	const realURL = "http://user:secret@proxy.example.com:8080"
	f.config.Proxy.URL = realURL

	if err := f.UpdateProxySettings(ProxySettingsRequest{
		Enabled: true,
		URL:     proxy.MaskURL(realURL),
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.config.Proxy.URL != realURL {
		t.Errorf("proxy URL overwritten with masked value: got %q want %q", f.config.Proxy.URL, realURL)
	}
}

// TestUpdateProxySettings_NewURLApplied verifies a genuinely different URL is
// still applied (the preserve guard only skips the masked form).
func TestUpdateProxySettings_NewURLApplied(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.Proxy.URL = "http://user:secret@old.example.com:8080"
	const newURL = "http://user:newpass@proxy.example.com:3128"

	if err := f.UpdateProxySettings(ProxySettingsRequest{Enabled: true, URL: newURL}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.config.Proxy.URL != newURL {
		t.Errorf("proxy URL not updated: got %q want %q", f.config.Proxy.URL, newURL)
	}
}

// --- UpdateSecuritySettings ---

// fullGroupPayload returns a COMPLETE seven-group payload (the contract
// UpdateSecuritySettings enforces: the map replaces the stored one, so every
// configurable group must be present) with the given overrides applied on
// top of safe defaults.
func fullGroupPayload(overrides map[string]GroupPolicyResponse) map[string]GroupPolicyResponse {
	groups := map[string]GroupPolicyResponse{
		config.ToolGroupExecute:     {Policy: config.GroupPolicyUserConfirm},
		config.ToolGroupLocalRead:   {Policy: config.GroupPolicyAllow},
		config.ToolGroupRemoteRead:  {Policy: config.GroupPolicyAllow},
		config.ToolGroupLocalWrite:  {Policy: config.GroupPolicyUserConfirm},
		config.ToolGroupLocalMCP:    {Policy: config.GroupPolicyUserConfirm},
		config.ToolGroupRemoteMCP:   {Policy: config.GroupPolicyUserConfirm},
		config.ToolGroupRemoteWrite: {Policy: config.GroupPolicyUserConfirm},
	}
	for name, g := range overrides {
		groups[name] = g
	}
	return groups
}

func TestUpdateSecuritySettings_AppliesGroups(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups: fullGroupPayload(map[string]GroupPolicyResponse{
			config.ToolGroupExecute:    {Policy: config.GroupPolicyUserConfirm, Blacklist: []string{`rm\s+-rf`}},
			config.ToolGroupLocalWrite: {Policy: config.GroupPolicyDeny},
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.updateSecPolicyCalls != 1 {
		t.Errorf("UpdateSecurityPolicies called %d times, want 1", mock.updateSecPolicyCalls)
	}
	got := f.config.Security.Groups[config.ToolGroupExecute]
	if got.Policy != config.GroupPolicyUserConfirm {
		t.Errorf("execute policy = %q, want user_confirm", got.Policy)
	}
	if len(got.Blacklist) != 1 || got.Blacklist[0] != `rm\s+-rf` {
		t.Errorf("execute blacklist = %v, want [rm\\s+-rf]", got.Blacklist)
	}
	if got := f.config.Security.Groups[config.ToolGroupLocalWrite].Policy; got != config.GroupPolicyDeny {
		t.Errorf("local_write policy = %q, want deny", got)
	}
	// The payload changed the execute blacklist, so the shell tool must be
	// re-registered for the edit to apply without a restart.
	if mock.updateShellBlacklistCalls != 1 {
		t.Errorf("UpdateShellBlacklist called %d times, want 1", mock.updateShellBlacklistCalls)
	}
}

// TestUpdateSecuritySettings_NoShellReregistrationWhenBlacklistUnchanged
// verifies that a policy-only update leaves the shell tool alone: the
// blacklist is compiled into the tool instance, so re-registration is
// reserved for actual blacklist edits.
func TestUpdateSecuritySettings_NoShellReregistrationWhenBlacklistUnchanged(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	before := f.config.Security.Groups[config.ToolGroupExecute].Blacklist

	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups: fullGroupPayload(map[string]GroupPolicyResponse{
			config.ToolGroupExecute:    {Policy: config.GroupPolicyAllow, Blacklist: before},
			config.ToolGroupLocalWrite: {Policy: config.GroupPolicyDeny},
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.updateSecPolicyCalls != 1 {
		t.Errorf("UpdateSecurityPolicies called %d times, want 1", mock.updateSecPolicyCalls)
	}
	if mock.updateShellBlacklistCalls != 0 {
		t.Errorf("UpdateShellBlacklist called %d times, want 0 for an unchanged blacklist", mock.updateShellBlacklistCalls)
	}
}

// TestUpdateSecuritySettings_RejectsSystemGroup verifies the reserved system
// group cannot be configured from the UI (mirroring config-file validation)
// and that an invalid payload mutates nothing.
func TestUpdateSecuritySettings_RejectsSystemGroup(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	before := f.config.Security.Groups[config.ToolGroupExecute].Policy

	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups: map[string]GroupPolicyResponse{
			config.ToolGroupSystem: {Policy: config.GroupPolicyAllow},
		},
	})
	if err == nil {
		t.Fatal("expected error for the reserved system group")
	}
	if mock.updateSecPolicyCalls != 0 {
		t.Error("UpdateSecurityPolicies must not be called on an invalid payload")
	}
	if got := f.config.Security.Groups[config.ToolGroupExecute].Policy; got != before {
		t.Errorf("config mutated by a rejected payload: %q -> %q", before, got)
	}
}

// TestUpdateSecuritySettings_RejectsPartialGroups verifies the completeness
// contract: the groups map REPLACES the stored one, so a payload omitting a
// configurable group is rejected instead of silently weakening it (an
// omitted deny downgrades live to user_confirm; omitting execute would strip
// the live shell blacklist). The error must teach the missing group names.
func TestUpdateSecuritySettings_RejectsPartialGroups(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	// local_read is configured to deny — a partial update missing it must
	// not silently downgrade the live policy.
	f.config.Security.Groups[config.ToolGroupLocalRead] = config.GroupPolicyConfig{Policy: config.GroupPolicyDeny}
	before := f.config.Security.Groups

	groups := fullGroupPayload(nil)
	delete(groups, config.ToolGroupLocalRead) // omit one of the seven
	delete(groups, config.ToolGroupRemoteMCP) // omit another

	err := f.UpdateSecuritySettings(SecuritySettingsResponse{Groups: groups})
	if err == nil {
		t.Fatal("expected error for a partial groups payload")
	}
	if !strings.Contains(err.Error(), "missing: local_read, remote_mcp") {
		t.Errorf("error %q should name the missing groups", err)
	}
	if mock.updateSecPolicyCalls != 0 || mock.updateShellBlacklistCalls != 0 {
		t.Error("no builder method may run for a rejected payload")
	}
	if got := f.config.Security.Groups[config.ToolGroupLocalRead].Policy; got != config.GroupPolicyDeny {
		t.Errorf("local_read policy downgraded by rejected payload: got %q, want deny", got)
	}
	if len(f.config.Security.Groups) != len(before) {
		t.Errorf("group set mutated by rejected payload: got %d groups, want %d", len(f.config.Security.Groups), len(before))
	}
}

// TestUpdateSecuritySettings_DefaultBlacklistStoredUnset verifies the
// store-as-unset rule: saving a blacklist identical to the shipped defaults
// (what the UI echoes back on a defaults-in-force config) stores UNSET (nil)
// instead of pinning today's default patterns into the config file — future
// default-list improvements keep flowing. The effective blacklist is
// unchanged: GetSecuritySettings still reports the defaults and no shell
// re-registration fires (effective lists compare equal).
func TestUpdateSecuritySettings_DefaultBlacklistStoredUnset(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	// newTestAPI ran ApplyDefaults, so the live config holds the default
	// blacklist — exactly the state the UI round-trips.
	defaults := config.DefaultExecuteGroupBlacklist()

	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups: fullGroupPayload(map[string]GroupPolicyResponse{
			config.ToolGroupExecute: {Policy: config.GroupPolicyUserConfirm, Blacklist: defaults},
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := f.config.Security.Groups[config.ToolGroupExecute].Blacklist; got != nil {
		t.Errorf("default-equal blacklist stored as a literal (%d patterns) — must stay unset (nil)", len(got))
	}
	if mock.updateShellBlacklistCalls != 0 {
		t.Errorf("UpdateShellBlacklist called %d times, want 0 (effective list unchanged)", mock.updateShellBlacklistCalls)
	}
	// The UI must still see the live truth: the effective default list.
	if got := f.GetSecuritySettings().Groups[config.ToolGroupExecute].Blacklist; !slices.Equal(got, defaults) {
		t.Errorf("GetSecuritySettings execute blacklist = %v, want the shipped defaults", got)
	}
}

// TestUpdateSecuritySettings_ExplicitEmptyBlacklistStaysEmpty verifies that
// clearing the blacklist in the UI is an intentional choice: an explicit
// empty list is NOT resurrected into the shipped defaults. It re-registers
// the shell tool (the effective list changed) and GetSecuritySettings
// reports an empty list.
func TestUpdateSecuritySettings_ExplicitEmptyBlacklistStaysEmpty(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups: fullGroupPayload(map[string]GroupPolicyResponse{
			config.ToolGroupExecute: {Policy: config.GroupPolicyUserConfirm, Blacklist: []string{}},
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stored := f.config.Security.Groups[config.ToolGroupExecute].Blacklist
	if stored == nil || len(stored) != 0 {
		t.Errorf("explicit empty blacklist must persist as a non-nil empty list, got %v (nil=%t)", stored, stored == nil)
	}
	if mock.updateShellBlacklistCalls != 1 {
		t.Errorf("UpdateShellBlacklist called %d times, want 1 (defaults → empty is a real change)", mock.updateShellBlacklistCalls)
	}
	if got := f.GetSecuritySettings().Groups[config.ToolGroupExecute].Blacklist; len(got) != 0 {
		t.Errorf("GetSecuritySettings execute blacklist = %v, want empty", got)
	}
}

// TestSecuritySettings_ExplicitEmptyBlacklistRoundTrip verifies that an
// explicitly emptied execute blacklist survives a get -> update -> get
// cycle: the response encodes [] (not an omitted field), the echoed update
// stores a non-nil empty list, and the effective blacklist stays empty
// instead of silently reverting to the shipped defaults. This is exactly
// the settings-UI pattern — every save echoes GetSecuritySettings output
// back into UpdateSecuritySettings.
func TestSecuritySettings_ExplicitEmptyBlacklistRoundTrip(t *testing.T) {
	f, _, _ := newTestAPI(t)
	if err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups: fullGroupPayload(map[string]GroupPolicyResponse{
			config.ToolGroupExecute: {Policy: config.GroupPolicyUserConfirm, Blacklist: []string{}},
		}),
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	echo := f.GetSecuritySettings()
	got := echo.Groups[config.ToolGroupExecute].Blacklist
	if got == nil || len(got) != 0 {
		t.Fatalf("get must report an explicit empty execute blacklist (non-nil []), got %v (nil=%t)", got, got == nil)
	}
	if err := f.UpdateSecuritySettings(echo); err != nil {
		t.Fatalf("unexpected error re-saving the echoed settings: %v", err)
	}
	stored := f.config.Security.Groups[config.ToolGroupExecute].Blacklist
	if stored == nil || len(stored) != 0 {
		t.Errorf("explicit empty blacklist reverted to %v (nil=%t) after the echo round trip — the user's choice must survive", stored, stored == nil)
	}
	if got := f.GetSecuritySettings().Groups[config.ToolGroupExecute].Blacklist; len(got) != 0 {
		t.Errorf("effective execute blacklist after round trip = %v, want empty", got)
	}
}

// TestUpdateSecuritySettings_ShellBlacklistFailureRollsBack verifies the
// failure atomicity of a blacklist edit: UpdateShellBlacklist runs BEFORE
// the policy application, and when it fails the whole replacement is rolled
// back — the previous groups stay in force and UpdateSecurityPolicies is
// never called, so the live registry and the stored config never diverge.
func TestUpdateSecuritySettings_ShellBlacklistFailureRollsBack(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	mock.updateShellBlacklistErr = errors.New("blacklist compile failure")
	prevGroups := f.config.Security.Groups
	prevBlacklist := prevGroups[config.ToolGroupExecute].Blacklist
	f.config.Security.SmartApprove = false

	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups: fullGroupPayload(map[string]GroupPolicyResponse{
			config.ToolGroupExecute: {Policy: config.GroupPolicyDeny, Blacklist: []string{`mkfs`}},
		}),
		SmartApprove: true,
	})
	if err == nil {
		t.Fatal("expected error when the shell re-registration fails")
	}
	if mock.updateSecPolicyCalls != 0 {
		t.Error("UpdateSecurityPolicies must not run when the shell re-registration fails")
	}
	if got := f.config.Security.Groups[config.ToolGroupExecute].Policy; got != prevGroups[config.ToolGroupExecute].Policy {
		t.Errorf("execute policy not rolled back: got %q, want %q", got, prevGroups[config.ToolGroupExecute].Policy)
	}
	if got := f.config.Security.Groups[config.ToolGroupExecute].Blacklist; !slices.Equal(got, prevBlacklist) {
		t.Errorf("execute blacklist not rolled back: got %v, want %v", got, prevBlacklist)
	}
	if f.config.Security.SmartApprove {
		t.Error("SmartApprove not rolled back")
	}
	if len(f.config.Security.Groups) != len(prevGroups) {
		t.Errorf("group set not fully restored: got %d groups, want %d", len(f.config.Security.Groups), len(prevGroups))
	}
}

func TestUpdateSecuritySettings_RejectsInvalidPayloads(t *testing.T) {
	f, _, _ := newTestAPI(t)
	cases := []struct {
		name    string
		groups  map[string]GroupPolicyResponse
		wantErr bool
	}{
		{
			name: "bad policy enum",
			groups: map[string]GroupPolicyResponse{
				config.ToolGroupLocalRead: {Policy: "always_allow"},
			},
			wantErr: true,
		},
		{
			name: "unknown group name",
			groups: map[string]GroupPolicyResponse{
				"totally_fake": {Policy: config.GroupPolicyAllow},
			},
			wantErr: true,
		},
		{
			name: "blacklist pattern does not compile",
			groups: map[string]GroupPolicyResponse{
				config.ToolGroupExecute: {Policy: config.GroupPolicyAllow, Blacklist: []string{"("}},
			},
			wantErr: true,
		},
		{
			name: "blacklist outside execute",
			groups: map[string]GroupPolicyResponse{
				config.ToolGroupLocalWrite: {Policy: config.GroupPolicyAllow, Blacklist: []string{"x"}},
			},
			wantErr: true,
		},
		{
			name: "partial payload (six of seven groups)",
			groups: func() map[string]GroupPolicyResponse {
				g := fullGroupPayload(nil)
				delete(g, config.ToolGroupLocalRead)
				return g
			}(),
			wantErr: true,
		},
		{
			name: "valid full group set",
			groups: fullGroupPayload(map[string]GroupPolicyResponse{
				config.ToolGroupExecute: {Policy: config.GroupPolicyAllow, Blacklist: []string{`sudo\s+`}},
			}),
			wantErr: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := f.UpdateSecuritySettings(SecuritySettingsResponse{Groups: tt.groups})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestGetUpdateSecuritySettings_GroupsRoundTrip verifies a get -> set -> get
// cycle returns the same group set (policies and the execute blacklist) and
// that the SmartApprove flag propagates to the shared tool registry via
// UpdateSecurityPolicies.
func TestGetUpdateSecuritySettings_GroupsRoundTrip(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.Security.SmartApprove = false

	// Default-off flag is exposed as stored.
	if got := f.GetSecuritySettings().SmartApprove; got {
		t.Fatalf("SmartApprove = true, want false by default")
	}

	in := SecuritySettingsResponse{
		Groups: map[string]GroupPolicyResponse{
			config.ToolGroupExecute:     {Policy: config.GroupPolicyDeny, Blacklist: []string{`mkfs`}},
			config.ToolGroupLocalRead:   {Policy: config.GroupPolicyAllow},
			config.ToolGroupRemoteRead:  {Policy: config.GroupPolicyAllow},
			config.ToolGroupLocalWrite:  {Policy: config.GroupPolicyUserConfirm},
			config.ToolGroupLocalMCP:    {Policy: config.GroupPolicyUserConfirm},
			config.ToolGroupRemoteMCP:   {Policy: config.GroupPolicyUserConfirm},
			config.ToolGroupRemoteWrite: {Policy: config.GroupPolicyUserConfirm},
		},
		SmartApprove: true,
	}
	if err := f.UpdateSecuritySettings(in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.config.Security.SmartApprove {
		t.Error("SmartApprove not persisted to config")
	}
	if mock.updateSecPolicyCalls != 1 {
		t.Errorf("UpdateSecurityPolicies called %d times, want 1", mock.updateSecPolicyCalls)
	}

	got := f.GetSecuritySettings()
	if !got.SmartApprove {
		t.Error("SmartApprove not reflected in GetSecuritySettings")
	}
	for name, want := range in.Groups {
		g, ok := got.Groups[name]
		if !ok {
			t.Errorf("group %q missing from GetSecuritySettings", name)
			continue
		}
		if g.Policy != want.Policy {
			t.Errorf("group %q policy = %q, want %q", name, g.Policy, want.Policy)
		}
		if len(g.Blacklist) != len(want.Blacklist) {
			t.Errorf("group %q blacklist = %v, want %v", name, g.Blacklist, want.Blacklist)
		}
	}
}

// TestGetSecuritySettings_NoConfigDefaults verifies the nil-config branch
// still hands the UI a complete, editable default group set.
func TestGetSecuritySettings_NoConfigDefaults(t *testing.T) {
	f := &FrontendAPI{} // f.config == nil
	got := f.GetSecuritySettings()
	if len(got.Groups) != 7 {
		t.Fatalf("expected the 7 configurable groups from defaults, got %d: %v", len(got.Groups), got.Groups)
	}
	if got.Groups[config.ToolGroupExecute].Policy != config.GroupPolicyUserConfirm {
		t.Errorf("default execute policy = %q, want user_confirm", got.Groups[config.ToolGroupExecute].Policy)
	}
	// The shipped-default patterns ride along on every branch so the UI's
	// reset affordance works before a config is loaded too.
	if !slices.Equal(got.ExecuteBlacklistDefaults, config.DefaultExecuteGroupBlacklist()) {
		t.Errorf("ExecuteBlacklistDefaults = %v, want the shipped default patterns", got.ExecuteBlacklistDefaults)
	}
}

// TestGetSecuritySettings_ExecuteBlacklistDefaults verifies the reset
// affordance's data source: the shipped default patterns are always sent —
// regardless of what the stored execute blacklist is (unset, defaulted,
// customized, or emptied) — because they describe the app's defaults, not
// the user's current choice.
func TestGetSecuritySettings_ExecuteBlacklistDefaults(t *testing.T) {
	f, _, _ := newTestAPI(t)

	got := f.GetSecuritySettings()
	if !slices.Equal(got.ExecuteBlacklistDefaults, config.DefaultExecuteGroupBlacklist()) {
		t.Fatalf("ExecuteBlacklistDefaults = %v, want the shipped default patterns", got.ExecuteBlacklistDefaults)
	}

	// ...and still after the user empties the blacklist (the reset control
	// exists precisely for this state).
	if err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups: fullGroupPayload(map[string]GroupPolicyResponse{
			config.ToolGroupExecute: {Policy: config.GroupPolicyUserConfirm, Blacklist: []string{}},
		}),
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got = f.GetSecuritySettings()
	if !slices.Equal(got.ExecuteBlacklistDefaults, config.DefaultExecuteGroupBlacklist()) {
		t.Errorf("ExecuteBlacklistDefaults after emptying = %v, want the shipped default patterns", got.ExecuteBlacklistDefaults)
	}
}

// --- SmallLLMConfig ---

// validSmallLLMConfig is a profile that passes all validation rules. It is the
// baseline used by the happy-path tests; individual cases mutate copies.
func validSmallLLMConfig() SmallLLMConfigResponse {
	return SmallLLMConfigResponse{
		Enabled: true,
		EssentialTools: SmallLLMEssentialToolsResp{
			Enabled:       true,
			AlwaysPresent: []string{"read_file", "edit_file"},
			MaxTools:      8,
		},
		SystemPrompt: SmallLLMSystemPromptResp{Lite: true},
		Sampling: SmallLLMSamplingResp{
			Enabled:     true,
			Temperature: 0.1,
			TopP:        0.9,
		},
		LoopHardening: SmallLLMLoopHardeningResp{
			Enabled:                      true,
			RepeatNudgeThreshold:         2,
			ParseErrorAbortThreshold:     3,
			FruitlessNudgeThreshold:      3,
			FruitlessAbortThreshold:      5,
			SameToolRepeatNudgeThreshold: 4,
		},
		Context: SmallLLMContextResp{
			Enabled: true,
			Compaction: SmallLLMCompactionResp{
				KeepLast:       6,
				BlockSize:      5,
				TriggerPercent: 80,
			},
			ToolOutputKeepLastN: 2,
			OutputTokenReserve:  8192,
		},
	}
}

func TestGetSmallLLMConfig_ReturnsCurrentConfig(t *testing.T) {
	f, _, _ := newTestAPI(t)
	// ApplyDefaults seeds the SmallLLM section; mutate a couple of fields and
	// confirm they round-trip through the DTO.
	f.config.SmallLLM.Enabled = true
	f.config.SmallLLM.EssentialTools.Enabled = true
	f.config.SmallLLM.EssentialTools.MaxTools = 7

	got := f.GetSmallLLMConfig()

	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}
	if !got.EssentialTools.Enabled {
		t.Error("EssentialTools.Enabled = false, want true")
	}
	if got.EssentialTools.MaxTools != 7 {
		t.Errorf("MaxTools = %d, want 7", got.EssentialTools.MaxTools)
	}
	// AlwaysPresent should be a non-nil slice (JSON [] not null).
	if got.EssentialTools.AlwaysPresent == nil {
		t.Error("AlwaysPresent is nil, want non-nil")
	}
}

func TestGetSmallLLMConfig_NilConfigReturnsZero(t *testing.T) {
	f := &FrontendAPI{}
	got := f.GetSmallLLMConfig()
	if got.Enabled {
		t.Error("Enabled = true, want false for nil config")
	}
}

func TestUpdateSmallLLMConfig_PersistsAndRebuilds(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	err := f.UpdateSmallLLMConfig(validSmallLLMConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Router rebuilt so changes apply without restart.
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}

	// Config file persisted.
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Fatal("config file not persisted")
	}

	// In-memory config reflects the update.
	if !f.config.SmallLLM.Enabled {
		t.Error("SmallLLM.Enabled not applied")
	}
	if f.config.SmallLLM.EssentialTools.MaxTools != 8 {
		t.Errorf("MaxTools = %d, want 8", f.config.SmallLLM.EssentialTools.MaxTools)
	}
}

func TestUpdateSmallLLMConfig_NilConfig(t *testing.T) {
	f := &FrontendAPI{}
	err := f.UpdateSmallLLMConfig(validSmallLLMConfig())
	if err == nil {
		t.Fatal("expected error when config is nil")
	}
}

// TestUpdateSmallLLMConfig_PersistFailureRestoresInMemory verifies that when
// persistConfig fails, the in-memory config is restored to its previous value.
// Without this, the UI's revert-on-failure path (GetSmallLLMConfig) would read
// back the rejected value, silently keeping the failed change.
func TestUpdateSmallLLMConfig_PersistFailureRestoresInMemory(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	// Establish a known baseline.
	baseline := validSmallLLMConfig()
	baseline.EssentialTools.MaxTools = 9
	if err := f.UpdateSmallLLMConfig(baseline); err != nil {
		t.Fatalf("baseline setup failed: %v", err)
	}
	baselineCalls := mock.rebuildRouterCalls

	// Force persistConfig to fail by clearing the path.
	f.configPath = ""

	change := validSmallLLMConfig()
	// Must stay valid under validateSmallLLMConfig (guaranteed = 7 ≤ 10) so
	// the persist step itself is what fails, not validation.
	change.EssentialTools.MaxTools = 10
	err := f.UpdateSmallLLMConfig(change)
	if err == nil {
		t.Fatal("expected error when persist fails")
	}

	// In-memory config must be restored to the baseline, not the rejected change.
	if f.config.SmallLLM.EssentialTools.MaxTools != 9 {
		t.Errorf("in-memory MaxTools = %d, want 9 (baseline restored on persist failure)",
			f.config.SmallLLM.EssentialTools.MaxTools)
	}
	// RebuildRouter must not have been called for the failed update.
	if mock.rebuildRouterCalls != baselineCalls {
		t.Errorf("RebuildRouter called after persist failure: %d, want %d",
			mock.rebuildRouterCalls, baselineCalls)
	}
}

func TestUpdateSmallLLMConfig_EmptyAlwaysPresentAllowed(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	// An empty always_present list is valid: protected orchestration tools
	// (finish, fact memory, ask_user) and every MCP tool are always kept
	// implicitly by SelectTools, so the user need not pin anything.
	cfg := validSmallLLMConfig()
	cfg.EssentialTools.AlwaysPresent = nil

	err := f.UpdateSmallLLMConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error for empty always_present when essential enabled: %v", err)
	}

	// Successful update: config mutated, router rebuilt, file persisted.
	if !f.config.SmallLLM.Enabled {
		t.Error("config was not applied despite a valid (empty always_present) payload")
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}
	if _, statErr := os.Stat(cfgPath); statErr != nil {
		t.Error("config file should have been written on success")
	}
}

func TestUpdateSmallLLMConfig_NegativeMaxTools(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	cfg := validSmallLLMConfig()
	cfg.EssentialTools.MaxTools = -1

	err := f.UpdateSmallLLMConfig(cfg)
	if err == nil {
		t.Fatal("expected error for negative max_tools")
	}
	if f.config.SmallLLM.Enabled {
		t.Error("config was mutated despite validation error")
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

// TestUpdateSmallLLMConfig_SelfHealsMaxToolsBelowGuaranteed pins the
// save-time reconciliation: a cap below the guaranteed set is unenforceable
// (guaranteed tools are never trimmed), so instead of rejecting the save —
// which locked the settings panel behind a hand-editable-only error — the
// update path raises the cap to the guaranteed count. See
// TestReconcileSmallLLMCap_Passthrough for the sentinel/negative guards and
// TestValidateSmallLLMConfig_RejectsCapBelowGuaranteed for the retained
// validator safety net.
func TestUpdateSmallLLMConfig_SelfHealsMaxToolsBelowGuaranteed(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	// validSmallLLMConfig pins 2 tools; the guaranteed set is
	// 2 always-present ∪ 5 protected (no overlap) = 7. A cap of 6 would
	// leave zero router-matched slots and the never-trimmed guaranteed set
	// would exceed the budget — the save reconciles it to 7.
	cfg := validSmallLLMConfig()
	cfg.EssentialTools.MaxTools = 6

	if err := f.UpdateSmallLLMConfig(cfg); err != nil {
		t.Fatalf("save must succeed after reconciliation, got: %v", err)
	}
	if got := f.config.SmallLLM.EssentialTools.MaxTools; got != 7 {
		t.Errorf("MaxTools = %d, want 7 (raised to the guaranteed count)", got)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}
}

// TestValidateSmallLLMConfig_RejectsCapBelowGuaranteed keeps the validator's
// safety net covered: reconcileSmallLLMCap normally prevents this state from
// reaching validation via UpdateSmallLLMConfig, but the validator remains the
// invariant's single source of truth for any future caller.
func TestValidateSmallLLMConfig_RejectsCapBelowGuaranteed(t *testing.T) {
	cfg := validSmallLLMConfig()
	cfg.EssentialTools.MaxTools = 6

	err := validateSmallLLMConfig(cfg)
	if err == nil {
		t.Fatal("expected validator error when max_tools is below the guaranteed tool count")
	}
	if !strings.Contains(err.Error(), "guaranteed tool count") {
		t.Errorf("error should explain the guaranteed-count constraint, got: %v", err)
	}
	if !strings.Contains(err.Error(), "max_tools") {
		t.Errorf("error should name max_tools, got: %v", err)
	}
}

func TestUpdateSmallLLMConfig_NegativeThresholds(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	cfg := validSmallLLMConfig()
	cfg.LoopHardening.FruitlessAbortThreshold = -5

	err := f.UpdateSmallLLMConfig(cfg)
	if err == nil {
		t.Fatal("expected error for negative loop-hardening threshold")
	}
	if f.config.SmallLLM.Enabled {
		t.Error("config was mutated despite validation error")
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

func TestUpdateSmallLLMConfig_InvalidContextRanges(t *testing.T) {
	// Each case mutates one knob of an otherwise-valid enabled context
	// variant and expects rejection.
	cases := []struct {
		name   string
		mutate func(*SmallLLMConfigResponse)
	}{
		{
			name:   "keep_last below 2 rejected",
			mutate: func(c *SmallLLMConfigResponse) { c.Context.Compaction.KeepLast = 1 },
		},
		{
			name:   "trigger_percent 100 rejected",
			mutate: func(c *SmallLLMConfigResponse) { c.Context.Compaction.TriggerPercent = 100 },
		},
		{
			name:   "trigger_percent zero rejected",
			mutate: func(c *SmallLLMConfigResponse) { c.Context.Compaction.TriggerPercent = 0 },
		},
		{
			name:   "block_size below 2 rejected",
			mutate: func(c *SmallLLMConfigResponse) { c.Context.Compaction.BlockSize = 1 },
		},
		{
			name:   "tool_output_keep_last_n zero rejected",
			mutate: func(c *SmallLLMConfigResponse) { c.Context.ToolOutputKeepLastN = 0 },
		},
		{
			name:   "output_token_reserve zero rejected",
			mutate: func(c *SmallLLMConfigResponse) { c.Context.OutputTokenReserve = 0 },
		},
		{
			name:   "negative keep_last rejected",
			mutate: func(c *SmallLLMConfigResponse) { c.Context.Compaction.KeepLast = -3 },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, mock, _ := newTestAPI(t)

			cfg := validSmallLLMConfig()
			tc.mutate(&cfg)

			if err := f.UpdateSmallLLMConfig(cfg); err == nil {
				t.Fatal("expected validation error")
			}
			if f.config.SmallLLM.Enabled {
				t.Error("config was mutated despite validation error")
			}
			if mock.rebuildRouterCalls != 0 {
				t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
			}
		})
	}
}

func TestUpdateSmallLLMConfig_DisabledContextAllowsZeroValues(t *testing.T) {
	f, _, _ := newTestAPI(t)

	// Variant off → zero values are accepted (they mean "do not override").
	cfg := validSmallLLMConfig()
	cfg.Context = SmallLLMContextResp{}

	if err := f.UpdateSmallLLMConfig(cfg); err != nil {
		t.Fatalf("disabled context variant with zero values must be accepted, got: %v", err)
	}
}

func TestUpdateSmallLLMConfig_InvalidSampling(t *testing.T) {
	f, _, _ := newTestAPI(t)

	t.Run("negative temperature rejected, zero inherits", func(t *testing.T) {
		// Temperature 0 is the "inherit the vendor preset" sentinel: it must
		// be accepted so enabling the variant without explicit values keeps
		// vendor presets intact. Only explicitly negative values are invalid.
		cfg := validSmallLLMConfig()
		cfg.Sampling.Temperature = 0
		if err := f.UpdateSmallLLMConfig(cfg); err != nil {
			t.Fatalf("zero temperature means inherit and must be accepted, got: %v", err)
		}
		cfg = validSmallLLMConfig()
		cfg.Sampling.Temperature = -0.5
		if err := f.UpdateSmallLLMConfig(cfg); err == nil {
			t.Fatal("expected error for negative temperature")
		}
	})
	t.Run("top_p out of range", func(t *testing.T) {
		cfg := validSmallLLMConfig()
		cfg.Sampling.TopP = 1.5
		if err := f.UpdateSmallLLMConfig(cfg); err == nil {
			t.Fatal("expected error for top_p > 1")
		}
		cfg = validSmallLLMConfig()
		cfg.Sampling.TopP = -0.1
		if err := f.UpdateSmallLLMConfig(cfg); err == nil {
			t.Fatal("expected error for negative top_p")
		}
	})
	t.Run("top_k out of range", func(t *testing.T) {
		cfg := validSmallLLMConfig()
		cfg.Sampling.TopK = -3
		if err := f.UpdateSmallLLMConfig(cfg); err == nil {
			t.Fatal("expected error for negative top_k")
		}
		cfg = validSmallLLMConfig()
		cfg.Sampling.TopK = 0 // zero means inherit, valid
		if err := f.UpdateSmallLLMConfig(cfg); err != nil {
			t.Fatalf("zero top_k means inherit and must be accepted, got: %v", err)
		}
	})
	t.Run("repetition_penalty out of range", func(t *testing.T) {
		cfg := validSmallLLMConfig()
		cfg.Sampling.RepetitionPenalty = 0.5
		if err := f.UpdateSmallLLMConfig(cfg); err == nil {
			t.Fatal("expected error for repetition_penalty < 1")
		}
		cfg = validSmallLLMConfig()
		cfg.Sampling.RepetitionPenalty = 2.5
		if err := f.UpdateSmallLLMConfig(cfg); err == nil {
			t.Fatal("expected error for repetition_penalty > 2")
		}
		cfg = validSmallLLMConfig()
		cfg.Sampling.RepetitionPenalty = 0 // zero means inherit, valid
		if err := f.UpdateSmallLLMConfig(cfg); err != nil {
			t.Fatalf("zero repetition_penalty means inherit and must be accepted, got: %v", err)
		}
	})
	t.Run("presence_penalty out of range", func(t *testing.T) {
		cfg := validSmallLLMConfig()
		cfg.Sampling.PresencePenalty = 2.5
		err := f.UpdateSmallLLMConfig(cfg)
		if err == nil {
			t.Fatal("expected error for presence_penalty > 2")
		}
		if !strings.Contains(err.Error(), "presence_penalty") || !strings.Contains(err.Error(), "[0, 2]") {
			t.Errorf("error must name the field and its range, got: %v", err)
		}
		cfg = validSmallLLMConfig()
		cfg.Sampling.PresencePenalty = -0.5
		if err := f.UpdateSmallLLMConfig(cfg); err == nil {
			t.Fatal("expected error for negative presence_penalty")
		}
		cfg = validSmallLLMConfig()
		cfg.Sampling.PresencePenalty = 0 // zero means inherit, valid
		if err := f.UpdateSmallLLMConfig(cfg); err != nil {
			t.Fatalf("zero presence_penalty means inherit and must be accepted, got: %v", err)
		}
		cfg = validSmallLLMConfig()
		cfg.Sampling.PresencePenalty = 1.5 // Qwen instruct default, valid
		if err := f.UpdateSmallLLMConfig(cfg); err != nil {
			t.Fatalf("presence_penalty 1.5 (Qwen instruct default) must be accepted, got: %v", err)
		}
	})
	t.Run("invalid reasoning effort", func(t *testing.T) {
		cfg := validSmallLLMConfig()
		cfg.Sampling.ReasoningEffort = "ultra"
		if err := f.UpdateSmallLLMConfig(cfg); err == nil {
			t.Fatal("expected error for invalid reasoning_effort")
		}
	})
	t.Run("seeded default reasoning effort", func(t *testing.T) {
		// On a freshly defaulted config (ApplyDefaults runs inside
		// newTestAPI), GetSmallLLMConfig reports the seeded default
		// "medium" (docs/small-llm-defaults-research.md, R3): an unset
		// value would inherit the model's own default — xhigh on qwen
		// thinking models — which measured as overthinking on trivial
		// tasks. A fresh API is used because sibling subtests above
		// persist fixtures whose effort is "".
		fresh, _, _ := newTestAPI(t)
		if got := fresh.GetSmallLLMConfig().Sampling.ReasoningEffort; got != "medium" {
			t.Fatalf("GetSmallLLMConfig().Sampling.ReasoningEffort = %q on a fresh config, want the seeded default %q", got, "medium")
		}
		// The seeded default must validate and persist like any explicit
		// value.
		cfg := validSmallLLMConfig()
		cfg.Sampling.ReasoningEffort = "medium"
		if err := f.UpdateSmallLLMConfig(cfg); err != nil {
			t.Fatalf("seeded default reasoning_effort %q must be accepted, got: %v", "medium", err)
		}
	})
}

func TestUpdateSmallLLMConfig_DisabledVariantsAllowZeroValues(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	// When all variants are off (only master enabled), empty/zero values are
	// acceptable because the variant logic is inert. Master toggle itself
	// imposes no constraints on sub-fields.
	cfg := SmallLLMConfigResponse{Enabled: true}

	err := f.UpdateSmallLLMConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}
}

// TestSmallLLMConfig_RoundTrip_FullProfileLossless is the config round-trip
// integration test: a fully-populated profile written via UpdateSmallLLMConfig
// and read back via GetSmallLLMConfig must survive losslessly. This exercises
// the converter pair (smallLLMToResponse / responseToSmallLLM) end-to-end
// through the public API surface and the config.yaml persist path, covering
// EVERY field — including the ones the happy-path test omits (FewShot,
// ReasoningScaffold, ReasoningEffort, and all five loop-hardening thresholds) —
// so a future converter change that drops a field is caught.
func TestSmallLLMConfig_RoundTrip_FullProfileLossless(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	want := SmallLLMConfigResponse{
		Enabled: true,
		EssentialTools: SmallLLMEssentialToolsResp{
			Enabled:       true,
			AlwaysPresent: []string{"read_file", "edit_file", "bash_exec", "semantic_search"},
			MaxTools:      11,
		},
		SystemPrompt: SmallLLMSystemPromptResp{
			Lite:              true,
			FewShot:           true,
			ReasoningScaffold: true,
		},
		Sampling: SmallLLMSamplingResp{
			Enabled:           true,
			Temperature:       0.15,
			TopP:              0.85,
			TopK:              40,
			RepetitionPenalty: 1.15,
			PresencePenalty:   1.5,
			ReasoningEffort:   "low",
		},
		LoopHardening: SmallLLMLoopHardeningResp{
			Enabled:                      true,
			RepeatNudgeThreshold:         2,
			ParseErrorAbortThreshold:     3,
			FruitlessNudgeThreshold:      4,
			FruitlessAbortThreshold:      6,
			SameToolRepeatNudgeThreshold: 5,
		},
		Context: SmallLLMContextResp{
			Enabled: true,
			Compaction: SmallLLMCompactionResp{
				KeepLast:       6,
				BlockSize:      5,
				TriggerPercent: 80,
			},
			ToolOutputKeepLastN: 2,
			OutputTokenReserve:  8192,
		},
	}

	if err := f.UpdateSmallLLMConfig(want); err != nil {
		t.Fatalf("UpdateSmallLLMConfig failed: %v", err)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1 (change must apply without restart)", mock.rebuildRouterCalls)
	}

	// Read back through the public getter and assert every field survived.
	got := f.GetSmallLLMConfig()

	if got.Enabled != want.Enabled {
		t.Errorf("Enabled = %v, want %v", got.Enabled, want.Enabled)
	}

	// Essential tools.
	if got.EssentialTools.Enabled != want.EssentialTools.Enabled {
		t.Errorf("EssentialTools.Enabled = %v, want %v", got.EssentialTools.Enabled, want.EssentialTools.Enabled)
	}
	if got.EssentialTools.MaxTools != want.EssentialTools.MaxTools {
		t.Errorf("EssentialTools.MaxTools = %d, want %d", got.EssentialTools.MaxTools, want.EssentialTools.MaxTools)
	}
	// always_present round-trips the user-chosen tools losslessly AND carries
	// the protected orchestration tools unioned in by smallLLMToResponse (so
	// the UI can render them as locked). The want list contains no protected
	// tools, so the read-back is exactly the user list ∪ the protected set.
	gotSet := make(map[string]struct{}, len(got.EssentialTools.AlwaysPresent))
	for _, n := range got.EssentialTools.AlwaysPresent {
		gotSet[n] = struct{}{}
	}
	for _, n := range want.EssentialTools.AlwaysPresent {
		if _, ok := gotSet[n]; !ok {
			t.Errorf("EssentialTools.AlwaysPresent lost user tool %q; got %v", n, got.EssentialTools.AlwaysPresent)
		}
	}
	for _, n := range smallllm.ProtectedToolNames() {
		if _, ok := gotSet[n]; !ok {
			t.Errorf("EssentialTools.AlwaysPresent missing protected tool %q; got %v", n, got.EssentialTools.AlwaysPresent)
		}
	}
	wantLen := len(want.EssentialTools.AlwaysPresent) + len(smallllm.ProtectedToolNames())
	if len(got.EssentialTools.AlwaysPresent) != wantLen {
		t.Errorf("EssentialTools.AlwaysPresent len = %d, want %d (user ∪ protected); got %v",
			len(got.EssentialTools.AlwaysPresent), wantLen, got.EssentialTools.AlwaysPresent)
	}
	if got.EssentialTools.AlwaysPresent == nil {
		t.Error("EssentialTools.AlwaysPresent is nil, want non-nil (normalized to [])")
	}

	// System prompt.
	if got.SystemPrompt.Lite != want.SystemPrompt.Lite {
		t.Errorf("SystemPrompt.Lite = %v, want %v", got.SystemPrompt.Lite, want.SystemPrompt.Lite)
	}
	if got.SystemPrompt.FewShot != want.SystemPrompt.FewShot {
		t.Errorf("SystemPrompt.FewShot = %v, want %v", got.SystemPrompt.FewShot, want.SystemPrompt.FewShot)
	}
	if got.SystemPrompt.ReasoningScaffold != want.SystemPrompt.ReasoningScaffold {
		t.Errorf("SystemPrompt.ReasoningScaffold = %v, want %v", got.SystemPrompt.ReasoningScaffold, want.SystemPrompt.ReasoningScaffold)
	}

	// Sampling.
	if got.Sampling.Enabled != want.Sampling.Enabled {
		t.Errorf("Sampling.Enabled = %v, want %v", got.Sampling.Enabled, want.Sampling.Enabled)
	}
	if got.Sampling.Temperature != want.Sampling.Temperature {
		t.Errorf("Sampling.Temperature = %v, want %v", got.Sampling.Temperature, want.Sampling.Temperature)
	}
	if got.Sampling.TopP != want.Sampling.TopP {
		t.Errorf("Sampling.TopP = %v, want %v", got.Sampling.TopP, want.Sampling.TopP)
	}
	if got.Sampling.TopK != want.Sampling.TopK {
		t.Errorf("Sampling.TopK = %d, want %d", got.Sampling.TopK, want.Sampling.TopK)
	}
	if got.Sampling.RepetitionPenalty != want.Sampling.RepetitionPenalty {
		t.Errorf("Sampling.RepetitionPenalty = %v, want %v", got.Sampling.RepetitionPenalty, want.Sampling.RepetitionPenalty)
	}
	if got.Sampling.PresencePenalty != want.Sampling.PresencePenalty {
		t.Errorf("Sampling.PresencePenalty = %v, want %v", got.Sampling.PresencePenalty, want.Sampling.PresencePenalty)
	}
	if got.Sampling.ReasoningEffort != want.Sampling.ReasoningEffort {
		t.Errorf("Sampling.ReasoningEffort = %q, want %q", got.Sampling.ReasoningEffort, want.Sampling.ReasoningEffort)
	}

	// Loop hardening.
	if got.LoopHardening.Enabled != want.LoopHardening.Enabled {
		t.Errorf("LoopHardening.Enabled = %v, want %v", got.LoopHardening.Enabled, want.LoopHardening.Enabled)
	}
	if got.LoopHardening.RepeatNudgeThreshold != want.LoopHardening.RepeatNudgeThreshold {
		t.Errorf("LoopHardening.RepeatNudgeThreshold = %d, want %d", got.LoopHardening.RepeatNudgeThreshold, want.LoopHardening.RepeatNudgeThreshold)
	}
	if got.LoopHardening.ParseErrorAbortThreshold != want.LoopHardening.ParseErrorAbortThreshold {
		t.Errorf("LoopHardening.ParseErrorAbortThreshold = %d, want %d", got.LoopHardening.ParseErrorAbortThreshold, want.LoopHardening.ParseErrorAbortThreshold)
	}
	if got.LoopHardening.FruitlessNudgeThreshold != want.LoopHardening.FruitlessNudgeThreshold {
		t.Errorf("LoopHardening.FruitlessNudgeThreshold = %d, want %d", got.LoopHardening.FruitlessNudgeThreshold, want.LoopHardening.FruitlessNudgeThreshold)
	}
	if got.LoopHardening.FruitlessAbortThreshold != want.LoopHardening.FruitlessAbortThreshold {
		t.Errorf("LoopHardening.FruitlessAbortThreshold = %d, want %d", got.LoopHardening.FruitlessAbortThreshold, want.LoopHardening.FruitlessAbortThreshold)
	}
	if got.LoopHardening.SameToolRepeatNudgeThreshold != want.LoopHardening.SameToolRepeatNudgeThreshold {
		t.Errorf("LoopHardening.SameToolRepeatNudgeThreshold = %d, want %d", got.LoopHardening.SameToolRepeatNudgeThreshold, want.LoopHardening.SameToolRepeatNudgeThreshold)
	}

	// Context management.
	if got.Context.Enabled != want.Context.Enabled {
		t.Errorf("Context.Enabled = %v, want %v", got.Context.Enabled, want.Context.Enabled)
	}
	if got.Context.Compaction.KeepLast != want.Context.Compaction.KeepLast {
		t.Errorf("Context.Compaction.KeepLast = %d, want %d", got.Context.Compaction.KeepLast, want.Context.Compaction.KeepLast)
	}
	if got.Context.Compaction.BlockSize != want.Context.Compaction.BlockSize {
		t.Errorf("Context.Compaction.BlockSize = %d, want %d", got.Context.Compaction.BlockSize, want.Context.Compaction.BlockSize)
	}
	if got.Context.Compaction.TriggerPercent != want.Context.Compaction.TriggerPercent {
		t.Errorf("Context.Compaction.TriggerPercent = %d, want %d", got.Context.Compaction.TriggerPercent, want.Context.Compaction.TriggerPercent)
	}
	if got.Context.ToolOutputKeepLastN != want.Context.ToolOutputKeepLastN {
		t.Errorf("Context.ToolOutputKeepLastN = %d, want %d", got.Context.ToolOutputKeepLastN, want.Context.ToolOutputKeepLastN)
	}
	if got.Context.OutputTokenReserve != want.Context.OutputTokenReserve {
		t.Errorf("Context.OutputTokenReserve = %d, want %d", got.Context.OutputTokenReserve, want.Context.OutputTokenReserve)
	}
}

// --- GetConfig / maskAPIKey ---

func TestGetConfig_MasksAPIKeys(t *testing.T) {
	f, _, _ := newTestAPI(t)
	resp := f.GetConfig()
	if !resp.Loaded {
		t.Fatal("expected Loaded=true")
	}
	if resp.LLM.Anthropic.APIKey != maskedAPIKey {
		t.Errorf("API key not masked: got %q", resp.LLM.Anthropic.APIKey)
	}
}

func TestGetConfig_AnthropicCompatibleExposed(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.LLM.AnthropicCompatible = map[string]config.AnthropicCompatibleConfig{
		"my-proxy": {APIKey: "real-proxy-key", BaseURL: "https://proxy.example.com", Models: []string{"claude-sonnet-4-20250514"}},
	}
	resp := f.GetConfig()
	if resp.LLM.AnthropicCompatible == nil {
		t.Fatal("expected non-nil anthropic_compatible map in response")
	}
	got, ok := resp.LLM.AnthropicCompatible["my-proxy"]
	if !ok {
		t.Fatal("expected 'my-proxy' in anthropic_compatible response")
	}
	if got.APIKey != maskedAPIKey {
		t.Errorf("anthropic_compatible API key not masked: got %q", got.APIKey)
	}
	if got.BaseURL != "https://proxy.example.com" {
		t.Errorf("anthropic_compatible base_url = %q, want 'https://proxy.example.com'", got.BaseURL)
	}
	if len(got.Models) != 1 || got.Models[0] != "claude-sonnet-4-20250514" {
		t.Errorf("anthropic_compatible models = %v, want [claude-sonnet-4-20250514]", got.Models)
	}
}

// countingTransport is the network canary for GetConfig's network-free
// guarantee: it counts every HTTP round trip attempted through it and fails
// each one immediately.
type countingTransport struct {
	calls atomic.Int32
}

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("network access is forbidden in GetConfig")
}

// TestGetConfig_UnknownModelNoNetwork pins the network-free contract of
// GetConfig: with a live ModelRegistry wired and an enabled model unknown to
// every local tier, the call resolves from memory alone — no HuggingFace
// probe, no registered sources, no per-model latency. Before the switch to
// ResolveLocal this path fired an HTTP lookup (10s client timeout) per
// unknown model on every settings open.
func TestGetConfig_UnknownModelNoNetwork(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	transport := &countingTransport{}
	reg := llm.NewModelRegistry(nil)
	reg.SetHTTPClient(&http.Client{Transport: transport, Timeout: 10 * time.Second})
	mock.registry = reg

	// One known model per provider plus an unknown one: covers both the
	// metadata hit and the fallback path in a single GetConfig call.
	f.config.LLM.Anthropic.Models = []string{"claude-3-opus", "acme-unknown-model-xyz"}
	f.config.LLM.ChatGPT.Models = []string{"gpt-4o"}

	start := time.Now()
	resp := f.GetConfig()
	elapsed := time.Since(start)

	if !resp.LLM.ModelsReady {
		t.Fatal("expected ModelsReady=true with a live registry wired")
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("GetConfig attempted %d HTTP round trip(s); collectAllModels must resolve network-free", got)
	}
	// One accidental probe burns the registry's 10s HTTP timeout, far above
	// this budget; a memory-only read lands in the microseconds.
	if elapsed > 2*time.Second {
		t.Fatalf("GetConfig with an unknown model enabled took %v; expected an instant in-memory read", elapsed)
	}

	// Composition is unchanged: every enabled model appears, in provider
	// order (anthropic, then chatgpt), and known models keep their family
	// metadata.
	wantOrder := []struct{ name, provider string }{
		{"claude-3-opus", "anthropic"},
		{"acme-unknown-model-xyz", "anthropic"},
		{"gpt-4o", "chatgpt"},
	}
	if len(resp.LLM.AllModels) != len(wantOrder) {
		t.Fatalf("AllModels length = %d, want %d: %+v", len(resp.LLM.AllModels), len(wantOrder), resp.LLM.AllModels)
	}
	for i, want := range wantOrder {
		got := resp.LLM.AllModels[i]
		if got.Name != want.name || got.Provider != want.provider {
			t.Errorf("AllModels[%d] = {Name: %q, Provider: %q}, want {Name: %q, Provider: %q}", i, got.Name, got.Provider, want.name, want.provider)
		}
	}
	if got := resp.LLM.AllModels[2]; got.Family != "openai_flagship" {
		t.Errorf("gpt-4o family = %q, want openai_flagship (built-in metadata must still enrich known models)", got.Family)
	}
}

// TestGetConfig_NoRegistryKeepsModelsVisible guards the pre-init window: with
// no registry wired (ModelsReady=false) the configured models still appear so
// the model picker is never empty.
func TestGetConfig_NoRegistryKeepsModelsVisible(t *testing.T) {
	f, _, _ := newTestAPI(t)

	start := time.Now()
	resp := f.GetConfig()
	elapsed := time.Since(start)

	if resp.LLM.ModelsReady {
		t.Fatal("expected ModelsReady=false when no registry is wired")
	}
	if len(resp.LLM.AllModels) != 1 || resp.LLM.AllModels[0].Name != "claude-3-opus" {
		t.Fatalf("AllModels = %+v, want the single configured model", resp.LLM.AllModels)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("GetConfig without a registry took %v; expected an instant in-memory read", elapsed)
	}
}

func TestMaskAPIKey_EnvVarPreserved(t *testing.T) {
	if got := maskAPIKey("${ANTHROPIC_API_KEY}"); got != "${ANTHROPIC_API_KEY}" {
		t.Errorf("env var reference should not be masked, got %q", got)
	}
}

func TestMaskAPIKey_Empty(t *testing.T) {
	if got := maskAPIKey(""); got != "" {
		t.Errorf("empty key should return empty, got %q", got)
	}
}

// --- SetLogLevel ---

func TestSetLogLevel_ValidLevels(t *testing.T) {
	f, _, _ := newTestAPI(t)
	for _, level := range []string{"debug", "INFO", "Warn", "ERROR"} {
		if err := f.SetLogLevel(level); err != nil {
			t.Errorf("SetLogLevel(%q) returned error: %v", level, err)
		}
	}
}

func TestSetLogLevel_InvalidLevel(t *testing.T) {
	f, _, _ := newTestAPI(t)
	err := f.SetLogLevel("TRACE")
	if err == nil {
		t.Fatal("expected error for invalid level")
	}
}

// --- ListProviderModels ---

func TestListProviderModels_Delegates(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	mock.listProviderModelsRes = []string{"model-a", "model-b"}

	models, err := f.ListProviderModels("anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.listProviderModelsCalls != 1 {
		t.Errorf("ListProviderModels called %d times, want 1", mock.listProviderModelsCalls)
	}
	if len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" {
		t.Errorf("models = %v, want [model-a model-b]", models)
	}
}

// --- UpdateLLMConfig: project-switch regression ---

// eventRecorder captures emitted event names in order.
type eventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *eventRecorder) emit(name string, _ ...any) {
	r.mu.Lock()
	r.events = append(r.events, name)
	r.mu.Unlock()
}

func (r *eventRecorder) has(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e == name {
			return true
		}
	}
	return false
}

// names returns a copy of the captured event names (for failure messages).
func (r *eventRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// waitFor polls for the named event until a 2s deadline. config:updated is
// dispatched asynchronously (emitConfigUpdated spawns a goroutine so the
// Wails dispatch never runs under configMu), so tests asserting it must
// synchronize instead of checking immediately after the RPC returns.
func (r *eventRecorder) waitFor(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.has(name) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for event %q; captured events: %v", name, r.names())
}

// newUpdateLLMConfigProjectHarness builds a FrontendAPI wired to a real
// project store + manager, a temp config file, a mock builder, and an
// event recorder. The returned project is a freshly created real project
// (not No Project). activeProjectID is left unset; callers set it as needed.
func newUpdateLLMConfigProjectHarness(t *testing.T) (*FrontendAPI, *project.ProjectInfo, *eventRecorder, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db := openProjectSwitchTestDB(t)

	projStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create project store: %v", err)
	}
	agentDir := t.TempDir()
	projectManager := project.NewManager(projStore, agentDir, nil)
	createdProject, err := projectManager.CreateProject("Active Project", "")
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create test project: %v", err)
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	// Configure the provider backing the default model so the default is
	// resolvable (matches a valid post-save state). Without this, the
	// re-validation invariant in UpdateLLMConfig would clear a dangling
	// default before EnsureNoProject runs.
	cfg.LLM.DefaultModel = "claude-3-opus"
	cfg.LLM.Anthropic.APIKey = "sk-test"
	cfg.LLM.Anthropic.Models = []string{"claude-3-opus"}

	rec := &eventRecorder{}
	f := &FrontendAPI{
		config:          cfg,
		configPath:      cfgPath,
		builderOverride: &mockBuilder{},
		projectManager:  projectManager,
		projStore:       projStore,
		agentDir:        agentDir,
		emitEvent:       rec.emit,
		appCtx:          func() context.Context { return ctx },
	}
	_ = ctx
	return f, createdProject, rec, db
}

// TestUpdateExperimentalFeatures_EmitsConfigUpdated verifies the
// config:updated announcement: every config mutation funneling through
// persistConfig emits it (asynchronously) so frontend consumers that are
// still in the "unknown/not latched" state — e.g. the experimental-features
// switch whose initial GetConfig landed during the startup race — can
// re-read the config without an app restart.
func TestUpdateExperimentalFeatures_EmitsConfigUpdated(t *testing.T) {
	f, _, rec, db := newUpdateLLMConfigProjectHarness(t)
	defer func() { _ = db.Close() }()

	if err := f.UpdateExperimentalFeatures(true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec.waitFor(t, EventConfigUpdated)

	// The toggle itself must be reflected in the served config.
	if !f.experimentalFeaturesEnabled() {
		t.Fatal("expected experimental features to be enabled after the update")
	}
}

// activeProjectIDOf reads f.activeProjectID under its lock.
func activeProjectIDOf(f *FrontendAPI) string {
	f.activeProjectMu.RLock()
	defer f.activeProjectMu.RUnlock()
	return f.activeProjectID
}

// TestUpdateLLMConfig_DoesNotSwitchAwayFromActiveProject verifies that
// saving LLM config while a real project is active never tears it down
// or emits project:switched. Previously UpdateLLMConfig unconditionally
// called SwitchProject(NoProjectID), yanking the user out of CODE mode
// mid-session on every debounced save.
func TestUpdateLLMConfig_DoesNotSwitchAwayFromActiveProject(t *testing.T) {
	f, activeProj, rec, db := newUpdateLLMConfigProjectHarness(t)
	defer func() { _ = db.Close() }()

	f.activeProjectMu.Lock()
	f.activeProjectID = activeProj.ID
	f.activeProjectPath = activeProj.WorkspacePath
	f.activeProjectMu.Unlock()

	if err := f.UpdateLLMConfig(LLMFullConfigRequest{DefaultModel: "claude-3-opus"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := activeProjectIDOf(f); got != activeProj.ID {
		t.Fatalf("active project switched: got %q, want %q (must stay on the active real project)", got, activeProj.ID)
	}
	if rec.has(EventProjectSwitched) {
		t.Fatal("project:switched must not be emitted when a real project is active")
	}
}

// TestUpdateLLMConfig_EmitsBackendReadyOnlyWhenNoProjectFirstCreated
// verifies the first-run provisioning path: when No Project does not yet
// exist and no project is active, saving a valid LLM config emits
// backend:ready so the frontend can auto-select No Project — but still
// does not force a project:switched event.
func TestUpdateLLMConfig_EmitsBackendReadyOnlyWhenNoProjectFirstCreated(t *testing.T) {
	f, _, rec, db := newUpdateLLMConfigProjectHarness(t)
	defer func() { _ = db.Close() }()

	// No project active and No Project does not exist yet → first-run.
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{DefaultModel: "claude-3-opus"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !rec.has(EventBackendReady) {
		t.Fatal("expected backend:ready on first-run No Project creation")
	}
	if rec.has(EventProjectSwitched) {
		t.Fatal("project:switched must not be emitted; frontend auto-selects via loadAndActivate")
	}
	if got := activeProjectIDOf(f); got != "" {
		t.Fatalf("active project unexpectedly changed to %q; backend must not force-switch", got)
	}
}

// TestUpdateLLMConfig_NoEmitWhenNoProjectAlreadyExists verifies that
// mid-session config edits emit nothing once No Project already exists:
// the project list is unchanged, so re-emitting backend:ready would just
// cause redundant frontend work.
func TestUpdateLLMConfig_NoEmitWhenNoProjectAlreadyExists(t *testing.T) {
	f, activeProj, rec, db := newUpdateLLMConfigProjectHarness(t)
	defer func() { _ = db.Close() }()

	// Provision No Project up front so the config-save path has nothing to create.
	if _, err := f.projectManager.EnsureNoProject(); err != nil {
		t.Fatalf("failed to seed No Project: %v", err)
	}

	f.activeProjectMu.Lock()
	f.activeProjectID = activeProj.ID
	f.activeProjectPath = activeProj.WorkspacePath
	f.activeProjectMu.Unlock()

	if err := f.UpdateLLMConfig(LLMFullConfigRequest{DefaultModel: "claude-3-opus"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.has(EventBackendReady) {
		t.Fatal("backend:ready must not be re-emitted when No Project already exists")
	}
	if rec.has(EventProjectSwitched) {
		t.Fatal("project:switched must not be emitted on mid-session config edits")
	}
	if got := activeProjectIDOf(f); got != activeProj.ID {
		t.Fatalf("active project switched: got %q, want %q", got, activeProj.ID)
	}
}

// --- UpdateLLMConfig: lock-convoy regression ---

// TestUpdateLLMConfig_ReadersNotBlockedDuringRebuild verifies that GetConfig
// (a configMu.RLock reader) completes while UpdateLLMConfig is inside its slow
// rebuild phase. Previously the whole update held configMu.Lock across the
// YAML persist and the router rebuild, convoying every reader behind the
// rebuild. The rebuild hook blocks RebuildRouter, so a passing reader proves
// configMu is released during the heavy phase.
func TestUpdateLLMConfig_ReadersNotBlockedDuringRebuild(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	rebuildStarted := make(chan struct{})
	release := make(chan struct{})
	mock.rebuildRouterHook = func(*core.BuilderConfig) {
		select {
		case <-rebuildStarted:
		default:
			close(rebuildStarted)
		}
		<-release // hold the rebuild phase open until the test lets go
	}

	updateDone := make(chan error, 1)
	go func() {
		updateDone <- f.UpdateLLMConfig(LLMFullConfigRequest{
			DefaultModel: "claude-3-sonnet",
			Anthropic:    &ProviderConfigRequest{Models: []string{"claude-3-sonnet"}},
		})
	}()

	select {
	case <-rebuildStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("rebuild phase never started")
	}

	// The update is now blocked inside RebuildRouter. GetConfig must return
	// promptly: it takes configMu.RLock, which must be free during the heavy
	// phase. It must also observe the freshly applied (already mutated) config.
	type readerResult struct {
		resp ConfigResponse
		ok   bool
	}
	readerDone := make(chan readerResult, 1)
	go func() {
		resp := f.GetConfig()
		readerDone <- readerResult{resp: resp, ok: true}
	}()

	select {
	case res := <-readerDone:
		if !res.resp.Loaded {
			t.Error("GetConfig returned unloaded config during rebuild")
		}
		if res.resp.LLM.DefaultModel != "claude-3-sonnet" {
			t.Errorf("GetConfig default_model = %q, want claude-3-sonnet (mutation applied before rebuild)", res.resp.LLM.DefaultModel)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("GetConfig blocked while UpdateLLMConfig was in its rebuild phase — configMu lock convoy present")
	}

	close(release)
	if err := <-updateDone; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestUpdateLLMConfig_DeferredSavesSerializedInOrder verifies that overlapping
// (debounced) saves apply strictly in submission order under saveMu: while the
// first save is still mid-rebuild, a second save must not yet have mutated the
// in-memory config, and once both settle the router rebuilds happened in
// submission order and the persisted file matches the final in-memory state
// (the first save's persist can never overwrite the second save's state).
func TestUpdateLLMConfig_DeferredSavesSerializedInOrder(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	const first, second = "claude-3-sonnet", "claude-3-haiku"

	firstRebuildStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	mock.rebuildRouterHook = func(cfg *core.BuilderConfig) {
		if cfg.LLM.DefaultModel == first {
			select {
			case <-firstRebuildStarted:
			default:
				close(firstRebuildStarted)
			}
			<-releaseFirst
		}
	}

	saveErrs := make(chan error, 2)
	go func() {
		saveErrs <- f.UpdateLLMConfig(LLMFullConfigRequest{
			DefaultModel: first,
			Anthropic:    &ProviderConfigRequest{Models: []string{first}},
		})
	}()

	// Wait until the first save is parked inside its rebuild phase.
	select {
	case <-firstRebuildStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("first save never reached its rebuild phase")
	}

	// Submit the second save; it must queue on saveMu behind the first.
	go func() {
		saveErrs <- f.UpdateLLMConfig(LLMFullConfigRequest{
			DefaultModel: second,
			Anthropic:    &ProviderConfigRequest{Models: []string{second}},
		})
	}()

	// While the first save is still mid-flight, the config must still reflect
	// only the first save — the second save's mutation is held back by saveMu.
	time.Sleep(50 * time.Millisecond)
	f.configMu.RLock()
	mid := f.config.LLM.DefaultModel
	f.configMu.RUnlock()
	if mid != first {
		close(releaseFirst)
		t.Fatalf("second save mutated config before the first save completed: got %q, want %q", mid, first)
	}

	close(releaseFirst)
	for i := 0; i < 2; i++ {
		if err := <-saveErrs; err != nil {
			t.Fatalf("unexpected error from save %d: %v", i+1, err)
		}
	}

	if got := mock.routerCfgSnapshot(); len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("RebuildRouter order = %v, want [%s %s]", got, first, second)
	}

	// The persisted file must match the final in-memory state: serialization
	// prevents the first save's persist from landing after the second's.
	f.configMu.RLock()
	inMemory := f.config.LLM.DefaultModel
	f.configMu.RUnlock()
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load persisted config: %v", err)
	}
	if persisted.LLM.DefaultModel != inMemory {
		t.Fatalf("persisted default_model = %q, want %q (in-memory final state)", persisted.LLM.DefaultModel, inMemory)
	}
}

// TestUpdateLLMConfig_RebuildNotRevertedByConcurrentConfigWriter verifies that
// the router rebuild in UpdateLLMConfig cannot roll back changes made by a
// config writer that mutates and rebuilds under configMu.Lock
// (UpdateSmallLLMConfig, SetModelConfig). The rebuild must re-snapshot the
// config and hold configMu.RLock across snapshot + rebuild, so a concurrent
// writer's mutate+rebuild can never interleave between them and leave the
// router on a snapshot that predates its changes.
//
// The hook parks the FIRST RebuildRouter call (UpdateLLMConfig's) and records
// SmallLLM.Enabled of each rebuild in application (completion) order:
// with the fix the order is [false (LLM save), true (small-LLM save)] — the
// small-LLM rebuild lands last and wins; without it the stale LLM-save rebuild
// completes after the small-LLM one and the router is left on [.., false].
func TestUpdateLLMConfig_RebuildNotRevertedByConcurrentConfigWriter(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	// Experimental features gate the Small-LLM mapping in ToBuilderConfig;
	// enable them so the small-LLM save's rebuild snapshot reflects
	// SmallLLM.Enabled=true, which is what this test asserts about ordering.
	f.configMu.Lock()
	f.config.Experimental.Enabled = true
	f.configMu.Unlock()

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var orderMu sync.Mutex
	calls := 0
	applied := make([]bool, 0, 2)
	mock.rebuildRouterHook = func(cfg *core.BuilderConfig) {
		orderMu.Lock()
		calls++
		mine := calls
		orderMu.Unlock()
		if mine == 1 {
			close(firstEntered)
			<-releaseFirst // hold the LLM save's rebuild open
		}
		orderMu.Lock()
		applied = append(applied, cfg.SmallLLM.Enabled)
		orderMu.Unlock()
	}

	aDone := make(chan error, 1)
	go func() {
		aDone <- f.UpdateLLMConfig(LLMFullConfigRequest{
			DefaultModel: "claude-3-sonnet",
			Anthropic:    &ProviderConfigRequest{Models: []string{"claude-3-sonnet"}},
		})
	}()

	// Wait until the LLM save is parked inside its rebuild phase.
	select {
	case <-firstEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("LLM save never reached its rebuild phase")
	}

	// A config writer that is NOT serialized by saveMu races here. With the
	// fix it must block on configMu until the LLM save's rebuild completes.
	bDone := make(chan error, 1)
	go func() {
		bDone <- f.UpdateSmallLLMConfig(SmallLLMConfigResponse{Enabled: true})
	}()
	select {
	case err := <-bDone:
		close(releaseFirst)
		t.Fatalf("UpdateSmallLLMConfig completed while UpdateLLMConfig was inside its rebuild phase — the rebuild snapshot can be stale (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
		// Still blocked: expected under the fix.
	}

	close(releaseFirst)
	if err := <-aDone; err != nil {
		t.Fatalf("unexpected error from UpdateLLMConfig: %v", err)
	}
	if err := <-bDone; err != nil {
		t.Fatalf("unexpected error from UpdateSmallLLMConfig: %v", err)
	}

	orderMu.Lock()
	got := append([]bool(nil), applied...)
	orderMu.Unlock()
	if len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("router rebuild application order = %v, want [false true]: the router was left on a snapshot predating the concurrent small-LLM update", got)
	}

	// Sanity: the small-LLM change survived in memory and on disk.
	f.configMu.RLock()
	inMemory := f.config.SmallLLM.Enabled
	f.configMu.RUnlock()
	if !inMemory {
		t.Fatal("in-memory config lost the small-LLM master toggle")
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load persisted config: %v", err)
	}
	if !persisted.SmallLLM.Enabled {
		t.Fatal("persisted config lost the small-LLM master toggle")
	}
}

// TestHasDefaultModel verifies the cheap default-model probe used by the
// settings close check: nil config reports false, an empty default_model
// reports false, and any configured default reports true — without touching
// the model registry or the network.
func TestHasDefaultModel(t *testing.T) {
	tests := []struct {
		name string
		api  *FrontendAPI
		want bool
	}{
		{name: "nil config", api: &FrontendAPI{}, want: false},
		{name: "empty default model", api: &FrontendAPI{config: &config.Config{}}, want: false},
		{name: "configured default model", api: &FrontendAPI{config: &config.Config{LLM: config.LLMConfig{DefaultModel: "anthropic/claude-sonnet-4"}}}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.api.HasDefaultModel(); got != tc.want {
				t.Errorf("HasDefaultModel() = %v, want %v", got, tc.want)
			}
		})
	}
}
