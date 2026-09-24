package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"gopkg.in/yaml.v3"
)

// embeddedBoolPtr / embeddedIntPtr build the pointer-valued auto_unload fields.
func embeddedBoolPtr(v bool) *bool { return &v }
func embeddedIntPtr(v int) *int    { return &v }

// installedEmbeddedState is the canonical "install completed" section: every
// field populated, so a round-trip or a sync regression shows up immediately.
func installedEmbeddedState() EmbeddedLLMConfig {
	return EmbeddedLLMConfig{
		Installed:      true,
		Packing:        "PQ2_0",
		Backend:        "metal",
		Port:           4321,
		ModelFile:      "/home/u/.c0wrk/models/bonsai-2-27b/Ternary-Bonsai-2-27B-PQ2_0.gguf",
		RuntimeVersion: "prism-b10709-9a9394a",
		InstalledAt:    "2026-09-23T10:15:00Z",
		AutoUnload: AutoUnloadConfig{
			Enabled: embeddedBoolPtr(false),
			Minutes: embeddedIntPtr(15),
		},
	}
}

// TestEmbeddedLLMYAMLRoundTrip pins the persisted shape of the section: every
// field survives marshal → unmarshal with its yaml tag, including the
// pointer-valued auto_unload knobs, and an explicitly disabled timer is not
// resurrected by the defaults.
func TestEmbeddedLLMYAMLRoundTrip(t *testing.T) {
	original := installedEmbeddedState()

	data, err := yaml.Marshal(&original)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}

	var restored EmbeddedLLMConfig
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}

	if restored.Installed != original.Installed {
		t.Errorf("installed = %v, want %v", restored.Installed, original.Installed)
	}
	if restored.Packing != original.Packing {
		t.Errorf("packing = %q, want %q", restored.Packing, original.Packing)
	}
	if restored.Backend != original.Backend {
		t.Errorf("backend = %q, want %q", restored.Backend, original.Backend)
	}
	if restored.Port != original.Port {
		t.Errorf("port = %d, want %d", restored.Port, original.Port)
	}
	if restored.ModelFile != original.ModelFile {
		t.Errorf("model_file = %q, want %q", restored.ModelFile, original.ModelFile)
	}
	if restored.RuntimeVersion != original.RuntimeVersion {
		t.Errorf("runtime_version = %q, want %q", restored.RuntimeVersion, original.RuntimeVersion)
	}
	if restored.InstalledAt != original.InstalledAt {
		t.Errorf("installed_at = %q, want %q", restored.InstalledAt, original.InstalledAt)
	}
	if restored.AutoUnload.Enabled == nil || *restored.AutoUnload.Enabled {
		t.Errorf("auto_unload.enabled = %v, want an explicit false", restored.AutoUnload.Enabled)
	}
	if restored.AutoUnload.Minutes == nil || *restored.AutoUnload.Minutes != 15 {
		t.Errorf("auto_unload.minutes = %v, want 15", restored.AutoUnload.Minutes)
	}

	// The documented yaml keys, so a renamed tag cannot slip through.
	for _, key := range []string{
		"installed:", "packing:", "backend:", "port:", "model_file:",
		"runtime_version:", "installed_at:", "auto_unload:", "enabled:", "minutes:",
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("marshalled section is missing key %q:\n%s", key, data)
		}
	}
}

// TestEmbeddedLLMConfigRoundTrip verifies the section is reachable from the
// top-level config under the `embedded_llm:` key.
func TestEmbeddedLLMConfigRoundTrip(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.EmbeddedLLM = installedEmbeddedState()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}
	if !strings.Contains(string(data), "embedded_llm:") {
		t.Fatalf("marshalled config has no embedded_llm section:\n%s", data)
	}

	var restored Config
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	if restored.EmbeddedLLM.Port != 4321 || !restored.EmbeddedLLM.Installed {
		t.Errorf("embedded_llm did not round-trip: %+v", restored.EmbeddedLLM)
	}
}

