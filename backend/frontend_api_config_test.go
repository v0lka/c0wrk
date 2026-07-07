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
	cfg.LLM.DefaultModel = "claude-3-opus"

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
