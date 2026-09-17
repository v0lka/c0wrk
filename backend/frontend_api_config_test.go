package backend

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/proxy"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
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
	// listProviderModelsLastProvider / LastCfg capture the most recent
	// ListProviderModels arguments so tests can assert draft-credential merges.
	listProviderModelsLastProvider string
	listProviderModelsLastCfg      *core.BuilderConfig
	optimizePromptRes              *core.OptimizePromptResult
	optimizePromptErr              error
	generateCommitMsgRes           string
	generateCommitMsgErr           error
	generateCommitMsgDiff          string

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
func (m *mockBuilder) ListProviderModels(_ context.Context, provider string, cfg *core.BuilderConfig) ([]string, error) {
	m.mu.Lock()
	m.listProviderModelsCalls++
	m.listProviderModelsLastProvider = provider
	m.listProviderModelsLastCfg = cfg
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

// activateCustomModelProfile creates a writable custom ModelProfiles profile in f's
// agent-dir store and makes it the active one, mirroring the production state
// where knob edits target the active custom profile (predefined profiles are
// read-only). Returns the activated profile.
func activateCustomModelProfile(t *testing.T, f *FrontendAPI) config.ModelProfile {
	t.Helper()
	profile, err := config.CreateCustomModelProfile("Test Tuned", config.ModelProfileConfig{}, nil)
	if err != nil {
		t.Fatalf("CreateCustomModelProfile: %v", err)
	}
	if err := config.SaveCustomModelProfiles(config.ModelProfilesPath(f.agentDir), []config.ModelProfile{profile}); err != nil {
		t.Fatalf("SaveCustomModelProfiles: %v", err)
	}
	f.config.ModelProfiles.ActiveProfile = profile.ID
	return profile
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
	// Knob values live in the active profile: activate a writable custom one
	// so ModelProfiles profile mutations have an editable target (the default "generic" active
	// profile is predefined/read-only).
	activateCustomModelProfile(t, f)
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

// --- UpdateVectorIndexSettings ---

// TestUpdateVectorIndexSettings_PersistsAndRoundTrips verifies that a valid
// update mutates the in-memory config, survives a Load round-trip from disk,
// and is served back by GetConfig's vector_index block.
func TestUpdateVectorIndexSettings_PersistsAndRoundTrips(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)

	err := f.UpdateVectorIndexSettings(VectorIndexSettingsResponse{
		ExecutionProvider: config.VectorIndexProviderCUDA,
		DeviceID:          2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// In-memory state applied.
	if got := f.config.VectorIndex.ExecutionProvider; got != config.VectorIndexProviderCUDA {
		t.Errorf("in-memory execution_provider = %q, want %q", got, config.VectorIndexProviderCUDA)
	}
	if got := f.config.VectorIndex.DeviceID; got != 2 {
		t.Errorf("in-memory device_id = %d, want 2", got)
	}

	// Persisted file round-trips through Load.
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to reload persisted config: %v", err)
	}
	if got := reloaded.VectorIndex.ExecutionProvider; got != config.VectorIndexProviderCUDA {
		t.Errorf("persisted execution_provider = %q, want %q", got, config.VectorIndexProviderCUDA)
	}
	if got := reloaded.VectorIndex.DeviceID; got != 2 {
		t.Errorf("persisted device_id = %d, want 2", got)
	}

	// GetConfig exposes the vector_index block.
	resp := f.GetConfig()
	if got := resp.VectorIndex.ExecutionProvider; got != config.VectorIndexProviderCUDA {
		t.Errorf("GetConfig execution_provider = %q, want %q", got, config.VectorIndexProviderCUDA)
	}
	if got := resp.VectorIndex.DeviceID; got != 2 {
		t.Errorf("GetConfig device_id = %d, want 2", got)
	}
}

// TestUpdateVectorIndexSettings_InvalidValuesRejectedBeforeWrite verifies
// that an invalid provider or a negative device id is rejected BEFORE any
// mutation or disk write: the in-memory config keeps its prior values and
// the persisted file remains loadable with those prior values (a restart
// can never brick the app on a rejected request).
func TestUpdateVectorIndexSettings_InvalidValuesRejectedBeforeWrite(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)

	// Seed a known-good persisted state first.
	if err := f.UpdateVectorIndexSettings(VectorIndexSettingsResponse{
		ExecutionProvider: config.VectorIndexProviderCPU,
		DeviceID:          1,
	}); err != nil {
		t.Fatalf("failed to seed valid state: %v", err)
	}

	tests := []struct {
		name     string
		req      VectorIndexSettingsResponse
		wantPart string
	}{
		{
			name:     "unknown provider",
			req:      VectorIndexSettingsResponse{ExecutionProvider: "tpu", DeviceID: 1},
			wantPart: "vector_index.execution_provider",
		},
		{
			name:     "empty provider",
			req:      VectorIndexSettingsResponse{ExecutionProvider: "", DeviceID: 1},
			wantPart: "vector_index.execution_provider",
		},
		{
			name:     "negative device id",
			req:      VectorIndexSettingsResponse{ExecutionProvider: config.VectorIndexProviderCUDA, DeviceID: -1},
			wantPart: "vector_index.device_id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := f.UpdateVectorIndexSettings(tt.req)
			if err == nil {
				t.Fatal("expected error for invalid settings")
			}
			if !strings.Contains(err.Error(), tt.wantPart) {
				t.Errorf("error %q does not mention %q", err.Error(), tt.wantPart)
			}

			// In-memory state untouched.
			if got := f.config.VectorIndex.ExecutionProvider; got != config.VectorIndexProviderCPU {
				t.Errorf("in-memory execution_provider mutated to %q, want %q", got, config.VectorIndexProviderCPU)
			}
			if got := f.config.VectorIndex.DeviceID; got != 1 {
				t.Errorf("in-memory device_id mutated to %d, want 1", got)
			}

			// Persisted file still loads cleanly with the prior values.
			reloaded, loadErr := config.Load(cfgPath)
			if loadErr != nil {
				t.Fatalf("persisted config no longer loadable after rejected update: %v", loadErr)
			}
			if got := reloaded.VectorIndex.ExecutionProvider; got != config.VectorIndexProviderCPU {
				t.Errorf("persisted execution_provider = %q, want %q (unchanged)", got, config.VectorIndexProviderCPU)
			}
			if got := reloaded.VectorIndex.DeviceID; got != 1 {
				t.Errorf("persisted device_id = %d, want 1 (unchanged)", got)
			}
		})
	}
}