// TestEmbeddedLLMDefaults verifies ApplyDefaults seeds the documented defaults
// (auto_unload.enabled true, auto_unload.minutes 60) and respects explicit
// values — including an explicitly disabled timer.
func TestEmbeddedLLMDefaults(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if cfg.EmbeddedLLM.Installed {
		t.Error("embedded_llm.installed must default to false")
	}
	if cfg.EmbeddedLLM.Port != 0 {
		t.Errorf("embedded_llm.port = %d, want the 0 (allocate-at-install) sentinel", cfg.EmbeddedLLM.Port)
	}
	if !cfg.EmbeddedLLM.AutoUnload.IsEnabled() {
		t.Error("auto_unload.enabled must default to true")
	}
	if cfg.EmbeddedLLM.AutoUnload.Enabled == nil {
		t.Error("ApplyDefaults must materialize the auto_unload.enabled default pointer")
	}
	if got := cfg.EmbeddedLLM.AutoUnload.IdleMinutes(); got != EmbeddedLLMDefaultAutoUnloadMinutes {
		t.Errorf("auto_unload.minutes = %d, want %d", got, EmbeddedLLMDefaultAutoUnloadMinutes)
	}
	if got := cfg.EmbeddedLLM.AutoUnload.Minutes; got == nil || *got != 60 {
		t.Errorf("auto_unload.minutes pointer = %v, want 60", got)
	}

	// A nil section still resolves to the documented defaults.
	var zero AutoUnloadConfig
	if !zero.IsEnabled() {
		t.Error("nil auto_unload.enabled must resolve to true")
	}
	if got := zero.IdleMinutes(); got != 60 {
		t.Errorf("nil auto_unload.minutes must resolve to 60, got %d", got)
	}

	// Explicit values win.
	explicit := &Config{}
	explicit.EmbeddedLLM.AutoUnload.Enabled = embeddedBoolPtr(false)
	explicit.EmbeddedLLM.AutoUnload.Minutes = embeddedIntPtr(5)
	ApplyDefaults(explicit)
	if explicit.EmbeddedLLM.AutoUnload.IsEnabled() {
		t.Error("an explicit auto_unload.enabled: false must not be overwritten by the default")
	}
	if got := explicit.EmbeddedLLM.AutoUnload.IdleMinutes(); got != 5 {
		t.Errorf("an explicit auto_unload.minutes: 5 must be kept, got %d", got)
	}
}

// TestEmbeddedLLMValidateRejected verifies validate() fails fast on a corrupt
// embedded_llm section with an actionable message naming the offending key.
func TestEmbeddedLLMValidateRejected(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*EmbeddedLLMConfig)
		wantMsg string
	}{
		"port below the range": {
			mutate:  func(c *EmbeddedLLMConfig) { c.Port = 80 },
			wantMsg: "embedded_llm.port 80 is not valid",
		},
		"port above the range": {
			mutate:  func(c *EmbeddedLLMConfig) { c.Port = 70000 },
			wantMsg: "embedded_llm.port 70000 is not valid",
		},
		"negative port": {
			mutate:  func(c *EmbeddedLLMConfig) { c.Port = -1 },
			wantMsg: "embedded_llm.port -1 is not valid",
		},
		"installed without an allocated port": {
			mutate:  func(c *EmbeddedLLMConfig) { c.Installed = true },
			wantMsg: "embedded_llm.installed is true but embedded_llm.port is 0",
		},
		"zero idle minutes": {
			mutate:  func(c *EmbeddedLLMConfig) { c.AutoUnload.Minutes = embeddedIntPtr(0) },
			wantMsg: "embedded_llm.auto_unload.minutes 0 is not valid",
		},
		"negative idle minutes": {
			mutate:  func(c *EmbeddedLLMConfig) { c.AutoUnload.Minutes = embeddedIntPtr(-30) },
			wantMsg: "embedded_llm.auto_unload.minutes -30 is not valid",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := minimalValidConfig()
			tt.mutate(&cfg.EmbeddedLLM)

			err := validate(cfg)
			if err == nil {
				t.Fatalf("validate() accepted an invalid embedded_llm section: %+v", cfg.EmbeddedLLM)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
			// Actionable: the message must tell the operator what to do, not
			// just what is wrong.
			if !strings.Contains(err.Error(), "must be") && !strings.Contains(err.Error(), "reinstall") {
				t.Errorf("error %q is not actionable (no remediation hint)", err)
			}
		})
	}
}

