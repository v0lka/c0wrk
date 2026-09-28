package core

import (
	"log/slog"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// This file pins the mechanics of the runtime model-metadata push:
// UpdateModelOverrides must reach every LIVE per-session model registry and
// none beyond it. The motivating caller is the embedded LLM context read-back
// (backend), which corrects the llm.models window after a load and pushes the
// correction to sessions whose routers were built with the stale estimate.

// newModelOverridesTestBuilder mirrors the lightweight builder the embedded
// gate tests construct — enough of the struct for buildRouter and the push.
func newModelOverridesTestBuilder(t *testing.T) *OrchestratorBuilder {
	t.Helper()
	return &OrchestratorBuilder{
		logger:  slog.New(slog.DiscardHandler),
		goAsync: func(fn func()) { fn() },
	}
}

// overridesCfg builds the minimal config the push derives its overrides from:
// a llm.models context-window map, exactly the shape the embedded install and
// read-back write.
func overridesCfg(windows map[string]int) *BuilderConfig {
	models := make(map[string]BuilderModelOverride, len(windows))
	for name, window := range windows {
		models[name] = BuilderModelOverride{ContextWindow: window}
	}
	return &BuilderConfig{
		LLM: BuilderLLMConfig{
			DefaultModel: "embedded/" + embeddedModelName,
			Models:       models,
		},
		ExpandEnvVars: identityExpand,
	}
}

func windowOf(t *testing.T, reg *llm.ModelRegistry, model string) (int, bool) {
	t.Helper()
	meta, ok := reg.ResolveLocal(model)
	if !ok {
		return 0, false
	}
	return meta.ContextWindow, true
}

// TestUpdateModelOverridesReachesLiveSessionRegistries: a registry registered
// by a session receives the config-derived override; a model the new cfg no
// longer mentions keeps its stored entry (upsert, not replace); an empty
// override map is a no-op.
func TestUpdateModelOverridesReachesLiveSessionRegistries(t *testing.T) {
	b := newModelOverridesTestBuilder(t)

	cfgA := overridesCfg(map[string]int{
		embeddedModelName: 65536,
		"other-model":     12345,
	})
	cfgB := overridesCfg(map[string]int{
		// other-model is deliberately absent: the push must not clobber it.
		embeddedModelName: 262144,
	})

	reg := llm.NewModelRegistry(modelOverridesFromConfig(cfgA))
	b.registerSessionModelRegistry(reg)

	if window, ok := windowOf(t, reg, embeddedModelName); !ok || window != 65536 {
		t.Fatalf("before push: window = %d, ok = %v, want the seeded 65536", window, ok)
	}

	b.UpdateModelOverrides(cfgB)

	if window, ok := windowOf(t, reg, embeddedModelName); !ok || window != 262144 {
		t.Errorf("after push: window = %d, ok = %v, want the corrected 262144", window, ok)
	}
	if window, ok := windowOf(t, reg, "other-model"); !ok || window != 12345 {
		t.Errorf("model absent from the pushed cfg: window = %d, ok = %v, want the untouched 12345", window, ok)
	}

	b.UpdateModelOverrides(&BuilderConfig{ExpandEnvVars: identityExpand})

	if window, ok := windowOf(t, reg, embeddedModelName); !ok || window != 262144 {
		t.Errorf("empty-cfg push: window = %d, ok = %v, want the previous 262144", window, ok)
	}
}

// TestUnregisterSessionModelRegistryStopsPushes: the cleanup hook must remove
// the registry from the live set so a dead session never receives a push and
// the builder does not accumulate registries forever.
func TestUnregisterSessionModelRegistryStopsPushes(t *testing.T) {
	b := newModelOverridesTestBuilder(t)

	reg := llm.NewModelRegistry(modelOverridesFromConfig(
		overridesCfg(map[string]int{embeddedModelName: 65536})))
	b.registerSessionModelRegistry(reg)

	b.mu.RLock()
	live := len(b.sessionModelRegistries)
	b.mu.RUnlock()
	if live != 1 {
		t.Fatalf("live set = %d, want 1 after registration", live)
	}

	b.unregisterSessionModelRegistry(reg)

	b.mu.RLock()
	live = len(b.sessionModelRegistries)
	b.mu.RUnlock()
	if live != 0 {
		t.Fatalf("live set = %d, want 0 after unregistration", live)
	}

	// The unregistered registry no longer moves: the corrected window must
	// NOT appear on it after a push it is no longer part of.
	b.UpdateModelOverrides(overridesCfg(map[string]int{embeddedModelName: 262144}))
	if window, ok := windowOf(t, reg, embeddedModelName); !ok || window != 65536 {
		t.Errorf("unregistered registry moved: window = %d, ok = %v, want the original 65536", window, ok)
	}
}

// TestBuildRouterSeedsOverridesFromSharedHelper: buildRouter and the push
// derive their overrides through the same helper, so a session built AFTER a
// change carries the same metadata a push would have delivered. This asserts
// the constructor path sees the corrected window without any push.
func TestBuildRouterSeedsOverridesFromSharedHelper(t *testing.T) {
	b := newModelOverridesTestBuilder(t)

	cfg := embeddedRouterCfg("http://127.0.0.1:1") // provider shape; nothing dials it
	cfg.LLM.Models = map[string]BuilderModelOverride{
		embeddedModelName: {ContextWindow: 262144},
	}

	_, reg, err := b.buildRouter(t.Context(), cfg, nil)
	if err != nil {
		t.Fatalf("buildRouter: %v", err)
	}
	if window, ok := windowOf(t, reg, embeddedModelName); !ok || window != 262144 {
		t.Errorf("seeded window = %d, ok = %v, want 262144", window, ok)
	}
}