func TestUpdateVectorIndexSettings_NilConfig(t *testing.T) {
	f := &FrontendAPI{}
	err := f.UpdateVectorIndexSettings(VectorIndexSettingsResponse{
		ExecutionProvider: config.VectorIndexProviderCPU,
	})
	if err == nil {
		t.Fatal("expected error when config is nil")
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

// --- ModelProfilesConfig ---

// validModelProfilesConfig is a profile that passes all validation rules. It is the
// baseline used by the happy-path tests; individual cases mutate copies.
func validModelProfilesValues() ModelProfileValues {
	return ModelProfileValues{
		EssentialTools: ModelProfilesEssentialToolsValues{
			Enabled:       true,
			AlwaysPresent: []string{"read_file", "edit_file"},
		},
		SystemPrompt: ModelProfilesSystemPromptResp{Lite: true},
		Sampling: ModelProfilesSamplingResp{
			Enabled:     true,
			Temperature: 0.1,
			TopP:        0.9,
		},
		LoopHardening: ModelProfilesLoopHardeningResp{
			Enabled:                      true,
			RepeatNudgeThreshold:         2,
			ParseErrorAbortThreshold:     3,
			FruitlessNudgeThreshold:      3,
			FruitlessAbortThreshold:      5,
			SameToolRepeatNudgeThreshold: 4,
		},
		Context: ModelProfilesContextResp{
			Enabled: true,
			Compaction: ModelProfilesCompactionResp{
				KeepLast:       6,
				BlockSize:      5,
				TriggerPercent: 80,
			},
			ToolOutputKeepLastN: 2,
			OutputTokenReserve:  8192,
		},
	}
}

// modelProfilesConfigReq wraps a copy of v into a values-only update request.
func modelProfilesConfigReq(v ModelProfileValues) ModelProfileUpdateRequest {
	cfg := v
	return ModelProfileUpdateRequest{Config: &cfg}
}

// modelProfilesFindProfile locates a profile DTO by id in a GetModelProfiles response.
func modelProfilesFindProfile(t *testing.T, resp ModelProfilesResponse, id string) ModelProfileDTO {
	t.Helper()
	for _, p := range resp.Profiles {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("profile %q not found in the GetModelProfiles response", id)
	return ModelProfileDTO{}
}

// modelProfilesStoredProfile reads the custom store under f's agent dir.
func modelProfilesStoredProfile(t *testing.T, f *FrontendAPI, id string) config.ModelProfile {
	t.Helper()
	profiles, _ := config.LoadCustomModelProfiles(config.ModelProfilesPath(f.agentDir))
	for _, p := range profiles {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("custom profile %q not found in the store", id)
	return config.ModelProfile{}
}

// --- GetModelProfiles ---

func TestGetModelProfiles_ReturnsCatalog(t *testing.T) {
	f, _, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	// Push distinctive values through the public write path.
	want := validModelProfilesValues()
	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(want)); err != nil {
		t.Fatalf("UpdateModelProfile: %v", err)
	}

	got := f.GetModelProfiles()

	if got.ActiveID != active.ID {
		t.Errorf("ActiveID = %q, want %q", got.ActiveID, active.ID)
	}
	// The full catalog: 5 predefined + the custom(s) created above.
	nPredefined := 0
	nCustom := 0
	for _, p := range got.Profiles {
		switch p.Kind {
		case "predefined":
			nPredefined++
			if p.Name == "" {
				t.Errorf("predefined profile %q has an empty name", p.ID)
			}
		case "custom":
			nCustom++
		default:
			t.Errorf("profile %q has unexpected kind %q", p.ID, p.Kind)
		}
	}
	if nPredefined != len(config.PredefinedModelProfiles()) {
		t.Errorf("predefined profiles = %d, want %d", nPredefined, len(config.PredefinedModelProfiles()))
	}
	if nCustom == 0 {
		t.Error("no custom profiles in the catalog despite the activated store profile")
	}

	// The active profile's values round-trip through the DTO.
	dto := modelProfilesFindProfile(t, got, active.ID)
	if !dto.Values.EssentialTools.Enabled || len(dto.Values.EssentialTools.AlwaysPresent) != 2 {
		t.Errorf("active profile values did not round-trip: %+v", dto.Values.EssentialTools)
	}
	if !dto.Values.Sampling.Enabled || dto.Values.Sampling.Temperature != 0.1 {
		t.Errorf("sampling values did not round-trip: %+v", dto.Values.Sampling)
	}
	// AlwaysPresent must be a non-nil slice (JSON [] not null).
	if dto.Values.EssentialTools.AlwaysPresent == nil {
		t.Error("AlwaysPresent is nil, want non-nil")
	}
}

func TestGetModelProfiles_NilConfigReturnsCatalogAndUniverse(t *testing.T) {
	f := &FrontendAPI{}
	got := f.GetModelProfiles()
	if got.ActiveID != "" {
		t.Errorf("ActiveID = %q, want empty for nil config", got.ActiveID)
	}
	if got.Enabled {
		t.Error("Enabled = true, want false for nil config")
	}
	if got.SuggestedProfileID != nil {
		t.Errorf("SuggestedProfileID = %v, want nil for nil config", *got.SuggestedProfileID)
	}
	if len(got.Profiles) != len(config.PredefinedModelProfiles()) {
		t.Errorf("profiles = %d, want the %d predefined entries (catalog is config-independent)",
			len(got.Profiles), len(config.PredefinedModelProfiles()))
	}
	if got.BuiltinTools == nil || got.ToolGroups == nil || got.Warnings == nil || got.Profiles == nil {
		t.Error("slices must be non-nil (JSON [] not null) even without an app/config")
	}
}

// TestGetModelProfiles_ReportsStoredEnabled verifies the master toggle is echoed
// verbatim from config.yaml (model_profiles.enabled) — it is not a per-profile value.
func TestGetModelProfiles_ReportsStoredEnabled(t *testing.T) {
	f, _, _ := newTestAPI(t)

	f.config.ModelProfiles.Enabled = true
	if got := f.GetModelProfiles(); !got.Enabled {
		t.Error("Enabled = false, want true when model_profiles.enabled is true")
	}

	f.config.ModelProfiles.Enabled = false
	if got := f.GetModelProfiles(); got.Enabled {
		t.Error("Enabled = true, want false when model_profiles.enabled is false")
	}
}

func TestGetModelProfiles_PickerAlwaysNonNil(t *testing.T) {
	f := &FrontendAPI{} // no app: registry unavailable
	got := f.GetModelProfiles()
	if got.BuiltinTools == nil || len(got.BuiltinTools) != 0 {
		t.Errorf("BuiltinTools = %v, want empty non-nil when the registry is unavailable", got.BuiltinTools)
	}
	if got.ToolGroups == nil || len(got.ToolGroups) != 0 {
		t.Errorf("ToolGroups = %v, want empty non-nil when the registry is unavailable", got.ToolGroups)
	}
}

func TestGetModelProfiles_SuggestedFromDefaultModel(t *testing.T) {
	cases := []struct {
		defaultModel string
		want         string // "" means nil (no suggestion)
	}{
		{"qwen3.8-27b", "qwen3.8-27b"},
		{"Qwen/Qwen3.8-27B", "qwen3.8-27b"},
		{"Qwen/Qwen3.8-27B-Instruct", "qwen3.8-27b"},
		{"gemma-4-26b-a4b-it", "gemma-4-26b-a4b-it"},
		{"my-custom-model", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.defaultModel, func(t *testing.T) {
			f, _, _ := newTestAPI(t)
			f.config.LLM.DefaultModel = tc.defaultModel
			got := f.GetModelProfiles()
			if tc.want == "" {
				if got.SuggestedProfileID != nil {
					t.Fatalf("SuggestedProfileID = %q, want nil", *got.SuggestedProfileID)
				}
				return
			}
			if got.SuggestedProfileID == nil {
				t.Fatalf("SuggestedProfileID = nil, want %q", tc.want)
			}
			if *got.SuggestedProfileID != tc.want {
				t.Fatalf("SuggestedProfileID = %q, want %q", *got.SuggestedProfileID, tc.want)
			}
		})
	}
}

// TestSuggestModelProfileID_Normalization exercises the pure matcher: provider
// prefixes are stripped ("Qwen/", "openrouter/qwen/", ":" keys), trailing
// marketing suffixes collapse ("-instruct", "-it", ":free"), separators are
// irrelevant, "generic" is never suggested, and unknown/custom names yield no
// match.
func TestSuggestModelProfileID_Normalization(t *testing.T) {
	cases := []struct{ model, want string }{
		{"qwen3.8-27b", "qwen3.8-27b"},
		{"Qwen/Qwen3.8-27B", "qwen3.8-27b"},
		{"qwen3.8-27b-instruct-2507", "qwen3.8-27b"},
		{"Qwen/Qwen3.6-35B-A3B-Instruct", "qwen3.6-35b-a3b"},
		{"openrouter/qwen/qwen3.6-35b-a3b:free", "qwen3.6-35b-a3b"},
		{"google/gemma-4-26b-a4b-it", "gemma-4-26b-a4b-it"},
		{"gemma-4-31b-it", "gemma-4-31b-it"},
		{"qwen3_8_27b", "qwen3.8-27b"},
		// No matches.
		{"claude-sonnet-4", ""},
		{"my-tuned-model", ""},
		{"Test Tuned", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := suggestModelProfileID(tc.model); got != tc.want {
			t.Errorf("suggestModelProfileID(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestGetModelProfiles_DanglingActiveWarns(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.ModelProfiles.ActiveProfile = "ghost-profile"

	got := f.GetModelProfiles()
	if got.ActiveID != "ghost-profile" {
		t.Errorf("ActiveID = %q, want the stored (dangling) id", got.ActiveID)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("expected a resolver warning for the dangling active id")
	}
}

// TestBuiltinToolInfos_ExcludesNonNarrowable verifies the always-present picker
// universe helper: MCP-sourced tools are excluded (the selection always keeps
// them implicitly, so pinning one is a no-op) and goal-mode-only tools too (they
// are stripped from every non-goal run before the selection runs and the
// selection is not applied in goal mode), while every remaining built-in —
// including the reserved system/orchestration group — is included, sorted by
// name for a stable UI list, each carrying its registry description.
func TestBuiltinToolInfos_ExcludesNonNarrowable(t *testing.T) {
	descriptors := []sdktools.ToolDescriptor{
		{Name: "web_search", SourceCategory: sdktools.SourceCategoryCore, Description: "search the web"},
		{Name: "read_file", SourceCategory: sdktools.SourceCategoryCore, Description: "read a file"},
		{Name: "delegate", SourceCategory: sdktools.SourceCategoryCore, Description: "run a subagent"},
		{Name: "finish", SourceCategory: sdktools.SourceCategoryCore, Description: "finish the task"},
		{Name: "mcp_linter", SourceCategory: sdktools.SourceCategoryMCP, Description: "lint"},
		{Name: "propose_goal", SourceCategory: sdktools.SourceCategoryCore, Description: "propose a goal"},
		{Name: "declare_verification", SourceCategory: sdktools.SourceCategoryCore, Description: "verify"},
	}

	got := builtinToolInfos(descriptors)
	wantNames := []string{"delegate", "finish", "read_file", "web_search"}
	gotNames := make([]string, 0, len(got))
	for _, g := range got {
		if g.Description == "" {
			t.Errorf("tool %q has an empty description", g.Name)
		}
		gotNames = append(gotNames, g.Name)
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Errorf("builtinToolInfos names = %v, want %v (narrowable built-ins only, sorted)", gotNames, wantNames)
	}
	for _, g := range got {
		if g.Name == "mcp_linter" {
			t.Error("MCP-sourced tool leaked into the built-in tool universe")
		}
		if coretools.IsGoalModeTool(g.Name) {
			t.Errorf("goal-mode-only tool %q leaked into the built-in tool universe", g.Name)
		}
	}
}

// TestModelProfilesToolGroups_FilteredToRegistered verifies the cluster projection:
// member names absent from the picker universe are dropped, a cluster left with
// no surviving member is omitted, and the surviving members keep catalog order.
func TestModelProfilesToolGroups_FilteredToRegistered(t *testing.T) {
	// A subset of the plan cluster plus a delegate tool is in the universe, so
	// the subagents cluster keeps only delegate and the plan cluster only its two
	// present members.
	universe := []ModelProfilesBuiltinTool{
		{Name: "declare_plan"},
		{Name: "update_checklist"},
		{Name: "delegate"},
		{Name: "unrelated_tool"},
	}

	got := modelProfilesToolGroups(universe)
	byID := make(map[string]ModelProfilesToolGroup, len(got))
	for _, g := range got {
		byID[g.ID] = g
	}

	plan, ok := byID["plan"]
	if !ok {
		t.Fatal("plan cluster missing despite two present members")
	}
	if want := []string{"declare_plan", "update_checklist"}; !slices.Equal(plan.Tools, want) {
		t.Errorf("plan members = %v, want %v (present only, catalog order)", plan.Tools, want)
	}
	if plan.Title == "" || plan.Description == "" {
		t.Error("plan cluster is missing title/description")
	}

	subagents, ok := byID["subagents"]
	if !ok {
		t.Fatal("subagents cluster missing despite delegate being present")
	}
	if want := []string{"delegate"}; !slices.Equal(subagents.Tools, want) {
		t.Errorf("subagents members = %v, want %v (only delegate is present)", subagents.Tools, want)
	}

	if len(got) != 2 {
		t.Errorf("got %d clusters, want 2 (plan and subagents)", len(got))
	}

	// A cluster whose members are all absent from the universe is dropped: with
	// only a plan member present, the subagents cluster has no surviving member.
	onlyPlan := modelProfilesToolGroups([]ModelProfilesBuiltinTool{{Name: "execute_plan"}})
	if len(onlyPlan) != 1 || onlyPlan[0].ID != "plan" {
		t.Errorf("clusters = %v, want just plan (subagents has no present member)", onlyPlan)
	}
}

// --- CreateModelProfile ---

func TestCreateModelProfile_FromPredefinedBase(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	id, err := f.CreateModelProfile("qwen3.8-27b", "My Qwen")
	if err != nil {
		t.Fatalf("CreateModelProfile: %v", err)
	}
	if id != "my-qwen" {
		t.Errorf("generated id = %q, want my-qwen", id)
	}

	// Values copied from the base, kind custom, active unchanged.
	created := modelProfilesStoredProfile(t, f, id)
	base, _ := config.FindPredefinedModelProfile("qwen3.8-27b")
	if !reflect.DeepEqual(created.Config, base.Config) {
		t.Errorf("created values differ from the base profile")
	}
	if f.config.ModelProfiles.ActiveProfile != active.ID {
		t.Errorf("active profile changed to %q, create must not select", f.config.ModelProfiles.ActiveProfile)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}
}

func TestCreateModelProfile_EmptyBaseMeansGeneric(t *testing.T) {
	f, _, _ := newTestAPI(t)

	id, err := f.CreateModelProfile("", "From Generic")
	if err != nil {
		t.Fatalf("CreateModelProfile: %v", err)
	}
	created := modelProfilesStoredProfile(t, f, id)
	generic, _ := config.FindPredefinedModelProfile(config.ModelProfilesGenericProfileID)
	if !reflect.DeepEqual(created.Config, generic.Config) {
		t.Error("created values differ from the generic base")
	}
}

func TestCreateModelProfile_UnknownBaseRejected(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	if _, err := f.CreateModelProfile("no-such-profile", "X"); err == nil {
		t.Fatal("expected error for an unknown base id")
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

func TestCreateModelProfile_NameCollisionsRejected(t *testing.T) {
	f, _, _ := newTestAPI(t)

	predefined := config.PredefinedModelProfiles()[0]
	cases := []struct {
		name   string
		reason string
	}{
		{predefined.Name, "collides with a predefined profile name"},
		{"Test Tuned", "collides with an existing custom profile name"},
		{"   ", "empty after trim"},
	}
	for _, tc := range cases {
		if _, err := f.CreateModelProfile("generic", tc.name); err == nil {
			t.Errorf("expected error for a name that %s, got nil", tc.reason)
		}
	}
	// The store is untouched by the rejected creates.
	profiles, _ := config.LoadCustomModelProfiles(config.ModelProfilesPath(f.agentDir))
	if len(profiles) != 1 {
		t.Errorf("custom store = %d profiles, want 1 (untouched)", len(profiles))
	}
}

// --- UpdateModelProfile ---

func TestUpdateModelProfile_PersistsAndRebuilds(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(validModelProfilesValues())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Router rebuilt so changes apply without restart.
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}
	// Values persisted in the store.
	stored := modelProfilesStoredProfile(t, f, active.ID)
	if !stored.Config.EssentialTools.Enabled {
		t.Error("stored values were not applied")
	}
}

func TestUpdateModelProfile_NilConfig(t *testing.T) {
	f := &FrontendAPI{}
	err := f.UpdateModelProfile("any", modelProfilesConfigReq(validModelProfilesValues()))
	if err == nil {
		t.Fatal("expected error when config is nil")
	}
}

// TestUpdateModelProfile_PredefinedRejected verifies the read-only semantics:
// predefined profiles are hard-coded, so an update is rejected before any
// mutation of config.yaml or the profile store.
func TestUpdateModelProfile_PredefinedRejected(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	err := f.UpdateModelProfile(config.ModelProfilesGenericProfileID, modelProfilesConfigReq(validModelProfilesValues()))
	if err == nil {
		t.Fatal("expected error for a predefined profile id")
	}
	if !strings.Contains(err.Error(), "predefined") {
		t.Errorf("error must explain the read-only predefined profile, got: %v", err)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0 (nothing was applied)", mock.rebuildRouterCalls)
	}
	// The custom store must be untouched.
	if err := f.UpdateModelProfile("unknown-id", modelProfilesConfigReq(validModelProfilesValues())); err == nil {
		t.Fatal("expected error for an unknown profile id")
	}
	profiles, _ := config.LoadCustomModelProfiles(config.ModelProfilesPath(f.agentDir))
	if len(profiles) == 0 {
		t.Fatal("custom store lost its profiles from rejected updates")
	}
	_ = active
}

func TestUpdateModelProfile_UnknownIDRejected(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	if err := f.UpdateModelProfile("ghost", modelProfilesConfigReq(validModelProfilesValues())); err == nil {
		t.Fatal("expected error for an unknown profile id")
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

func TestUpdateModelProfile_Rename(t *testing.T) {
	f, _, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	newName := "Renamed Tuned"
	if err := f.UpdateModelProfile(active.ID, ModelProfileUpdateRequest{Name: &newName}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	stored := modelProfilesStoredProfile(t, f, active.ID)
	if stored.Name != newName {
		t.Errorf("stored name = %q, want %q", stored.Name, newName)
	}
	if !stored.Config.EssentialTools.Enabled && stored.ID != active.ID {
		t.Error("rename must keep id and values stable")
	}
}

// TestUpdateModelProfile_RenameCollisionRejected: a rename that lands on a
// predefined name or another custom profile's name is rejected without a
// write; renaming to the profile's own name is a no-op and allowed.
func TestUpdateModelProfile_RenameCollisionRejected(t *testing.T) {
	f, _, _ := newTestAPI(t)
	first := activateCustomModelProfile(t, f) // "Test Tuned"
	secondID, err := f.CreateModelProfile("generic", "Second Profile")
	if err != nil {
		t.Fatalf("CreateModelProfile: %v", err)
	}

	predefinedName := config.PredefinedModelProfiles()[0].Name
	for _, tc := range []struct{ name, why string }{
		{predefinedName, "predefined name"},
		{first.Name, "another custom profile's name"},
		{"  ", "empty after trim"},
	} {
		name := tc.name
		if err := f.UpdateModelProfile(secondID, ModelProfileUpdateRequest{Name: &name}); err == nil {
			t.Errorf("rename to %s (%q) must be rejected", tc.why, tc.name)
		}
	}
	// Store unchanged: both profiles keep their names.
	if got := modelProfilesStoredProfile(t, f, secondID).Name; got != "Second Profile" {
		t.Errorf("rejected rename leaked: second profile name = %q, want Second Profile", got)
	}
	// Renaming to the own name is allowed (no-op).
	own := "Second Profile"
	if err := f.UpdateModelProfile(secondID, ModelProfileUpdateRequest{Name: &own}); err != nil {
		t.Fatalf("rename to the own name must be allowed, got: %v", err)
	}
}

func TestUpdateModelProfile_PartialFields(t *testing.T) {
	f, _, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	// Baseline values.
	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(validModelProfilesValues())); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// Name-only update keeps the values.
	newName := "Only Renamed"
	if err := f.UpdateModelProfile(active.ID, ModelProfileUpdateRequest{Name: &newName}); err != nil {
		t.Fatalf("name-only update: %v", err)
	}
	stored := modelProfilesStoredProfile(t, f, active.ID)
	if stored.Name != newName || !stored.Config.EssentialTools.Enabled {
		t.Errorf("name-only update must keep values: name=%q essential=%v", stored.Name, stored.Config.EssentialTools.Enabled)
	}
	// Values-only update keeps the name.
	other := validModelProfilesValues()
	other.Sampling.Temperature = 0.3
	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(other)); err != nil {
		t.Fatalf("values-only update: %v", err)
	}
	stored = modelProfilesStoredProfile(t, f, active.ID)
	if stored.Name != newName || stored.Config.Sampling.Temperature != 0.3 {
		t.Errorf("values-only update must keep the name: name=%q temp=%v", stored.Name, stored.Config.Sampling.Temperature)
	}
	// Empty request is a no-op.
	if err := f.UpdateModelProfile(active.ID, ModelProfileUpdateRequest{}); err != nil {
		t.Fatalf("empty request must be a no-op, got: %v", err)
	}
}

// TestUpdateModelProfile_InvalidValuesRejectedWithoutWrite: every invalid value
// is rejected before any mutation — the store keeps its previous content and
// no router rebuild happens.
func TestUpdateModelProfile_InvalidValuesRejectedWithoutWrite(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ModelProfileValues)
	}{
		{"negative loop threshold", func(v *ModelProfileValues) { v.LoopHardening.FruitlessAbortThreshold = -5 }},
		{"zero loop threshold while enabled", func(v *ModelProfileValues) { v.LoopHardening.RepeatNudgeThreshold = 0 }},
		{"keep_last below 2", func(v *ModelProfileValues) { v.Context.Compaction.KeepLast = 1 }},
		{"trigger_percent 100", func(v *ModelProfileValues) { v.Context.Compaction.TriggerPercent = 100 }},
		{"trigger_percent zero", func(v *ModelProfileValues) { v.Context.Compaction.TriggerPercent = 0 }},
		{"block_size below 2", func(v *ModelProfileValues) { v.Context.Compaction.BlockSize = 1 }},
		{"tool_output_keep_last_n zero", func(v *ModelProfileValues) { v.Context.ToolOutputKeepLastN = 0 }},
		{"output_token_reserve zero", func(v *ModelProfileValues) { v.Context.OutputTokenReserve = 0 }},
		{"negative temperature", func(v *ModelProfileValues) { v.Sampling.Temperature = -0.5 }},
		{"top_p above 1", func(v *ModelProfileValues) { v.Sampling.TopP = 1.5 }},
		{"top_k negative", func(v *ModelProfileValues) { v.Sampling.TopK = -3 }},
		{"repetition_penalty below 1", func(v *ModelProfileValues) { v.Sampling.RepetitionPenalty = 0.5 }},
		{"presence_penalty above 2", func(v *ModelProfileValues) { v.Sampling.PresencePenalty = 2.5 }},
		{"invalid reasoning effort", func(v *ModelProfileValues) { v.Sampling.ReasoningEffort = "ultra" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, mock, _ := newTestAPI(t)
			active := activateCustomModelProfile(t, f)

			values := validModelProfilesValues()
			tc.mutate(&values)

			if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(values)); err == nil {
				t.Fatal("expected validation error")
			}
			// Rejected without a write: the store keeps the zero-valued
			// profile created by the helper.
			stored := modelProfilesStoredProfile(t, f, active.ID)
			if stored.Config.EssentialTools.Enabled {
				t.Error("rejected update leaked into the profile store")
			}
			if mock.rebuildRouterCalls != 0 {
				t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
			}
		})
	}
}

func TestUpdateModelProfile_ZeroSentinelsAndDisabledVariants(t *testing.T) {
	f, _, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	// Zero sampling numerics mean "inherit the vendor preset" — valid.
	values := validModelProfilesValues()
	values.Sampling.Temperature = 0
	values.Sampling.TopP = 0
	values.Sampling.TopK = 0
	values.Sampling.RepetitionPenalty = 0
	values.Sampling.PresencePenalty = 0
	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(values)); err != nil {
		t.Fatalf("zero sampling sentinels must be accepted, got: %v", err)
	}
	// All variants disabled: zero values are acceptable (variant logic inert).
	empty := ModelProfileValues{}
	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(empty)); err != nil {
		t.Fatalf("all-variants-off payload must be accepted, got: %v", err)
	}
	// Empty always_present is valid: protected/MCP tools are kept implicitly.
	noPins := ModelProfileValues{EssentialTools: ModelProfilesEssentialToolsValues{Enabled: true}}
	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(noPins)); err != nil {
		t.Fatalf("empty always_present must be accepted, got: %v", err)
	}
}

// TestUpdateModelProfile_StoreWriteFailureLeavesStateUntouched forces the store
// write to fail and verifies nothing changed. The store persists with an
// atomic temp-file-then-rename (see config.SaveCustomModelProfiles), so
// occupying that temp sibling with a directory makes os.WriteFile fail on
// every platform. Making the agent dir read-only via os.Chmod cannot: on
// Windows the read-only attribute does not block creating files inside a
// directory, so the write would silently succeed.
func TestUpdateModelProfile_StoreWriteFailureLeavesStateUntouched(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)
	before := modelProfilesStoredProfile(t, f, active.ID)

	// Block the atomic write: a directory at the temp path makes the
	// temp-file creation fail before it can be renamed into place.
	tmpPath := config.ModelProfilesPath(f.agentDir) + ".tmp"
	if err := os.Mkdir(tmpPath, 0o755); err != nil {
		t.Fatalf("cannot occupy the store temp path %q: %v", tmpPath, err)
	}

	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(validModelProfilesValues())); err == nil {
		t.Fatal("expected error when the store write fails")
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0 (nothing was applied)", mock.rebuildRouterCalls)
	}
	after := modelProfilesStoredProfile(t, f, active.ID)
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("failed store write changed the persisted profile (-before +after):\n%s", diff)
	}
}