// TestEmbeddedLLMValidateAccepted verifies the legal shapes pass: the
// not-installed zero section, an allocated port at both range boundaries, and a
// disabled idle timer.
func TestEmbeddedLLMValidateAccepted(t *testing.T) {
	for name, mutate := range map[string]func(*EmbeddedLLMConfig){
		"not installed":      func(*EmbeddedLLMConfig) {},
		"installed":          func(c *EmbeddedLLMConfig) { c.Installed = true; c.Port = 4321 },
		"lowest legal port":  func(c *EmbeddedLLMConfig) { c.Port = EmbeddedLLMMinPort },
		"highest legal port": func(c *EmbeddedLLMConfig) { c.Port = EmbeddedLLMMaxPort },
		"timer disabled": func(c *EmbeddedLLMConfig) {
			c.AutoUnload.Enabled = embeddedBoolPtr(false)
			c.AutoUnload.Minutes = embeddedIntPtr(1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := minimalValidConfig()
			mutate(&cfg.EmbeddedLLM)
			if err := validate(cfg); err != nil {
				t.Fatalf("validate() rejected a legal embedded_llm section: %v", err)
			}
		})
	}
}

// TestSyncEmbeddedProviderGeneratesAndRemoves pins the backend-owned provider
// record: generated from the persisted port while installed, gone once not, and
// resolvable as the composite model id in both directions.
func TestSyncEmbeddedProviderGeneratesAndRemoves(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.EmbeddedLLM = installedEmbeddedState()

	if !cfg.SyncEmbeddedLLMProvider(0) {
		t.Fatal("syncing an installed state into an empty map must report a change")
	}

	entry, ok := cfg.LLM.OpenAICompatible[EmbeddedLLMProviderName]
	if !ok {
		t.Fatalf("provider %q was not generated: %+v", EmbeddedLLMProviderName, cfg.LLM.OpenAICompatible)
	}
	if want := "http://127.0.0.1:4321/v1"; entry.BaseURL != want {
		t.Errorf("base_url = %q, want %q", entry.BaseURL, want)
	}
	if entry.APIKey != "" {
		t.Errorf("api_key = %q, want empty (a loopback server takes no key)", entry.APIKey)
	}
	if len(entry.Models) != 1 || entry.Models[0] != EmbeddedLLMModelName {
		t.Errorf("models = %v, want [%s]", entry.Models, EmbeddedLLMModelName)
	}
	if entry.TLSFingerprint != "" {
		t.Errorf("tls_fingerprint = %q, want empty on a plain-HTTP loopback endpoint", entry.TLSFingerprint)
	}

	// Idempotent: a second sync changes nothing.
	if cfg.SyncEmbeddedLLMProvider(0) {
		t.Error("a second sync of an unchanged state must report no change")
	}

	// The composite id resolves through the normal path, and so does a bare
	// name; the provider is typed as openai-compatible.
	composite := EmbeddedLLMProviderName + "/" + EmbeddedLLMModelName
	gotID, ambiguous, err := cfg.LLM.ResolveModelID(composite)
	if err != nil {
		t.Fatalf("ResolveModelID(%q) failed: %v", composite, err)
	}
	if gotID != composite || ambiguous {
		t.Errorf("ResolveModelID(%q) = %q, ambiguous=%v; want the same composite, unambiguous", composite, gotID, ambiguous)
	}
	gotBare, _, err := cfg.LLM.ResolveModelID(EmbeddedLLMModelName)
	if err != nil {
		t.Fatalf("ResolveModelID(%q) failed: %v", EmbeddedLLMModelName, err)
	}
	if gotBare != composite {
		t.Errorf("bare name resolved to %q, want %q", gotBare, composite)
	}

	cfg.LLM.DefaultModel = composite
	provider, bare, err := cfg.LLM.ResolveDefaultModelProvider()
	if err != nil {
		t.Fatalf("ResolveDefaultModelProvider() failed: %v", err)
	}
	if provider.Name != EmbeddedLLMProviderName || provider.ProviderType != "openai" {
		t.Errorf("provider = %q/%q, want %q/openai", provider.Name, provider.ProviderType, EmbeddedLLMProviderName)
	}
	if provider.BaseURL != "http://127.0.0.1:4321/v1" || bare != EmbeddedLLMModelName {
		t.Errorf("resolved provider = %+v, bare = %q", provider, bare)
	}
	if len(cfg.LLM.AllModelIDs()) == 0 {
		t.Error("AllModelIDs must include the embedded model")
	}

	// Uninstall: the record disappears and the composite id no longer resolves.
	cfg.EmbeddedLLM.Installed = false
	if !cfg.SyncEmbeddedLLMProvider(0) {
		t.Fatal("removing an installed state must report a change")
	}
	if _, stillThere := cfg.LLM.OpenAICompatible[EmbeddedLLMProviderName]; stillThere {
		t.Errorf("provider %q survived the uninstall: %+v", EmbeddedLLMProviderName, cfg.LLM.OpenAICompatible)
	}
	if _, _, err := cfg.LLM.ResolveModelID(composite); err == nil {
		t.Errorf("ResolveModelID(%q) must fail once the model is uninstalled", composite)
	}
	if cfg.SyncEmbeddedLLMProvider(0) {
		t.Error("a second sync of an uninstalled state must report no change")
	}
}

