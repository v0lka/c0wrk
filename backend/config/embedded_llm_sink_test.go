package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// This file pins the contract core/embeddedllm.ConfigSink requires from the
// config layer. core cannot import backend/config (this package sits above core
// and already imports core packages, so an import back would cycle), so the
// interface is declared on core's side and the implementation is assembled
// here. The production implementation lives in backend/frontend_api_embedded.go
// and additionally persists the config and rebuilds the router; the state
// mutation it performs is exactly embeddedConfigSink's.

// embeddedConfigSink is the reference ConfigSink implementation.
type embeddedConfigSink struct{ cfg *Config }

var _ embeddedllm.ConfigSink = embeddedConfigSink{}

// ApplyInstalled writes embedded_llm.* from the install record, establishes the
// auto-unload defaults WITHOUT overwriting an explicit operator choice (both
// knobs are pointers, so nil is distinguishable from "explicitly false"), and
// regenerates the backend-owned provider entry plus the context-window override
// from the authoritative state. The tuning section is operator-owned too, so it
// is carried through verbatim: provisioning the model never resets a tuned
// memory plan.
func (s embeddedConfigSink) ApplyInstalled(_ context.Context, state embeddedllm.InstallState) error {
	autoUnload := s.cfg.EmbeddedLLM.AutoUnload
	tuning := s.cfg.EmbeddedLLM.Tuning
	s.cfg.EmbeddedLLM = EmbeddedLLMConfig{
		Installed:      true,
		Packing:        string(state.Packing),
		Backend:        string(state.Backend),
		Port:           state.Port,
		ModelFile:      state.ModelFile,
		RuntimeVersion: state.RuntimeVersion,
		InstalledAt:    state.InstalledAt,
		AutoUnload:     autoUnload,
		Tuning:         tuning,
	}
	if s.cfg.EmbeddedLLM.AutoUnload.Enabled == nil {
		enabled := state.AutoUnloadEnabled
		s.cfg.EmbeddedLLM.AutoUnload.Enabled = &enabled
	}
	if s.cfg.EmbeddedLLM.AutoUnload.Minutes == nil {
		minutes := state.AutoUnloadMinutes
		s.cfg.EmbeddedLLM.AutoUnload.Minutes = &minutes
	}
	// The only caller that passes a non-zero context tier: it is the only one
	// that has resolved it.
	s.cfg.SyncEmbeddedLLMProvider(state.ContextSize)
	return nil
}

// ApplyRemoved clears the install state, migrates llm.default_model off the
// embedded composite (otherwise the next load fails validation and the next
// settings save is rejected as dangling), and drops the provider record. The
// auto-unload and tuning knobs are operator settings, not install state, so
// both survive — a reinstall starts from the same memory plan the operator
// chose, not from a reset one.
func (s embeddedConfigSink) ApplyRemoved(_ context.Context) error {
	composite := EmbeddedLLMProviderName + "/" + EmbeddedLLMModelName
	// Migrate the default model BEFORE the record disappears: an empty
	// llm.default_model fails validate(), so the composite must move to another
	// enabled model rather than simply be cleared.
	migrated := s.cfg.LLM.DefaultModel
	if migrated == composite {
		migrated = firstNonEmbeddedModelID(s.cfg, composite)
	}
	autoUnload := s.cfg.EmbeddedLLM.AutoUnload
	tuning := s.cfg.EmbeddedLLM.Tuning
	s.cfg.EmbeddedLLM = EmbeddedLLMConfig{AutoUnload: autoUnload, Tuning: tuning}
	s.cfg.LLM.DefaultModel = migrated
	s.cfg.SyncEmbeddedLLMProvider(0)
	return nil
}

// firstNonEmbeddedModelID picks the migration target for llm.default_model when
// the embedded model is removed. When it was the only enabled model there is
// nothing to move to and the result is empty: ApplyDefaults fills the first
// available model on the next load, and a config with no provider at all is
// already invalid independently of this subsystem.
func firstNonEmbeddedModelID(cfg *Config, composite string) string {
	for _, id := range cfg.LLM.AllModelIDs() {
		if id != composite {
			return id
		}
	}
	return ""
}

func testInstallState() embeddedllm.InstallState {
	return embeddedllm.InstallState{
		Packing:           embeddedllm.PackingPQ2_0,
		Backend:           embeddedllm.BackendMetal,
		Port:              4321,
		ModelFile:         "/home/u/.c0wrk/models/bonsai-2-27b/Ternary-Bonsai-2-27B-PQ2_0.gguf",
		RuntimeVersion:    embeddedllm.RuntimeTag,
		InstalledAt:       "2026-09-23T10:15:00Z",
		ContextSize:       32768,
		AutoUnloadEnabled: embeddedllm.DefaultAutoUnloadEnabled,
		AutoUnloadMinutes: embeddedllm.DefaultAutoUnloadMinutes,
	}
}

