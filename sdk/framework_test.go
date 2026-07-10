package sp4rk

import (
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// registryNames returns the set of registered tool names.
func registryNames(t *testing.T, fw *Framework) map[string]bool {
	t.Helper()
	names := make(map[string]bool)
	for _, td := range fw.ToolRegistry().List() {
		names[td.Name] = true
	}
	return names
}

// dummyProvider returns a minimal valid provider entry for tests. New does
// not validate API keys at construction time (only at call time), so a dummy
// key is sufficient to build a Framework.
func dummyProvider() llm.ProviderEntry {
	return Anthropic("test-key", "claude-sonnet-4-5")
}

// testFramework builds a minimal Framework (dummy provider + auto-finish) and
// schedules Shutdown cleanup. It is the shared fixture for tests that need a
// ready Framework but don't exercise the builder surface itself.
func testFramework(t *testing.T) *Framework {
	t.Helper()
	fw, err := NewF().Provider(dummyProvider()).Build()
	if err != nil {
		t.Fatalf("build test framework: %v", err)
	}
	t.Cleanup(func() { _ = fw.Shutdown() })
	return fw
}

func TestNewMinimalFramework(t *testing.T) {
	fw := testFramework(t)

	// Finish tool is auto-registered by default.
	names := registryNames(t, fw)
	if !names["finish"] {
		t.Error("expected finish tool to be auto-registered")
	}
}

func TestNoAutoFinish(t *testing.T) {
	fw, err := NewF().Provider(dummyProvider()).NoAutoFinish().Build()
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = fw.Shutdown() })

	names := registryNames(t, fw)
	if names["finish"] {
		t.Error("expected finish tool to be absent after NoAutoFinish")
	}
}

func TestBuilderToolsRegisters(t *testing.T) {
	fw, err := NewF().
		Provider(dummyProvider()).
		Tools(FileTools()...).
		Build()
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = fw.Shutdown() })

	names := registryNames(t, fw)
	for _, want := range []string{"read_file", "write_file", "edit_file", "finish"} {
		if !names[want] {
			t.Errorf("expected tool %q to be registered", want)
		}
	}
}

func TestNewNoProviderError(t *testing.T) {
	_, err := NewF().Build()
	if err == nil {
		t.Fatal("expected error when no provider is configured")
	}
}

func TestConfigEscapeHatch(t *testing.T) {
	// A classic base config with a provider + an MCP server.
	base := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{OpenAI("base-key", "gpt-4o")},
		},
	}
	// Layer builder methods on top: add tools + finish.
	fw, err := NewF().
		Config(base).
		MemoryTools().
		Build()
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = fw.Shutdown() })

	names := registryNames(t, fw)
	for _, want := range []string{"store_fact", "search_facts", "finish"} {
		if !names[want] {
			t.Errorf("expected tool %q registered via escape-hatch + auto-finish", want)
		}
	}
}

func TestConfigAndProviderCombine(t *testing.T) {
	base := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{OpenAI("base-key", "gpt-4o")},
		},
	}
	fw, err := NewF().
		Config(base).
		Provider(Anthropic("added-key", "claude-sonnet-4-5")).
		Build()
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = fw.Shutdown() })

	// Both providers should be present; switching to the added model must succeed.
	if err := fw.LLMRouter().SetModel(t.Context(), "claude-sonnet-4-5"); err != nil {
		t.Errorf("SetModel(claude-sonnet-4-5): %v", err)
	}
	if err := fw.LLMRouter().SetModel(t.Context(), "gpt-4o"); err != nil {
		t.Errorf("SetModel(gpt-4o): %v", err)
	}
}

// TestNewReturnsOriginalType confirms the builder returns the real sp4rk type.
// The return-type contract is enforced by the Build signature (which returns
// *Framework directly); this test exercises the construction path.
func TestNewReturnsOriginalType(t *testing.T) {
	fw := testFramework(t)

	if fw.ToolRegistry() == nil {
		t.Error("expected non-nil tool registry")
	}
}

// TestMergeConfigMCPNilServersNoPanic — a base config that sets MCP but leaves
// Servers nil is a valid partial configuration. Merging MCP servers on top must
// not panic on a nil-map write. Regression test for the shallow-copy bug where
// build() wrote into the shared (nil) base map.
func TestMergeConfigMCPNilServersNoPanic(t *testing.T) {
	o := options{
		baseCfg:    &Config{MCP: &MCPConfig{DefaultWorkDir: "/tmp"}},
		mcpServers: map[string]mcp.ServerEntry{"added": {Transport: "stdio"}},
	}
	// Must not panic on the nil-map write.
	cfg := mergeConfig(o)
	if cfg.MCP == nil || len(cfg.MCP.Servers) != 1 {
		t.Fatalf("expected 1 merged server, got MCP=%v", cfg.MCP)
	}
	if cfg.MCP.DefaultWorkDir != "/tmp" {
		t.Errorf("DefaultWorkDir = %q, want /tmp (base value should be preserved)", cfg.MCP.DefaultWorkDir)
	}
}

// TestMergeConfigDoesNotMutateBase — the base config must be safely reusable
// across multiple merges. Without the fresh-allocation fix, the builder's MCP
// servers leak into (mutate) the caller's base map and the provider slice
// aliases the caller's backing array. Regression test for the shallow-copy
// aliasing bug in mergeConfig's provider/MCP merge.
func TestMergeConfigDoesNotMutateBase(t *testing.T) {
	base := &Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{OpenAI("base-key", "gpt-4o")},
		},
		MCP: &MCPConfig{
			Servers: map[string]mcp.ServerEntry{"existing": {Transport: "stdio"}},
		},
	}
	o := options{
		baseCfg:    base,
		providers:  []llm.ProviderEntry{Anthropic("added-key", "claude-sonnet-4-5")},
		mcpServers: map[string]mcp.ServerEntry{"added": {Transport: "stdio"}},
	}

	_ = mergeConfig(o)
	_ = mergeConfig(o) // the base must be reusable across merges

	// Base provider slice untouched.
	if len(base.LLM.Providers) != 1 {
		t.Errorf("base providers mutated: got %d, want 1 (slice aliasing leak)", len(base.LLM.Providers))
	}
	// Base MCP map untouched — no accumulation across merges.
	if got := len(base.MCP.Servers); got != 1 {
		t.Errorf("base MCP servers mutated: got %d, want 1 (map aliasing leak)", got)
	}
	if _, ok := base.MCP.Servers["added"]; ok {
		t.Error("base MCP map was mutated: 'added' server leaked into caller's config")
	}
}