// TestSyncEmbeddedProviderFollowsPortAndPreservesReserve verifies the base URL
// is always derived from the persisted port (a reallocation cannot leave a
// stale endpoint) and that the one operator knob with no embedded_llm
// counterpart survives regeneration.
func TestSyncEmbeddedProviderFollowsPortAndPreservesReserve(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.EmbeddedLLM = installedEmbeddedState()
	cfg.LLM.OpenAICompatible = map[string]OpenAICompatibleConfig{
		EmbeddedLLMProviderName: {
			BaseURL:            "http://127.0.0.1:1234/v1",
			Models:             []string{"stale"},
			OutputTokenReserve: 4096,
		},
		"lmstudio": {BaseURL: "http://127.0.0.1:1234/v1", Models: []string{"local-model"}},
	}

	if !cfg.SyncEmbeddedLLMProvider(0) {
		t.Fatal("a stale embedded record must be regenerated")
	}
	entry := cfg.LLM.OpenAICompatible[EmbeddedLLMProviderName]
	if entry.BaseURL != "http://127.0.0.1:4321/v1" {
		t.Errorf("base_url = %q, want the URL derived from the persisted port", entry.BaseURL)
	}
	if len(entry.Models) != 1 || entry.Models[0] != EmbeddedLLMModelName {
		t.Errorf("models = %v, want [%s]", entry.Models, EmbeddedLLMModelName)
	}
	if entry.OutputTokenReserve != 4096 {
		t.Errorf("output_token_reserve = %d, want the preserved 4096", entry.OutputTokenReserve)
	}
	// A sibling provider is untouched.
	if got := cfg.LLM.OpenAICompatible["lmstudio"]; len(got.Models) != 1 || got.Models[0] != "local-model" {
		t.Errorf("sibling provider was modified: %+v", got)
	}

	// A port reallocation re-derives the URL.
	cfg.EmbeddedLLM.Port = 5599
	if !cfg.SyncEmbeddedLLMProvider(0) {
		t.Fatal("a port change must regenerate the record")
	}
	if got := cfg.LLM.OpenAICompatible[EmbeddedLLMProviderName].BaseURL; got != "http://127.0.0.1:5599/v1" {
		t.Errorf("base_url = %q after the port change, want http://127.0.0.1:5599/v1", got)
	}
}