// TestEmbeddedConfigSinkAppliesInstallState is the install half of the
// contract: everything the subsystem reports must land in embedded_llm.*, the
// provider record must be regenerated from it, and the resolved context tier
// must become the llm.models override.
func TestEmbeddedConfigSinkAppliesInstallState(t *testing.T) {
	cfg := minimalValidConfig()
	state := testInstallState()

	if err := (embeddedConfigSink{cfg: cfg}).ApplyInstalled(context.Background(), state); err != nil {
		t.Fatalf("ApplyInstalled: %v", err)
	}

	got := cfg.EmbeddedLLM
	if !got.Installed {
		t.Error("embedded_llm.installed = false after an install")
	}
	if got.Packing != string(state.Packing) || got.Backend != string(state.Backend) {
		t.Errorf("packing/backend = %q/%q, want %q/%q", got.Packing, got.Backend,
			state.Packing, state.Backend)
	}
	if got.Port != state.Port || got.ModelFile != state.ModelFile ||
		got.RuntimeVersion != state.RuntimeVersion || got.InstalledAt != state.InstalledAt {
		t.Errorf("embedded_llm = %+v, does not carry the install record %+v", got, state)
	}
	if got.AutoUnload.Enabled == nil || !*got.AutoUnload.Enabled {
		t.Errorf("auto_unload.enabled = %v, want the default true", got.AutoUnload.Enabled)
	}
	if got.AutoUnload.Minutes == nil || *got.AutoUnload.Minutes != EmbeddedLLMDefaultAutoUnloadMinutes {
		t.Errorf("auto_unload.minutes = %v, want the default %d", got.AutoUnload.Minutes,
			EmbeddedLLMDefaultAutoUnloadMinutes)
	}

	entry, ok := cfg.LLM.OpenAICompatible[EmbeddedLLMProviderName]
	if !ok {
		t.Fatalf("the %q provider record was not generated: %+v",
			EmbeddedLLMProviderName, cfg.LLM.OpenAICompatible)
	}
	if want := "http://127.0.0.1:4321/v1"; entry.BaseURL != want {
		t.Errorf("base_url = %q, want %q", entry.BaseURL, want)
	}
	if len(entry.Models) != 1 || entry.Models[0] != EmbeddedLLMModelName {
		t.Errorf("models = %v, want [%s]", entry.Models, EmbeddedLLMModelName)
	}
	override, ok := cfg.LLM.Models[EmbeddedLLMModelName]
	if !ok {
		t.Fatalf("no llm.models override for %q: %+v", EmbeddedLLMModelName, cfg.LLM.Models)
	}
	if override.ContextWindow != state.ContextSize {
		t.Errorf("context_window = %d, want the resolved tier %d", override.ContextWindow, state.ContextSize)
	}

	// The composite id is usable as the default model, and the result validates.
	cfg.LLM.DefaultModel = EmbeddedLLMProviderName + "/" + EmbeddedLLMModelName
	if err := validate(cfg); err != nil {
		t.Errorf("validate() after an install: %v", err)
	}
}

// TestEmbeddedConfigSinkPreservesOperatorAutoUnload proves an install does not
// reset a tuned idle budget: the defaults fill unset knobs only.
func TestEmbeddedConfigSinkPreservesOperatorAutoUnload(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.EmbeddedLLM.AutoUnload = AutoUnloadConfig{
		Enabled: embeddedBoolPtr(false),
		Minutes: embeddedIntPtr(15),
	}

	if err := (embeddedConfigSink{cfg: cfg}).ApplyInstalled(context.Background(), testInstallState()); err != nil {
		t.Fatalf("ApplyInstalled: %v", err)
	}
	got := cfg.EmbeddedLLM.AutoUnload
	if got.Enabled == nil || *got.Enabled {
		t.Errorf("auto_unload.enabled = %v, want the operator's explicit false", got.Enabled)
	}
	if got.Minutes == nil || *got.Minutes != 15 {
		t.Errorf("auto_unload.minutes = %v, want the operator's 15", got.Minutes)
	}
}

