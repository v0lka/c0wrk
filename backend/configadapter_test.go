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