// TestSyncEmbeddedProviderContextWindowOverride verifies the resolved RAM tier
// is recorded as an llm.models override without disturbing the other override
// fields, and that a zero tier never clears an existing one.
func TestSyncEmbeddedProviderContextWindowOverride(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.EmbeddedLLM = installedEmbeddedState()
	cfg.LLM.Models = map[string]ModelOverride{
		EmbeddedLLMModelName: {Family: "qwen3", OutputLimit: 8192},
		"gpt-4o":             {ContextWindow: 128000},
	}

	if !cfg.SyncEmbeddedLLMProvider(32768) {
		t.Fatal("writing a resolved context tier must report a change")
	}
	override := cfg.LLM.Models[EmbeddedLLMModelName]
	if override.ContextWindow != 32768 {
		t.Errorf("context_window = %d, want the resolved 32768", override.ContextWindow)
	}
	if override.Family != "qwen3" || override.OutputLimit != 8192 {
		t.Errorf("the other override fields were clobbered: %+v", override)
	}
	if got := cfg.LLM.Models["gpt-4o"].ContextWindow; got != 128000 {
		t.Errorf("an unrelated model override changed: %d", got)
	}

	// Same tier again: no change reported, no churn.
	if cfg.SyncEmbeddedLLMProvider(32768) {
		t.Error("re-writing the same tier must report no change")
	}

	// A zero tier (the load path, which performs no probe) leaves it alone.
	if cfg.SyncEmbeddedLLMProvider(0) {
		t.Error("a zero context window must not touch the override")
	}
	if got := cfg.LLM.Models[EmbeddedLLMModelName].ContextWindow; got != 32768 {
		t.Errorf("context_window = %d after a zero-tier sync, want it preserved", got)
	}

	// A fresh tier replaces it.
	if !cfg.SyncEmbeddedLLMProvider(65536) {
		t.Fatal("a new resolved tier must report a change")
	}
	if got := cfg.LLM.Models[EmbeddedLLMModelName].ContextWindow; got != 65536 {
		t.Errorf("context_window = %d, want the re-resolved 65536", got)
	}

	// The override map is created when absent.
	fresh := minimalValidConfig()
	fresh.EmbeddedLLM = installedEmbeddedState()
	if !fresh.SyncEmbeddedLLMProvider(16384) {
		t.Fatal("syncing into a nil override map must report a change")
	}
	if got := fresh.LLM.Models[EmbeddedLLMModelName].ContextWindow; got != 16384 {
		t.Errorf("context_window = %d, want 16384", got)
	}
}

// resolveEmbeddedModelMetadata seeds a registry from the config's llm.models
// overrides exactly the way core.buildRouter does, and returns the effective
// metadata for the embedded model. It is the only way to observe what a family
// authored (or not authored) in the override actually resolves to.
func resolveEmbeddedModelMetadata(t *testing.T, cfg *Config) llm.ModelMetadata {
	t.Helper()
	overrides := make(map[string]llm.ModelMetadata, len(cfg.LLM.Models))
	for name, override := range cfg.LLM.Models {
		overrides[name] = llm.ModelMetadata{
			ContextWindow: override.ContextWindow,
			OutputLimit:   override.OutputLimit,
			TokenizerType: override.TokenizerType,
			Family:        override.Family,
			Protocol:      llm.APIProtocol(override.Protocol),
			Capabilities:  override.Capabilities,
		}
	}
	meta, _ := llm.NewModelRegistry(overrides).ResolveLocal(EmbeddedLLMModelName)
	return meta
}