// --- DeleteModelProfile ---

func TestDeleteModelProfile_NonActiveCustom(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)
	victim, err := f.CreateModelProfile("generic", "Doomed Copy")
	if err != nil {
		t.Fatalf("CreateModelProfile: %v", err)
	}

	if err := f.DeleteModelProfile(victim); err != nil {
		t.Fatalf("DeleteModelProfile: %v", err)
	}
	profiles, _ := config.LoadCustomModelProfiles(config.ModelProfilesPath(f.agentDir))
	for _, p := range profiles {
		if p.ID == victim {
			t.Fatal("deleted profile still in the store")
		}
	}
	if f.config.ModelProfiles.ActiveProfile != active.ID {
		t.Errorf("active profile changed to %q; deleting a non-active profile must not touch it", f.config.ModelProfiles.ActiveProfile)
	}
	if mock.rebuildRouterCalls != 2 { // 1 create + 1 delete
		t.Errorf("RebuildRouter called %d times, want 2", mock.rebuildRouterCalls)
	}
}

func TestDeleteModelProfile_PredefinedRejected(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	for _, p := range config.PredefinedModelProfiles() {
		if err := f.DeleteModelProfile(p.ID); err == nil {
			t.Fatalf("expected error deleting the predefined profile %q", p.ID)
		}
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

func TestDeleteModelProfile_UnknownIDRejected(t *testing.T) {
	f, _, _ := newTestAPI(t)
	if err := f.DeleteModelProfile("ghost"); err == nil {
		t.Fatal("expected error for an unknown profile id")
	}
}

// TestDeleteModelProfile_ActiveCustomFallsBackToGeneric is the acceptance
// scenario: deleting the ACTIVE custom profile switches the active id to
// generic in memory AND in config.yaml, and the NEXT GetModelProfiles reports
// the generic active id plus a one-shot warning explaining the switch (the
// second Get no longer carries the notice).
func TestDeleteModelProfile_ActiveCustomFallsBackToGeneric(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)
	// Make the store hold exactly one active custom profile.
	f.config.ModelProfiles.ActiveProfile = ""
	active := activateCustomModelProfile(t, f)

	if err := f.DeleteModelProfile(active.ID); err != nil {
		t.Fatalf("DeleteModelProfile(active): %v", err)
	}

	// In-memory and persisted active id is generic.
	if f.config.ModelProfiles.ActiveProfile != config.ModelProfilesGenericProfileID {
		t.Errorf("in-memory active = %q, want generic", f.config.ModelProfiles.ActiveProfile)
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if persisted.ModelProfiles.ActiveProfile != config.ModelProfilesGenericProfileID {
		t.Errorf("persisted active = %q, want generic", persisted.ModelProfiles.ActiveProfile)
	}
	// The profile is gone from the store.
	profiles, _ := config.LoadCustomModelProfiles(config.ModelProfilesPath(f.agentDir))
	for _, p := range profiles {
		if p.ID == active.ID {
			t.Fatal("deleted profile still in the store")
		}
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}

	// The next Get reports generic as active plus the one-shot warning.
	got := f.GetModelProfiles()
	if got.ActiveID != config.ModelProfilesGenericProfileID {
		t.Errorf("GetModelProfiles active = %q, want generic", got.ActiveID)
	}
	found := false
	for _, w := range got.Warnings {
		if strings.Contains(w, "was deleted") {
			found = true
		}
	}
	if !found {
		t.Errorf("next Get must carry the delete notice, got warnings: %v", got.Warnings)
	}
	// The notice is one-shot.
	again := f.GetModelProfiles()
	for _, w := range again.Warnings {
		if strings.Contains(w, "was deleted") {
			t.Errorf("the delete notice must not repeat, got warnings: %v", again.Warnings)
		}
	}
}

// TestDeleteModelProfile_ActiveConfigPersistFailureRollsBack: when config.yaml
// cannot be written, the delete aborts — the profile stays in the store and
// the active id keeps pointing at it.
func TestDeleteModelProfile_ActiveConfigPersistFailureRollsBack(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.ModelProfiles.ActiveProfile = ""
	active := activateCustomModelProfile(t, f)

	f.configPath = "" // force the config.yaml persist to fail

	if err := f.DeleteModelProfile(active.ID); err == nil {
		t.Fatal("expected error when the config persist fails")
	}
	if f.config.ModelProfiles.ActiveProfile != active.ID {
		t.Errorf("active id = %q, want %q (rolled back)", f.config.ModelProfiles.ActiveProfile, active.ID)
	}
	modelProfilesStoredProfile(t, f, active.ID) // still in the store
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

// --- SelectModelProfile ---

func TestSelectModelProfile_PersistsActiveID(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	if err := f.SelectModelProfile("qwen3.8-27b"); err != nil {
		t.Fatalf("SelectModelProfile: %v", err)
	}
	if f.config.ModelProfiles.ActiveProfile != "qwen3.8-27b" {
		t.Errorf("in-memory active = %q, want qwen3.8-27b", f.config.ModelProfiles.ActiveProfile)
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if persisted.ModelProfiles.ActiveProfile != "qwen3.8-27b" {
		t.Errorf("persisted active = %q, want qwen3.8-27b", persisted.ModelProfiles.ActiveProfile)
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}

	// Selecting a custom profile works the same way.
	if err := f.SelectModelProfile(active.ID); err != nil {
		t.Fatalf("SelectModelProfile(custom): %v", err)
	}
	if f.config.ModelProfiles.ActiveProfile != active.ID {
		t.Errorf("active = %q, want %q", f.config.ModelProfiles.ActiveProfile, active.ID)
	}
}

func TestSelectModelProfile_AlreadyActiveNoop(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	if err := f.SelectModelProfile(active.ID); err != nil {
		t.Fatalf("SelectModelProfile: %v", err)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0 (no-op)", mock.rebuildRouterCalls)
	}
}

func TestSelectModelProfile_UnknownAndEmptyRejected(t *testing.T) {
	f, _, _ := newTestAPI(t)

	for _, id := range []string{"ghost", ""} {
		if err := f.SelectModelProfile(id); err == nil {
			t.Errorf("expected error for profile id %q", id)
		}
	}
	if f.config.ModelProfiles.ActiveProfile == "ghost" {
		t.Error("rejected selection must not change the active id")
	}
}

// TestSelectModelProfile_PersistFailureRollsBack: a failed config.yaml write
// restores the previous active id so the rejected selection is
// indistinguishable from a rejected request.
func TestSelectModelProfile_PersistFailureRollsBack(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	prev := f.config.ModelProfiles.ActiveProfile

	f.configPath = ""
	if err := f.SelectModelProfile("qwen3.8-27b"); err == nil {
		t.Fatal("expected error when the config persist fails")
	}
	if f.config.ModelProfiles.ActiveProfile != prev {
		t.Errorf("active id = %q, want %q (rolled back)", f.config.ModelProfiles.ActiveProfile, prev)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

// --- SetModelProfilesEnabled ---

// TestSetModelProfilesEnabled_PersistsAndApplies verifies the master toggle is written to
// config.yaml, flips the in-memory value, and runs the shared ModelProfiles
// post-mutation tail (router rebuild) so the change takes effect for new
// sessions without a restart — in both directions.
func TestSetModelProfilesEnabled_PersistsAndApplies(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

	if err := f.SetModelProfilesEnabled(true); err != nil {
		t.Fatalf("SetModelProfilesEnabled(true): %v", err)
	}
	if !f.config.ModelProfiles.Enabled {
		t.Error("in-memory ModelProfiles.Enabled = false, want true")
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !persisted.ModelProfiles.Enabled {
		t.Error("persisted ModelProfiles.Enabled = false, want true")
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}

	// Disabling persists false and rebuilds again (allowed regardless of gate).
	if err := f.SetModelProfilesEnabled(false); err != nil {
		t.Fatalf("SetModelProfilesEnabled(false): %v", err)
	}
	if f.config.ModelProfiles.Enabled {
		t.Error("in-memory ModelProfiles.Enabled = true, want false")
	}
	persisted, err = config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if persisted.ModelProfiles.Enabled {
		t.Error("persisted ModelProfiles.Enabled = true, want false")
	}
	if mock.rebuildRouterCalls != 2 {
		t.Errorf("RebuildRouter called %d times, want 2", mock.rebuildRouterCalls)
	}
}

// TestSetModelProfilesEnabled_EnableWithoutExperimental verifies enabling succeeds with
// the experimental gate off: the master toggle is the only switch, so the enable
// flips the in-memory value, persists it, and runs the shared Model Profiles
// post-mutation tail (router rebuild).
func TestSetModelProfilesEnabled_EnableWithoutExperimental(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t) // experimental defaults to false

	if err := f.SetModelProfilesEnabled(true); err != nil {
		t.Fatalf("SetModelProfilesEnabled(true) with experimental off: %v", err)
	}
	if !f.config.ModelProfiles.Enabled {
		t.Error("in-memory ModelProfiles.Enabled = false, want true")
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !persisted.ModelProfiles.Enabled {
		t.Error("persisted ModelProfiles.Enabled = false, want true")
	}
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("RebuildRouter called %d times, want 1", mock.rebuildRouterCalls)
	}
}

// TestSetModelProfilesEnabled_NoopWhenUnchanged verifies a request matching the stored
// value is a true no-op: no persist, no router rebuild.
func TestSetModelProfilesEnabled_NoopWhenUnchanged(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.ModelProfiles.Enabled = true

	if err := f.SetModelProfilesEnabled(true); err != nil {
		t.Fatalf("SetModelProfilesEnabled(true): %v", err)
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0 (no-op)", mock.rebuildRouterCalls)
	}
}

// TestSetModelProfilesEnabled_PersistFailureRollsBack verifies a failed config.yaml write
// restores the previous value so the rejected toggle is indistinguishable from
// a rejected request.
func TestSetModelProfilesEnabled_PersistFailureRollsBack(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)
	f.configPath = filepath.Join(filepath.Dir(cfgPath), "missing", "config.yaml")

	if err := f.SetModelProfilesEnabled(true); err == nil {
		t.Fatal("expected an error when the config persist fails")
	}
	if f.config.ModelProfiles.Enabled {
		t.Error("ModelProfiles.Enabled = true, want false (rolled back)")
	}
	if mock.rebuildRouterCalls != 0 {
		t.Errorf("RebuildRouter called %d times, want 0", mock.rebuildRouterCalls)
	}
}

// TestSetModelProfilesEnabled_EmptyPathRejected verifies the config-path guard precedes
// any mutation.
func TestSetModelProfilesEnabled_EmptyPathRejected(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.configPath = ""

	if err := f.SetModelProfilesEnabled(true); err == nil {
		t.Fatal("expected an error when the config path is not set")
	}
	if f.config.ModelProfiles.Enabled {
		t.Error("ModelProfiles.Enabled must not change when the path is missing")
	}
}

// TestSetModelProfilesEnabled_EmitsConfigUpdated verifies the (asynchronous)
// config:updated announcement so frontend consumers re-read the config without
// an app restart.
func TestSetModelProfilesEnabled_EmitsConfigUpdated(t *testing.T) {
	f, _, rec, db := newUpdateLLMConfigProjectHarness(t)
	defer func() { _ = db.Close() }()

	if err := f.SetModelProfilesEnabled(true); err != nil {
		t.Fatalf("SetModelProfilesEnabled(true): %v", err)
	}
	rec.waitFor(t, EventConfigUpdated)
}

// TestModelProfilesNilConfigRejected verifies the nil-config guard on every mutation.
func TestModelProfilesNilConfigRejected(t *testing.T) {
	f := &FrontendAPI{}
	name := "X"
	if _, err := f.CreateModelProfile("generic", "X"); err == nil {
		t.Error("CreateModelProfile must fail on nil config")
	}
	if err := f.UpdateModelProfile("any", ModelProfileUpdateRequest{Name: &name}); err == nil {
		t.Error("UpdateModelProfile must fail on nil config")
	}
	if err := f.DeleteModelProfile("any"); err == nil {
		t.Error("DeleteModelProfile must fail on nil config")
	}
	if err := f.SelectModelProfile("any"); err == nil {
		t.Error("SelectModelProfile must fail on nil config")
	}
	if err := f.SetModelProfilesEnabled(true); err == nil {
		t.Error("SetModelProfilesEnabled must fail on nil config")
	}
}

// TestModelProfileValues_RoundTrip_FullProfileLossless is the round-trip
// integration test: a fully-populated values payload written via
// UpdateModelProfile and read back via GetModelProfiles must survive losslessly.
// This exercises the converter pair (modelProfileConfigToValues /
// modelProfilesValuesToProfileConfig) end-to-end through the public API surface and the
// store persist path, covering EVERY field — including the ones the
// happy-path test omits (FewShot, ReasoningScaffold, ReasoningEffort, and all
// five loop-hardening thresholds) — so a future converter change that drops a
// field is caught.
func TestModelProfileValues_RoundTrip_FullProfileLossless(t *testing.T) {
	f, _, _ := newTestAPI(t)
	active := activateCustomModelProfile(t, f)

	want := ModelProfileValues{
		EssentialTools: ModelProfilesEssentialToolsValues{
			Enabled:       true,
			AlwaysPresent: []string{"read_file", "edit_file", "bash_exec", "semantic_search"},
		},
		SystemPrompt: ModelProfilesSystemPromptResp{
			Lite:              true,
			FewShot:           true,
			ReasoningScaffold: true,
		},
		Sampling: ModelProfilesSamplingResp{
			Enabled:           true,
			Temperature:       0.15,
			TopP:              0.85,
			TopK:              40,
			RepetitionPenalty: 1.15,
			PresencePenalty:   1.5,
			ReasoningEffort:   "low",
		},
		LoopHardening: ModelProfilesLoopHardeningResp{
			Enabled:                      true,
			RepeatNudgeThreshold:         2,
			ParseErrorAbortThreshold:     3,
			FruitlessNudgeThreshold:      4,
			FruitlessAbortThreshold:      6,
			SameToolRepeatNudgeThreshold: 5,
		},
		Context: ModelProfilesContextResp{
			Enabled: true,
			Compaction: ModelProfilesCompactionResp{
				KeepLast:       6,
				BlockSize:      5,
				TriggerPercent: 80,
			},
			ToolOutputKeepLastN: 2,
			OutputTokenReserve:  8192,
		},
	}

	if err := f.UpdateModelProfile(active.ID, modelProfilesConfigReq(want)); err != nil {
		t.Fatalf("UpdateModelProfile failed: %v", err)
	}

	got := modelProfilesFindProfile(t, f.GetModelProfiles(), active.ID).Values

	if !reflect.DeepEqual(got.EssentialTools.Enabled, want.EssentialTools.Enabled) ||
		!slices.Equal(got.EssentialTools.AlwaysPresent, want.EssentialTools.AlwaysPresent) ||
		got.EssentialTools.CompactDescriptions != want.EssentialTools.CompactDescriptions {
		t.Errorf("EssentialTools round-trip mismatch:\n got %+v\nwant %+v", got.EssentialTools, want.EssentialTools)
	}
	if got.EssentialTools.AlwaysPresent == nil {
		t.Error("AlwaysPresent is nil, want non-nil (normalized to [])")
	}
	if got.SystemPrompt != want.SystemPrompt {
		t.Errorf("SystemPrompt round-trip mismatch:\n got %+v\nwant %+v", got.SystemPrompt, want.SystemPrompt)
	}
	if got.Sampling != want.Sampling {
		t.Errorf("Sampling round-trip mismatch:\n got %+v\nwant %+v", got.Sampling, want.Sampling)
	}
	if got.LoopHardening != want.LoopHardening {
		t.Errorf("LoopHardening round-trip mismatch:\n got %+v\nwant %+v", got.LoopHardening, want.LoopHardening)
	}
	if got.Context != want.Context {
		t.Errorf("Context round-trip mismatch:\n got %+v\nwant %+v", got.Context, want.Context)
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

	models, err := f.ListProviderModels(ListProviderModelsRequest{Provider: "anthropic"})
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

// TestListProviderModels_DraftCompatibleProvider verifies that an unsaved
// OpenAI-compatible provider (not yet in config.yaml) can still fetch models
// when the settings UI supplies draft base_url / api_key / type. Without this
// merge, Fetch Models fails with "unknown provider" during first-run setup
// where saves are blocked until a default_model is chosen.
func TestListProviderModels_DraftCompatibleProvider(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	mock.listProviderModelsRes = []string{"gpt-custom"}

	models, err := f.ListProviderModels(ListProviderModelsRequest{
		Provider: "custom",
		APIKey:   "sk-draft",
		BaseURL:  "https://api-llm.example.com/v1",
		Type:     "openai",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0] != "gpt-custom" {
		t.Errorf("models = %v, want [gpt-custom]", models)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.listProviderModelsLastProvider != "custom" {
		t.Errorf("provider = %q, want custom", mock.listProviderModelsLastProvider)
	}
	pc, ok := mock.listProviderModelsLastCfg.LLM.ProviderConfigs["custom"]
	if !ok {
		t.Fatal("expected custom to be injected into BuilderConfig")
	}
	if pc.ProviderType != "openai" {
		t.Errorf("ProviderType = %q, want openai", pc.ProviderType)
	}
	if pc.APIKey != "sk-draft" {
		t.Errorf("APIKey = %q, want sk-draft", pc.APIKey)
	}
	if pc.BaseURL != "https://api-llm.example.com/v1" {
		t.Errorf("BaseURL = %q, want https://api-llm.example.com/v1", pc.BaseURL)
	}
}

// TestListProviderModels_MaskedKeyFallsBackToSaved verifies that a masked
// sentinel from the UI does not wipe a saved API key when re-fetching models.
func TestListProviderModels_MaskedKeyFallsBackToSaved(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{
		"lmstudio": {
			APIKey:  "sk-saved",
			BaseURL: "http://localhost:1234/v1",
			Models:  []string{"local"},
		},
	}
	mock.listProviderModelsRes = []string{"local"}

	_, err := f.ListProviderModels(ListProviderModelsRequest{
		Provider: "lmstudio",
		APIKey:   maskedAPIKey,
		BaseURL:  "http://localhost:1234/v1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	pc := mock.listProviderModelsLastCfg.LLM.ProviderConfigs["lmstudio"]
	if pc.APIKey != "sk-saved" {
		t.Errorf("APIKey = %q, want sk-saved (masked sentinel must fall back)", pc.APIKey)
	}
}

func TestListProviderModels_UnknownWithoutBaseURL(t *testing.T) {
	f, _, _ := newTestAPI(t)
	_, err := f.ListProviderModels(ListProviderModelsRequest{Provider: "ghost"})
	if err == nil {
		t.Fatal("expected error for unknown provider without base URL")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error = %q, want mention of unknown provider", err)
	}
}

func TestListProviderModels_EmptyProvider(t *testing.T) {
	f, _, _ := newTestAPI(t)
	_, err := f.ListProviderModels(ListProviderModelsRequest{})
	if err == nil {
		t.Fatal("expected error for empty provider")
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

// TestUpdateExperimentalFeatures_DoesNotClearModelProfilesEnabled verifies that
// toggling the experimental gate leaves the Model Profiles master toggle
// untouched in both directions: Model Profiles is not gated by the
// experimental-features switch, so disabling the gate keeps the stored value.
func TestUpdateExperimentalFeatures_DoesNotClearModelProfilesEnabled(t *testing.T) {
	f, _, cfgPath := newTestAPI(t)

	// Start from the "both on" state.
	f.config.Experimental.Enabled = true
	f.config.ModelProfiles.Enabled = true

	if err := f.UpdateExperimentalFeatures(false); err != nil {
		t.Fatalf("UpdateExperimentalFeatures(false): %v", err)
	}
	if !f.config.ModelProfiles.Enabled {
		t.Error("in-memory ModelProfiles.Enabled = false, want it untouched (true) after disabling experimental features")
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !persisted.ModelProfiles.Enabled {
		t.Error("persisted ModelProfiles.Enabled = false, want it untouched (true)")
	}

	// Re-enabling the gate must not disturb it either.
	if err := f.UpdateExperimentalFeatures(true); err != nil {
		t.Fatalf("UpdateExperimentalFeatures(true): %v", err)
	}
	if !f.config.ModelProfiles.Enabled {
		t.Error("in-memory ModelProfiles.Enabled = false after re-enabling experimental features, want true")
	}
	persisted, err = config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !persisted.ModelProfiles.Enabled {
		t.Error("persisted ModelProfiles.Enabled = false after re-enabling experimental features, want true")
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
// (SelectModelProfile, SetModelConfig). The rebuild must re-snapshot the
// config and hold configMu.RLock across snapshot + rebuild, so a concurrent
// writer's mutate+rebuild can never interleave between them and leave the
// router on a snapshot that predates its changes.
//
// The hook parks the FIRST RebuildRouter call (UpdateLLMConfig's) and records
// the essential-tools variant of each rebuild snapshot in application
// (completion) order: the active profile starts as a zero-valued custom one
// (variant off) and the concurrent SelectModelProfile(generic) switches to the
// generic profile (variant on). With the fix the order is [false (LLM save),
// true (profile switch)] — the switch's rebuild lands last and wins; without
// it the stale LLM-save rebuild completes after it and the router is left on
// [.., false].
func TestUpdateLLMConfig_RebuildNotRevertedByConcurrentConfigWriter(t *testing.T) {
	f, mock, cfgPath := newTestAPI(t)

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
		applied = append(applied, cfg.ModelProfiles.EssentialTools.Enabled)
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
		bDone <- f.SelectModelProfile(config.ModelProfilesGenericProfileID)
	}()
	select {
	case err := <-bDone:
		close(releaseFirst)
		t.Fatalf("SelectModelProfile completed while UpdateLLMConfig was inside its rebuild phase — the rebuild snapshot can be stale (err=%v)", err)
	case <-time.After(100 * time.Millisecond):
		// Still blocked: expected under the fix.
	}

	close(releaseFirst)
	if err := <-aDone; err != nil {
		t.Fatalf("unexpected error from UpdateLLMConfig: %v", err)
	}
	if err := <-bDone; err != nil {
		t.Fatalf("unexpected error from SelectModelProfile: %v", err)
	}

	orderMu.Lock()
	got := append([]bool(nil), applied...)
	orderMu.Unlock()
	if len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("router rebuild application order = %v, want [false true]: the router was left on a snapshot predating the concurrent model-profile update", got)
	}

	// Sanity: the profile switch survived in memory and on disk.
	f.configMu.RLock()
	inMemory := f.config.ModelProfiles.ActiveProfile
	f.configMu.RUnlock()
	if inMemory != config.ModelProfilesGenericProfileID {
		t.Fatalf("in-memory active profile = %q, want generic", inMemory)
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load persisted config: %v", err)
	}
	if persisted.ModelProfiles.ActiveProfile != config.ModelProfilesGenericProfileID {
		t.Fatal("persisted config lost the selected model-profile profile")
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

// --- Notification banner timeout ---

func TestNotificationBannerTimeout_RoundTrip(t *testing.T) {
	f, _, _ := newTestAPI(t)

	// The two sentinels plus an explicit lifetime must all persist and read
	// back unchanged — especially 0, which is a real value ("never expire"),
	// not an unset field.
	for _, seconds := range []int{
		config.NotificationBannerTimeoutDaemonDefault,
		config.NotificationBannerTimeoutNever,
		30,
		config.NotificationBannerTimeoutMaxSeconds,
	} {
		if err := f.SetNotificationBannerTimeout(seconds); err != nil {
			t.Fatalf("SetNotificationBannerTimeout(%d): %v", seconds, err)
		}
		if got := f.GetNotificationBannerTimeout(); got != seconds {
			t.Errorf("GetNotificationBannerTimeout() = %d, want %d", got, seconds)
		}
	}
}

func TestNotificationBannerTimeout_RejectsOutOfRange(t *testing.T) {
	f, _, _ := newTestAPI(t)
	for _, seconds := range []int{-2, -100, config.NotificationBannerTimeoutMaxSeconds + 1} {
		if err := f.SetNotificationBannerTimeout(seconds); err == nil {
			t.Errorf("SetNotificationBannerTimeout(%d) accepted an out-of-range value", seconds)
		}
	}
}

// TestNotificationBannerTimeout_UnloadedConfigIsSafe pins the fail-safe: with
// no config the getter must answer -1 (the daemon default), never the Go zero
// value 0 — which would silently mean "banners never go away".
func TestNotificationBannerTimeout_UnloadedConfigIsSafe(t *testing.T) {
	f := &FrontendAPI{}
	if got := f.GetNotificationBannerTimeout(); got != config.NotificationBannerTimeoutDaemonDefault {
		t.Errorf("GetNotificationBannerTimeout() on an unloaded config = %d, want %d",
			got, config.NotificationBannerTimeoutDaemonDefault)
	}
	if err := f.SetNotificationBannerTimeout(30); err == nil {
		t.Error("SetNotificationBannerTimeout succeeded with no config loaded")
	}
}
