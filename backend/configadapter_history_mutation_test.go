package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

// The history-mutation cohort quantum (executor.historyMutation.cohortSize)
// must reach the BuilderConfig: the ContextWindow's SetHistoryMutation call
// replaces the whole struct, so a dropped field here would silently revert
// the eviction/pruning cohort behavior to sp4rk's zero-value legacy advance.
func TestToBuilderConfig_HistoryMutationCohortSize(t *testing.T) {
	newCfg := func() *config.Config {
		cfg := &config.Config{}
		config.ApplyDefaults(cfg)
		cfg.LLM.DefaultModel = "selfhosted/qwen3"
		cfg.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{
			"selfhosted": {
				BaseURL: "http://127.0.0.1:1234/v1",
				APIKey:  "k",
				Models:  []string{"qwen3"},
			},
		}
		return cfg
	}

	// The ApplyDefaults default (5) must flow through the adapter.
	bc := ToBuilderConfig(newCfg(), config.PredefinedModelProfiles())
	if got := bc.Executor.HistoryMutation.CohortSize; got != 5 {
		t.Errorf("default cohortSize mapped = %d, want 5", got)
	}

	// An explicit legacy opt-out must map through verbatim.
	cfg := newCfg()
	cfg.Executor.HistoryMutation.CohortSize = 1
	bc = ToBuilderConfig(cfg, config.PredefinedModelProfiles())
	if got := bc.Executor.HistoryMutation.CohortSize; got != 1 {
		t.Errorf("explicit legacy cohortSize mapped = %d, want 1", got)
	}

	// An explicit cohort size must map through verbatim.
	cfg = newCfg()
	cfg.Executor.HistoryMutation.CohortSize = 12
	bc = ToBuilderConfig(cfg, config.PredefinedModelProfiles())
	if got := bc.Executor.HistoryMutation.CohortSize; got != 12 {
		t.Errorf("explicit cohortSize mapped = %d, want 12", got)
	}

	// The sibling knobs ride the same struct — pin them alongside so the
	// mapping cannot regress field-by-field.
	cfg = newCfg()
	cfg.Executor.HistoryMutation.ToolResultEvictionStep = 7
	cfg.Executor.HistoryMutation.EvictStepStatus = true
	cfg.Executor.HistoryMutation.DedupRepeatedReads = true
	bc = ToBuilderConfig(cfg, config.PredefinedModelProfiles())
	hm := bc.Executor.HistoryMutation
	if hm.ToolResultEvictionStep != 7 || !hm.EvictStepStatus || !hm.DedupRepeatedReads {
		t.Errorf("sibling history-mutation knobs mapped wrong: %+v", hm)
	}
}