// TestSyncEmbeddedProviderFamilyResolution pins the two halves of the embedded
// model's family contract — the value the reasoning-effort picker keys off:
//
//   - a user-authored `family:` tweak survives every sync (the RAM-tier write
//     touches context_window only) and stays AUTHORITATIVE in the registry the
//     router is built from;
//   - with no family authored — the shape SyncEmbeddedProvider itself writes —
//     the effective family comes from the sp4rk catalog ("qwen" for the Bonsai
//     checkpoint), not from DetectFamily's "default" fallback. c0wrk never
//     spells the family out, so a catalog correction needs no c0wrk change.
func TestSyncEmbeddedProviderFamilyResolution(t *testing.T) {
	t.Run("user family tweak survives and wins", func(t *testing.T) {
		cfg := minimalValidConfig()
		cfg.EmbeddedLLM = installedEmbeddedState()
		cfg.LLM.Models = map[string]ModelOverride{
			EmbeddedLLMModelName: {Family: "qwen3", OutputLimit: 8192},
		}

		// Every sync shape: first write, idempotent re-write, re-resolved tier
		// and the zero tier of the load path.
		for _, tier := range []int{32768, 32768, 65536, 0} {
			cfg.SyncEmbeddedLLMProvider(tier)
			override := cfg.LLM.Models[EmbeddedLLMModelName]
			if override.Family != "qwen3" {
				t.Fatalf("family = %q after syncing tier %d, want the user's qwen3 preserved", override.Family, tier)
			}
			if override.OutputLimit != 8192 {
				t.Fatalf("output_limit = %d after syncing tier %d, want it preserved", override.OutputLimit, tier)
			}
		}
		if got := cfg.LLM.Models[EmbeddedLLMModelName].ContextWindow; got != 65536 {
			t.Errorf("context_window = %d, want the last resolved 65536", got)
		}
		if meta := resolveEmbeddedModelMetadata(t, cfg); meta.Family != "qwen3" {
			t.Errorf("resolved family = %q, want the user's authoritative qwen3", meta.Family)
		}
	})

	t.Run("absent family resolves from the catalog", func(t *testing.T) {
		cfg := minimalValidConfig()
		cfg.EmbeddedLLM = installedEmbeddedState()
		if !cfg.SyncEmbeddedLLMProvider(32768) {
			t.Fatal("writing the resolved context tier reported no change")
		}
		override := cfg.LLM.Models[EmbeddedLLMModelName]
		if override.ContextWindow != 32768 {
			t.Fatalf("context_window = %d, want 32768", override.ContextWindow)
		}
		if override.Family != "" {
			t.Fatalf("family = %q, want it left unset so the catalog decides", override.Family)
		}

		meta := resolveEmbeddedModelMetadata(t, cfg)
		if meta.Family != "qwen" {
			t.Errorf("resolved family = %q, want qwen from the sp4rk catalog", meta.Family)
		}
		if meta.ContextWindow != 32768 {
			t.Errorf("resolved context_window = %d, want the override's 32768 to win over the catalog", meta.ContextWindow)
		}
		if meta.Capabilities == nil {
			t.Fatal("resolved capabilities = nil, want the catalog set inherited through the override")
		}
		if !meta.Capabilities.Reasoning {
			t.Error("Reasoning = false, want true — without it the picker renders no effort control")
		}
		if !meta.Capabilities.Attachment {
			t.Error("Attachment = false, want true — vision gating must be unchanged")
		}
	})
}

// TestSyncEmbeddedProviderCopiesSharedMaps pins the copy-on-write behavior
// UpdateLLMConfig relies on: its candidate is a struct copy that SHARES the
// provider and override maps with the live config, so a sync that mutated in
// place would leak an uncommitted (or rolled-back) change to every reader.
func TestSyncEmbeddedProviderCopiesSharedMaps(t *testing.T) {
	live := minimalValidConfig()
	live.EmbeddedLLM = installedEmbeddedState()
	live.LLM.OpenAICompatible = map[string]OpenAICompatibleConfig{
		"lmstudio": {BaseURL: "http://127.0.0.1:1234/v1", Models: []string{"local-model"}},
	}
	live.LLM.Models = map[string]ModelOverride{"gpt-4o": {ContextWindow: 128000}}

	candidate := live.LLM // struct copy: same map headers
	candidate.SyncEmbeddedProvider(live.EmbeddedLLM, 32768)

	if _, ok := candidate.OpenAICompatible[EmbeddedLLMProviderName]; !ok {
		t.Fatal("the candidate did not receive the generated provider")
	}
	if _, leaked := live.LLM.OpenAICompatible[EmbeddedLLMProviderName]; leaked {
		t.Error("the sync mutated the shared provider map: the live config now carries an uncommitted entry")
	}
	if got := live.LLM.Models[EmbeddedLLMModelName].ContextWindow; got != 0 {
		t.Errorf("the sync mutated the shared override map: live context_window = %d", got)
	}
	if candidate.Models[EmbeddedLLMModelName].ContextWindow != 32768 {
		t.Error("the candidate did not receive the context window override")
	}

	// The removal direction is copy-on-write too.
	live.LLM.OpenAICompatible[EmbeddedLLMProviderName] = live.EmbeddedLLM.ProviderConfig()
	removed := live.LLM
	live.EmbeddedLLM.Installed = false
	removed.SyncEmbeddedProvider(live.EmbeddedLLM, 0)
	if _, ok := removed.OpenAICompatible[EmbeddedLLMProviderName]; ok {
		t.Error("the candidate kept the provider after an uninstall sync")
	}
	if _, stillLive := live.LLM.OpenAICompatible[EmbeddedLLMProviderName]; !stillLive {
		t.Error("the sync deleted the provider from the shared map of the live config")
	}
}

