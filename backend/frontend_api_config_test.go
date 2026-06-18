package backend

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/skills"
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

	// Configurable return values for methods that have them.
	rebuildRouterErr      error
	rebuildProxyErr       error
	reconfigureMCPErr     error
	listProviderModelsRes []string
	listProviderModelsErr error
	optimizePromptRes     *core.OptimizePromptResult
	optimizePromptErr     error
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
func (m *mockBuilder) GetBaseSkillDirs() []string {
	m.mu.Lock()
	m.getBaseSkillDirsCalls++
	m.mu.Unlock()
	return nil
}
func (m *mockBuilder) GetSkillDescriptors(string) []skills.SkillDescriptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return nil
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

func TestUpdateLLMConfig_PerProviderFields(t *testing.T) {
	tests := []struct {
		provider string
		req      LLMFullConfigRequest
	}{
		{"gemini", LLMFullConfigRequest{Gemini: &ProviderConfigRequest{Models: []string{"test-model"}}}},
		{"lmstudio", LLMFullConfigRequest{LMStudio: &ProviderConfigRequest{Models: []string{"test-model"}}}},
		{"openai_compatible", LLMFullConfigRequest{OpenAICompatible: &ProviderConfigRequest{Models: []string{"test-model"}}}},
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
