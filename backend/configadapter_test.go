package backend

import (
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

// TestAgentsMDSearchPaths verifies that agentsMDSearchPaths resolves the global
// and c0wrk-specific AGENTS.md paths relative to the user home directory in the
// documented priority order (global first, c0wrk second).
func TestAgentsMDSearchPaths(t *testing.T) {
	// os.UserHomeDir reads $HOME on Unix and %USERPROFILE% on Windows
	// (not %HOME%); override both to a temp dir for a deterministic result
	// on every platform Go supports.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	got := agentsMDSearchPaths()
	want := []string{
		filepath.Join(tmpHome, ".agents", "AGENTS.md"),
		filepath.Join(tmpHome, ".c0wrk", ".agents", "AGENTS.md"),
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d paths, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("path[%d]: got %q, want %q", i, got[i], w)
		}
	}
}

// TestAgentsMDSearchPaths_NoHomeDir verifies that a failure to resolve the home
// directory yields nil (no search paths) rather than panicking.
func TestAgentsMDSearchPaths_NoHomeDir(t *testing.T) {
	// Unset HOME and USERPROFILE so os.UserHomeDir returns an error.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	got := agentsMDSearchPaths()
	if got != nil {
		t.Errorf("expected nil when home dir is unavailable, got %v", got)
	}
}

// TestToBuilderConfig_SmallLLMSystemPrompt verifies the config→builder mapping
// for the prompt-simplification variant: cfg.SmallLLM.SystemPrompt.{Lite,
// FewShot, ReasoningScaffold} all flow into BuilderSmallLLMSystemPromptConfig
// so a config change takes effect on rebuild.
func TestToBuilderConfig_SmallLLMSystemPrompt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Experimental.Enabled = true
	cfg.SmallLLM.Enabled = true
	cfg.SmallLLM.SystemPrompt.Lite = true
	cfg.SmallLLM.SystemPrompt.FewShot = true
	cfg.SmallLLM.SystemPrompt.ReasoningScaffold = true

	bc := ToBuilderConfig(cfg)

	if !bc.SmallLLM.Enabled {
		t.Error("SmallLLM.Enabled not mapped")
	}
	// Lite is the SystemPrompt variant master toggle (there is no separate
	// Enabled field in config/builder — see SystemPromptConfig). It must map
	// straight through so a config change takes effect on rebuild.
	if !bc.SmallLLM.SystemPrompt.Lite {
		t.Error("SystemPrompt.Lite not mapped")
	}
	if !bc.SmallLLM.SystemPrompt.FewShot {
		t.Error("SystemPrompt.FewShot not mapped")
	}
	if !bc.SmallLLM.SystemPrompt.ReasoningScaffold {
		t.Error("SystemPrompt.ReasoningScaffold not mapped")
	}

	// Master off — even with Lite true, Enabled carries the master gate only.
	cfg.SmallLLM.Enabled = false
	cfg.SmallLLM.SystemPrompt.Lite = true
	bc = ToBuilderConfig(cfg)
	if bc.SmallLLM.Enabled {
		t.Error("master Enabled should be false")
	}
	// The variant sub-toggle still reflects the config value (master gating is
	// applied at runtime, not stripped at the mapping layer).
	if !bc.SmallLLM.SystemPrompt.Lite {
		t.Error("SystemPrompt.Lite should still map the config value")
	}
}