// TestLoadWithResultGeneratesEmbeddedProvider verifies the production load path
// reconciles the section: a config whose embedded model is installed but whose
// provider record was hand-deleted (or never written) loads with the record
// regenerated, and the embedded model can be the ONLY configured provider.
func TestLoadWithResultGeneratesEmbeddedProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	src := `
llm:
  default_model: embedded/Bonsai 2 27B
embedded_llm:
  installed: true
  packing: PQ2_0
  backend: metal
  port: 4321
  model_file: /tmp/bonsai.gguf
  runtime_version: prism-b10709-9a9394a
  installed_at: "2026-09-23T10:15:00Z"
  auto_unload:
    enabled: true
    minutes: 45
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Errorf("load warnings = %v, want none", result.LoadErrors)
	}

	entry, ok := result.Config.LLM.OpenAICompatible[EmbeddedLLMProviderName]
	if !ok {
		t.Fatalf("the load path did not generate the embedded provider: %+v", result.Config.LLM.OpenAICompatible)
	}
	if entry.BaseURL != "http://127.0.0.1:4321/v1" {
		t.Errorf("base_url = %q, want http://127.0.0.1:4321/v1", entry.BaseURL)
	}
	if got := result.Config.EmbeddedLLM.AutoUnload.IdleMinutes(); got != 45 {
		t.Errorf("auto_unload.minutes = %d, want the authored 45", got)
	}
	if !result.Config.EmbeddedLLM.AutoUnload.IsEnabled() {
		t.Error("auto_unload.enabled = false, want the authored true")
	}
	if _, _, err := result.Config.LLM.ResolveDefaultModelProvider(); err != nil {
		t.Errorf("the embedded model must be usable as the only default_model: %v", err)
	}

	// Saving the loaded config persists both the authoritative section and the
	// generated provider record.
	if err := Save(result.Config, path); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() after Save() failed: %v", err)
	}
	if reloaded.LLM.OpenAICompatible[EmbeddedLLMProviderName].BaseURL != "http://127.0.0.1:4321/v1" {
		t.Errorf("the saved config lost the generated provider: %+v", reloaded.LLM.OpenAICompatible)
	}
	if reloaded.EmbeddedLLM.ModelFile != "/tmp/bonsai.gguf" {
		t.Errorf("model_file = %q after the round-trip", reloaded.EmbeddedLLM.ModelFile)
	}
}

// TestLoadWithResultRemovesStaleEmbeddedProvider verifies the other direction:
// a hand-written `openai_compatible.embedded` with no install behind it is
// dropped at load, so a dangling loopback provider can never survive.
func TestLoadWithResultRemovesStaleEmbeddedProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	src := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: key
    models: ["claude-3-haiku"]
  openai_compatible:
    embedded:
      base_url: http://127.0.0.1:9999/v1
      models: ["Bonsai 2 27B"]
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	if _, ok := result.Config.LLM.OpenAICompatible[EmbeddedLLMProviderName]; ok {
		t.Errorf("a provider record with no install behind it survived the load: %+v", result.Config.LLM.OpenAICompatible)
	}
}
