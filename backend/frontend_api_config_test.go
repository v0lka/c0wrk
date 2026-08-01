package backend

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/skills"
	_ "modernc.org/sqlite"
)

// mockBuilder records calls to appBuilder methods for test assertions.
type mockBuilder struct {
	mu                      sync.Mutex
	rebuildJudgeCalls       int
	rebuildRouterCalls      int
	rebuildProxyCalls       int
	updateSearchToolCalls   int
	updateSecPolicyCalls    int
	reconfigureMCPCalls     int
	listProviderModelsCalls int
	setMCPWorkDirCalls      int
	optimizePromptCalls     int
	getBaseSkillDirsCalls   int
	generateCommitMsgCalls  int
	getBaseAgentDirsCalls   int

	// Configurable return values for methods that have them.
	rebuildRouterErr      error
	rebuildProxyErr       error
	reconfigureMCPErr     error
	listProviderModelsRes []string
	listProviderModelsErr error
	optimizePromptRes     *core.OptimizePromptResult
	optimizePromptErr     error
	generateCommitMsgRes  string
	generateCommitMsgErr  error
	generateCommitMsgDiff string

	getSkillDescriptorsCalls int
	getSkillDescriptorsRes   []skills.SkillDescriptor

	getAgentDescriptorsCalls int
	getAgentDescriptorsRes   []agents.AgentDescriptor
}

func (m *mockBuilder) RebuildJudge(_ *core.BuilderConfig) {
	m.mu.Lock()
	m.rebuildJudgeCalls++
	m.mu.Unlock()
}
func (m *mockBuilder) RebuildRouter(_ *core.BuilderConfig) error {
	m.mu.Lock()
	m.rebuildRouterCalls++
	m.mu.Unlock()
	return m.rebuildRouterErr
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
	return nil
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

// TestUpdateLLMConfig_ClearsDanglingDefaultOnProviderRemoval verifies that
// deleting the provider that owned the default model (without naming a new
// default in the same request) clears the now-invalid default_model rather
// than persisting a dangling selector. The settings dialog then blocks close
// until the user picks a new default.
func TestUpdateLLMConfig_ClearsDanglingDefaultOnProviderRemoval(t *testing.T) {
	f, _, _ := newTestAPI(t)

	// Seed an OpenAI-compatible provider that owns the default model, using a
	// composite selector so ResolveDefaultModelProvider can pin the provider.
	f.config.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{
		"lmstudio": {BaseURL: "http://localhost:1234/v1", Models: []string{"gpt-4"}},
	}
	f.config.LLM.DefaultModel = "lmstudio/gpt-4"

	// Remove the provider without setting a new default. An empty
	// default_model is normally skipped, but the re-validation step must
	// clear the now-unresolvable default.
	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		OpenAICompatible: map[string]ProviderConfigRequest{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if f.config.LLM.DefaultModel != "" {
		t.Errorf("default_model = %q, want \"\" after owning provider removed", f.config.LLM.DefaultModel)
	}
}

// TestUpdateLLMConfig_ClearsDanglingDefaultOnModelDisabled verifies that
// disabling the single model backing the default (in a fixed provider) clears
// the dangling default_model.
func TestUpdateLLMConfig_ClearsDanglingDefaultOnModelDisabled(t *testing.T) {
	f, _, _ := newTestAPI(t)
	// Default harness: default_model "claude-3-opus" owned by anthropic.

	err := f.UpdateLLMConfig(LLMFullConfigRequest{
		Anthropic: &ProviderConfigRequest{Models: []string{"claude-3-sonnet"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if f.config.LLM.DefaultModel != "" {
		t.Errorf("default_model = %q, want \"\" after backing model disabled", f.config.LLM.DefaultModel)
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
	// Unknown model → fallback defaults (128000/4096), both effective and default.
	if resp.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want fallback 128000", resp.ContextWindow)
	}
	if resp.OutputLimit != 4096 {
		t.Errorf("OutputLimit = %d, want fallback 4096", resp.OutputLimit)
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
		OutputLimit:   50000,   // != default → stored
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
		ContextWindow: 200000, // != default → stored
		OutputLimit:   16384,  // == default → omitted
		TokenizerType: "tiktoken/o200k_base", // == default → omitted
		Family:        "openai_flagship",     // == default → omitted
		Protocol:      "chat_completions",    // == default → omitted
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

// --- UpdateSecuritySettings ---

func TestUpdateSecuritySettings_AppliesPolicies(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		DefaultPolicy: "always_allow",
		ToolPolicies: map[string]ToolPolicyResponse{
			"bash_exec": {Policy: "user_confirm"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.updateSecPolicyCalls != 1 {
		t.Errorf("UpdateSecurityPolicies called %d times, want 1", mock.updateSecPolicyCalls)
	}
	if f.config.Security.DefaultPolicy != "always_allow" {
		t.Errorf("DefaultPolicy = %q, want always_allow", f.config.Security.DefaultPolicy)
	}
}

func TestUpdateSecuritySettings_FiltersInternal(t *testing.T) {
	f, _, _ := newTestAPI(t)
	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		DefaultPolicy: "user_confirm",
		ToolPolicies: map[string]ToolPolicyResponse{
			"finish":    {Policy: "always_allow"}, // internal
			"bash_exec": {Policy: "user_confirm"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := f.config.Security.ToolPolicies["finish"]; ok {
		t.Error("internal tool 'finish' should have been filtered out")
	}
	if _, ok := f.config.Security.ToolPolicies["bash_exec"]; !ok {
		t.Error("bash_exec should be preserved")
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

// --- GetProxySettings ---

func TestGetProxySettings_NilBypassList(t *testing.T) {
	f, _, _ := newTestAPI(t)
	resp := f.GetProxySettings()
	if resp.BypassList == nil {
		t.Fatal("BypassList must not be nil (JSON would serialize to null)")
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
	projectManager := project.NewManager(projStore, agentDir)
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