// TestToBuilderConfig_SmallLLMContext verifies the config→builder mapping for
// the context-management variant: cfg.SmallLLM.Context.{Enabled, Compaction,
// ToolOutputKeepLastN, OutputTokenReserve} all flow into
// BuilderSmallLLMContext so a config change takes effect on rebuild.
func TestToBuilderConfig_SmallLLMContext(t *testing.T) {
	cfg := &config.Config{}
	cfg.Experimental.Enabled = true
	cfg.SmallLLM.Enabled = true
	cfg.SmallLLM.Context.Enabled = true
	cfg.SmallLLM.Context.Compaction.KeepLast = 6
	cfg.SmallLLM.Context.Compaction.BlockSize = 5
	cfg.SmallLLM.Context.Compaction.TriggerPercent = 80
	cfg.SmallLLM.Context.ToolOutputKeepLastN = 2
	cfg.SmallLLM.Context.OutputTokenReserve = 8192

	bc := ToBuilderConfig(cfg)

	if !bc.SmallLLM.Context.Enabled {
		t.Error("Context.Enabled not mapped")
	}
	if bc.SmallLLM.Context.Compaction.KeepLast != 6 {
		t.Errorf("Context.Compaction.KeepLast = %d, want 6", bc.SmallLLM.Context.Compaction.KeepLast)
	}
	if bc.SmallLLM.Context.Compaction.BlockSize != 5 {
		t.Errorf("Context.Compaction.BlockSize = %d, want 5", bc.SmallLLM.Context.Compaction.BlockSize)
	}
	if bc.SmallLLM.Context.Compaction.TriggerPercent != 80 {
		t.Errorf("Context.Compaction.TriggerPercent = %d, want 80", bc.SmallLLM.Context.Compaction.TriggerPercent)
	}
	if bc.SmallLLM.Context.ToolOutputKeepLastN != 2 {
		t.Errorf("Context.ToolOutputKeepLastN = %d, want 2", bc.SmallLLM.Context.ToolOutputKeepLastN)
	}
	if bc.SmallLLM.Context.OutputTokenReserve != 8192 {
		t.Errorf("Context.OutputTokenReserve = %d, want 8192", bc.SmallLLM.Context.OutputTokenReserve)
	}

	// Master off — Enabled carries the master gate only; the variant values
	// still map through (gating is applied at runtime by
	// applyContextManagement, not stripped at the mapping layer).
	cfg.SmallLLM.Enabled = false
	bc = ToBuilderConfig(cfg)
	if bc.SmallLLM.Enabled {
		t.Error("master Enabled should be false")
	}
	if !bc.SmallLLM.Context.Enabled {
		t.Error("Context.Enabled should still map the config value")
	}
}

// TestToBuilderConfig_ExperimentalGatesSmallLLM verifies the experimental
// master switch forces the Small-LLM profile off regardless of the stored
// SmallLLM.Enabled value, and restores it when experimental features are on.
func TestToBuilderConfig_ExperimentalGatesSmallLLM(t *testing.T) {
	cfg := &config.Config{}
	cfg.SmallLLM.Enabled = true

	// Experimental off (zero value) → the effective Small-LLM master is off.
	if bc := ToBuilderConfig(cfg); bc.SmallLLM.Enabled {
		t.Error("experimental off should force SmallLLM.Enabled false")
	}

	// Experimental on → the stored Small-LLM master flows through.
	cfg.Experimental.Enabled = true
	if bc := ToBuilderConfig(cfg); !bc.SmallLLM.Enabled {
		t.Error("experimental on should preserve SmallLLM.Enabled true")
	}
}

// TestToBuilderConfig_ProviderOutputTokenReserve verifies the per-provider
// output_token_reserve plumbing (D4): provider-level values flow into
// BuilderProviderConfig.
func TestToBuilderConfig_ProviderOutputTokenReserve(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Anthropic.OutputTokenReserve = 12288
	cfg.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{
		"lmstudio": {
			BaseURL:            "http://localhost:1234/v1",
			Models:             []string{"qwen/qwen3-coder-30b"},
			OutputTokenReserve: 8192,
		},
	}

	bc := ToBuilderConfig(cfg)

	if got := bc.LLM.ProviderConfigs["anthropic"].OutputTokenReserve; got != 12288 {
		t.Errorf("anthropic OutputTokenReserve = %d, want 12288", got)
	}
	if got := bc.LLM.ProviderConfigs["lmstudio"].OutputTokenReserve; got != 8192 {
		t.Errorf("lmstudio OutputTokenReserve = %d, want 8192", got)
	}
	// Providers without an explicit reserve must map zero (inherit), not
	// accidentally inherit some other provider's value.
	cfg.LLM.OpenAICompatible["other"] = config.OpenAICompatibleConfig{Models: []string{"m"}}
	bc = ToBuilderConfig(cfg)
	if got := bc.LLM.ProviderConfigs["other"].OutputTokenReserve; got != 0 {
		t.Errorf("other OutputTokenReserve = %d, want 0 (inherit)", got)
	}
}