// TestEmbeddedLLMRemoveClearsProviderRecordEndToEnd drives the REAL
// embeddedllm.Installer.Remove across the package boundary: the trees come off
// the disk, the openai_compatible.embedded record is erased, the default model
// is migrated off the composite, and the flat embedding-model files sharing
// <agentDir>/models survive. It is the executable form of the removal half of
// the contract, with both roots built by this package's path API.
func TestEmbeddedLLMRemoveClearsProviderRecordEndToEnd(t *testing.T) {
	agentDir := t.TempDir()
	layout, err := embeddedllm.NewLayout(RuntimesDir(agentDir), EmbeddedModelDir(agentDir))
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}

	// An installed state on disk: a runtime tree, the weights and a manifest.
	runtimeDir, err := layout.RuntimeDir(embeddedllm.BackendMetal)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(runtimeDir, "build", "bin"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	modelFile, err := layout.ModelFile(embeddedllm.PackingPQ2_0)
	if err != nil {
		t.Fatalf("ModelFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(modelFile), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, path := range []string{
		filepath.Join(runtimeDir, "build", "bin", embeddedllm.ServerBinaryName),
		modelFile,
	} {
		if err := os.WriteFile(path, []byte("installed bytes"), 0o600); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
	manifestPath, err := layout.ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"packing":"PQ2_0"}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	// A neighbour that must survive: the flat embedding-model file resolved by
	// desktop/startup.go resolveModelPath shares <agentDir>/models.
	embeddingModel := filepath.Join(ModelsDir(agentDir), "ggml-model-q4_0.gguf")
	if err := os.WriteFile(embeddingModel, []byte("embedding model"), 0o600); err != nil {
		t.Fatalf("write embedding model: %v", err)
	}

	cfg := minimalValidConfig()
	cfg.EmbeddedLLM = installedEmbeddedState()
	cfg.SyncEmbeddedLLMProvider(0)
	composite := EmbeddedLLMProviderName + "/" + EmbeddedLLMModelName
	cfg.LLM.DefaultModel = composite
	if _, ok := cfg.LLM.OpenAICompatible[EmbeddedLLMProviderName]; !ok {
		t.Fatalf("test setup: the %q record was not generated", EmbeddedLLMProviderName)
	}

	installer := embeddedllm.NewInstaller(layout, newDiscardLogger())
	installer.Sink = embeddedConfigSink{cfg: cfg}
	if err := installer.Remove(context.Background()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Both trees are gone.
	if _, err := os.Stat(layout.ModelRoot); !os.IsNotExist(err) {
		t.Errorf("EmbeddedModelDir %q still exists (stat err = %v)", layout.ModelRoot, err)
	}
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Errorf("the runtime tree %q still exists (stat err = %v)", runtimeDir, err)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("manifest %q still exists (stat err = %v)", manifestPath, err)
	}
	if _, err := os.Stat(embeddingModel); err != nil {
		t.Errorf("the flat embedding model was deleted: %v", err)
	}

	// The provider record is erased and the composite no longer resolves.
	if entry, ok := cfg.LLM.OpenAICompatible[EmbeddedLLMProviderName]; ok {
		t.Errorf("openai_compatible.%s survived Remove: %+v", EmbeddedLLMProviderName, entry)
	}
	if _, _, err := cfg.LLM.ResolveModelID(composite); err == nil {
		t.Errorf("ResolveModelID(%q) still succeeds after Remove", composite)
	}
	// The default model was migrated off the embedded composite, so the config
	// still validates instead of failing on the next load.
	if cfg.LLM.DefaultModel == composite {
		t.Errorf("llm.default_model is still %q; Remove must migrate it off the embedded composite", composite)
	}
	if cfg.LLM.DefaultModel == "" {
		t.Error("llm.default_model was cleared instead of migrated to another enabled model")
	}
	if _, _, err := cfg.LLM.ResolveModelID(cfg.LLM.DefaultModel); err != nil {
		t.Errorf("the migrated llm.default_model %q does not resolve: %v", cfg.LLM.DefaultModel, err)
	}
	if err := validate(cfg); err != nil {
		t.Errorf("validate() after Remove: %v", err)
	}
	// The install state is cleared, the operator's auto-unload knobs survive.
	if cfg.EmbeddedLLM.Installed || cfg.EmbeddedLLM.Port != 0 || cfg.EmbeddedLLM.ModelFile != "" ||
		cfg.EmbeddedLLM.RuntimeVersion != "" || cfg.EmbeddedLLM.InstalledAt != "" {
		t.Errorf("embedded_llm = %+v, want the cleared state", cfg.EmbeddedLLM)
	}
	if cfg.EmbeddedLLM.AutoUnload.Enabled == nil || *cfg.EmbeddedLLM.AutoUnload.Enabled {
		t.Errorf("auto_unload.enabled = %v, want the operator's explicit false preserved",
			cfg.EmbeddedLLM.AutoUnload.Enabled)
	}
	if cfg.EmbeddedLLM.AutoUnload.Minutes == nil || *cfg.EmbeddedLLM.AutoUnload.Minutes != 15 {
		t.Errorf("auto_unload.minutes = %v, want the operator's 15 preserved",
			cfg.EmbeddedLLM.AutoUnload.Minutes)
	}

	// Removing again is a no-op that still clears the config.
	if err := installer.Remove(context.Background()); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

// TestEmbeddedLLMRemoveWithNoOtherModelLeavesAnEmptyDefault documents the one
// removal case with no migration target: the embedded model was the only
// enabled model. The record is still erased and llm.default_model ends up
// empty, which validate() reports — a config with no provider at all is invalid
// independently of this subsystem, and ApplyDefaults fills the first available
// model on the next load once the operator configures one.
func TestEmbeddedLLMRemoveWithNoOtherModelLeavesAnEmptyDefault(t *testing.T) {
	agentDir := t.TempDir()
	layout, err := embeddedllm.NewLayout(RuntimesDir(agentDir), EmbeddedModelDir(agentDir))
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}

	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.EmbeddedLLM = installedEmbeddedState()
	cfg.SyncEmbeddedLLMProvider(0)
	composite := EmbeddedLLMProviderName + "/" + EmbeddedLLMModelName
	cfg.LLM.DefaultModel = composite

	installer := embeddedllm.NewInstaller(layout, newDiscardLogger())
	installer.Sink = embeddedConfigSink{cfg: cfg}
	if err := installer.Remove(context.Background()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, ok := cfg.LLM.OpenAICompatible[EmbeddedLLMProviderName]; ok {
		t.Errorf("openai_compatible.%s survived Remove", EmbeddedLLMProviderName)
	}
	if cfg.LLM.DefaultModel != "" {
		t.Errorf("llm.default_model = %q, want empty when no other model is enabled",
			cfg.LLM.DefaultModel)
	}
	if err := validate(cfg); err == nil {
		t.Error("validate() accepted a config with no enabled model at all")
	}
}

// operatorTuning is a memory plan the operator actually chose: every knob set
// to something other than the planner's own default, so a sink that drops or
// re-seeds the section cannot pass by accident.
func operatorTuning() TuningConfig {
	return TuningConfig{
		Context: EmbeddedLLMContextConfig{
			Mode:   embeddedStrPtr(EmbeddedLLMContextExact),
			Tokens: embeddedIntPtr(49152),
		},
		KVCacheType: embeddedStrPtr("q4_0"),
		Offload: EmbeddedLLMOffloadConfig{
			Mode:   embeddedStrPtr(EmbeddedLLMOffloadLayers),
			Layers: embeddedIntPtr(40),
		},
		Fit:            embeddedBoolPtr(false),
		FitTargetMiB:   embeddedIntPtr(2048),
		FitMinContext:  embeddedIntPtr(32768),
		KVOffload:      embeddedBoolPtr(false),
		MMProjOffload:  embeddedBoolPtr(false),
		Packing:        embeddedStrPtr("PTQ1_0"),
		Parallel:       embeddedIntPtr(2),
		CacheRAMMiB:    embeddedIntPtr(0),
		HostReserveGiB: embeddedFloatPtr(6.5),
	}
}

// TestEmbeddedConfigSinkPreservesOperatorTuning is the install half of the
// tuning contract: ApplyInstalled rewrites embedded_llm.* wholesale, so it must
// carry the operator's memory plan through verbatim — provisioning the model
// does not reset a tuned plan, exactly as it does not reset a tuned idle budget.
// The install record must still land in full beside it.
func TestEmbeddedConfigSinkPreservesOperatorTuning(t *testing.T) {
	cfg := minimalValidConfig()
	want := operatorTuning()
	cfg.EmbeddedLLM.Tuning = want

	state := testInstallState()
	if err := (embeddedConfigSink{cfg: cfg}).ApplyInstalled(context.Background(), state); err != nil {
		t.Fatalf("ApplyInstalled: %v", err)
	}

	got := cfg.EmbeddedLLM.Tuning
	if !reflect.DeepEqual(got, want) {
		t.Errorf("an install reset the operator's memory plan:\n got %+v\nwant %+v", got, want)
	}
	// The section is carried as a VALUE. It shares its pointers with the value
	// the test kept, which is safe under this codebase's discipline — every
	// writer replaces the whole sub-struct with fresh pointers (see
	// SetEmbeddedLLMAutoUnload) rather than mutating a pointee in place — and
	// the boundary where a value genuinely LEAVES the config, ToTuning, clones
	// (pinned by TestEmbeddedLLMTuningToTuningClonesPointers).
	// …and the install record itself still landed.
	record := cfg.EmbeddedLLM
	if !record.Installed || record.Port != state.Port ||
		record.Packing != string(state.Packing) || record.Backend != string(state.Backend) {
		t.Errorf("embedded_llm = %+v, does not carry the install record %+v", record, state)
	}
	if err := validate(cfg); err != nil {
		t.Errorf("validate() after an install that preserved tuning: %v", err)
	}
}

// TestEmbeddedConfigSinkRemovePreservesOperatorTuning is the removal half: a
// Remove clears the RECORD and keeps the SETTINGS, so a reinstall starts from
// the memory plan the operator chose rather than from a reset one.
func TestEmbeddedConfigSinkRemovePreservesOperatorTuning(t *testing.T) {
	cfg := minimalValidConfig()
	want := operatorTuning()
	cfg.EmbeddedLLM = installedEmbeddedState()
	cfg.EmbeddedLLM.Tuning = want
	cfg.LLM.DefaultModel = EmbeddedLLMProviderName + "/" + EmbeddedLLMModelName

	if err := (embeddedConfigSink{cfg: cfg}).ApplyRemoved(context.Background()); err != nil {
		t.Fatalf("ApplyRemoved: %v", err)
	}

	got := cfg.EmbeddedLLM
	if got.Installed {
		t.Error("embedded_llm.installed survived a removal")
	}
	for key, value := range map[string]string{
		"packing": got.Packing, "backend": got.Backend, "model_file": got.ModelFile,
		"runtime_version": got.RuntimeVersion, "installed_at": got.InstalledAt,
	} {
		if value != "" {
			t.Errorf("embedded_llm.%s = %q survived a removal", key, value)
		}
	}
	if got.Port != 0 {
		t.Errorf("embedded_llm.port = %d, want the 0 (allocate-at-install) sentinel", got.Port)
	}
	if !reflect.DeepEqual(got.Tuning, want) {
		t.Errorf("a removal reset the operator's memory plan:\n got %+v\nwant %+v", got.Tuning, want)
	}
	// The idle budget survives too — the two operator-owned sub-sections are
	// preserved by the same rule.
	if got.AutoUnload.Enabled == nil || *got.AutoUnload.Enabled {
		t.Errorf("auto_unload.enabled = %v, want the operator's explicit false", got.AutoUnload.Enabled)
	}
}

// TestEmbeddedLLMTuningEditDoesNotRewriteInstallState is the round-trip half of
// the same guarantee: editing the tuning section of a persisted config.yaml and
// saving it again must leave every app-written record field byte-identical. A
// tuning change is a settings change, not a re-provision.
func TestEmbeddedLLMTuningEditDoesNotRewriteInstallState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := minimalValidConfig()
	cfg.EmbeddedLLM = installedEmbeddedState()
	cfg.LLM.DefaultModel = EmbeddedLLMProviderName + "/" + EmbeddedLLMModelName
	before := cfg.EmbeddedLLM
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	// Re-load from disk (so the edit starts from the persisted bytes, not from
	// an in-memory struct), change ONLY the tuning section, and save again.
	reloaded, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult(): %v", err)
	}
	edited := reloaded.Config
	edited.EmbeddedLLM.Tuning = operatorTuning()
	if err := Save(edited, path); err != nil {
		t.Fatalf("Save() after the tuning edit: %v", err)
	}

	final, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult() after the tuning edit: %v", err)
	}
	after := final.Config.EmbeddedLLM

	for _, field := range []struct {
		key  string
		got  any
		want any
	}{
		{"installed", after.Installed, before.Installed},
		{"packing", after.Packing, before.Packing},
		{"backend", after.Backend, before.Backend},
		{"port", after.Port, before.Port},
		{"model_file", after.ModelFile, before.ModelFile},
		{"runtime_version", after.RuntimeVersion, before.RuntimeVersion},
		{"installed_at", after.InstalledAt, before.InstalledAt},
	} {
		if field.got != field.want {
			t.Errorf("embedded_llm.%s = %v after a tuning edit, want the untouched %v",
				field.key, field.got, field.want)
		}
	}
	if !reflect.DeepEqual(after.Tuning, operatorTuning()) {
		t.Errorf("the tuning edit did not survive the round-trip: %+v", after.Tuning)
	}
	// The generated provider record follows the untouched port, so it is
	// untouched too.
	if entry := final.Config.LLM.OpenAICompatible[EmbeddedLLMProviderName]; entry.BaseURL != before.BaseURL() {
		t.Errorf("base_url = %q after a tuning edit, want the untouched %q",
			entry.BaseURL, before.BaseURL())
	}
}
