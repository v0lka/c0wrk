package config

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/embeddedllm"
	"gopkg.in/yaml.v3"
)

// This file pins the persisted override surface for the embedded model's memory
// plan: `embedded_llm.tuning`. Three properties carry the weight.
//
//  1. UNSET IS REPRESENTABLE. Every knob is a pointer, so "the operator never
//     wrote this" is a different value from "the operator wrote auto" — and
//     for `fit` and `cache_ram_mib` it is a different PLAN, not just a
//     different spelling. The round-trip tests fail if a save ever collapses
//     the two.
//  2. VALIDATION AND TRANSLATION ARE ONE FUNCTION. validate() delegates to
//     ToTuning, so a config that loads is by construction a config the planner
//     can honour; there is no second list of legal values to drift.
//  3. IT IS A SETTING, NOT A RECORD. Install and Remove rewrite embedded_llm.*
//     wholesale and must carry tuning through verbatim — see
//     embedded_llm_sink_test.go for the executable contract.

// embeddedStrPtr / embeddedFloatPtr build the pointer-valued tuning fields.
// (embeddedBoolPtr and embeddedIntPtr live in embedded_llm_test.go.)
func embeddedStrPtr(v string) *string     { return &v }
func embeddedFloatPtr(v float64) *float64 { return &v }

// fullySpecifiedTuning sets EVERY knob to a non-default, non-Auto value, so a
// field that loses its yaml tag or its mapping shows up as a round-trip or a
// translation failure rather than as a silently ignored setting.
func fullySpecifiedTuning() TuningConfig {
	return TuningConfig{
		Context: EmbeddedLLMContextConfig{
			Mode:   embeddedStrPtr(EmbeddedLLMContextExact),
			Tokens: embeddedIntPtr(49152),
		},
		KVCacheType: embeddedStrPtr(string(embeddedllm.KVTypeQ8_0)),
		Offload: EmbeddedLLMOffloadConfig{
			Mode:   embeddedStrPtr(EmbeddedLLMOffloadLayers),
			Layers: embeddedIntPtr(40),
		},
		Fit:            embeddedBoolPtr(false),
		FitTargetMiB:   embeddedIntPtr(2048),
		FitMinContext:  embeddedIntPtr(32768),
		KVOffload:      embeddedBoolPtr(false),
		MMProjOffload:  embeddedBoolPtr(false),
		Packing:        embeddedStrPtr(string(embeddedllm.PackingPTQ1_0)),
		Parallel:       embeddedIntPtr(2),
		CacheRAMMiB:    embeddedIntPtr(512),
		HostReserveGiB: embeddedFloatPtr(6.5),
	}
}

// TestEmbeddedLLMTuningYAMLRoundTrip pins the persisted shape: every knob
// survives marshal → unmarshal with its documented key, at a value that is not
// the default, so a renamed tag or a dropped field cannot pass.
func TestEmbeddedLLMTuningYAMLRoundTrip(t *testing.T) {
	original := installedEmbeddedState()
	original.Tuning = fullySpecifiedTuning()

	data, err := yaml.Marshal(&original)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}

	var restored EmbeddedLLMConfig
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	if !reflect.DeepEqual(restored.Tuning, original.Tuning) {
		t.Errorf("tuning did not round-trip:\n got %+v\nwant %+v\nYAML:\n%s",
			restored.Tuning, original.Tuning, data)
	}

	// The documented yaml keys, so a renamed tag cannot slip through.
	for _, key := range []string{
		"tuning:", "context:", "mode:", "tokens:", "kv_cache_type:", "offload:",
		"layers:", "fit:", "fit_target_mib:", "fit_min_context:", "kv_offload:",
		"mmproj_offload:", "packing:", "parallel:", "cache_ram_mib:",
		"host_reserve_gib:",
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("marshalled tuning section is missing key %q:\n%s", key, data)
		}
	}
}

// TestEmbeddedLLMTuningUnsetIsDistinguishableFromExplicitAuto is the property
// the whole surface exists for. An all-explicit-auto section and an all-absent
// section resolve to the SAME plan, but they must not be the same VALUE: only
// the absent one reports "the operator chose nothing", which is what the
// Install/Remove preservation and the no-seeding rule key off.
func TestEmbeddedLLMTuningUnsetIsDistinguishableFromExplicitAuto(t *testing.T) {
	explicit := TuningConfig{
		Context:       EmbeddedLLMContextConfig{Mode: embeddedStrPtr(EmbeddedLLMTuningAuto)},
		KVCacheType:   embeddedStrPtr(EmbeddedLLMTuningAuto),
		Offload:       EmbeddedLLMOffloadConfig{Mode: embeddedStrPtr(EmbeddedLLMTuningAuto)},
		Fit:           embeddedBoolPtr(true),
		FitTargetMiB:  embeddedIntPtr(0),
		FitMinContext: embeddedIntPtr(65536),
		KVOffload:     embeddedBoolPtr(true),
		MMProjOffload: embeddedBoolPtr(true),
		Packing:       embeddedStrPtr(EmbeddedLLMTuningAuto),
		Parallel:      embeddedIntPtr(1),
		CacheRAMMiB:   embeddedIntPtr(0),
		// HostReserveGiB stays nil: an explicit 0.0 would REPLACE the derived
		// reserve with nothing, which is a choice, not an auto.
	}

	section := EmbeddedLLMConfig{Tuning: explicit}
	data, err := yaml.Marshal(section)
	if err != nil {
		t.Fatalf("yaml.Marshal(): %v", err)
	}
	if !strings.Contains(string(data), "tuning:") {
		t.Fatalf("an explicitly authored tuning section must be written:\n%s", data)
	}

	var roundTripped EmbeddedLLMConfig
	if err := yaml.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("yaml.Unmarshal(): %v", err)
	}
	restored := roundTripped.Tuning
	if !reflect.DeepEqual(restored, explicit) {
		t.Errorf("explicit-auto tuning did not round-trip:\n got %+v\nwant %+v\nYAML:\n%s",
			restored, explicit, data)
	}
	if reflect.DeepEqual(restored, TuningConfig{}) {
		t.Error("an explicitly authored all-auto section must not equal the unset section")
	}
	// Every knob the operator wrote is still a non-nil pointer, which is the
	// distinction itself: nil means "never authored", and nothing here is nil
	// except the one field deliberately left out.
	if restored.Context.Mode == nil || restored.KVCacheType == nil ||
		restored.Offload.Mode == nil || restored.Fit == nil ||
		restored.FitTargetMiB == nil || restored.FitMinContext == nil ||
		restored.KVOffload == nil || restored.MMProjOffload == nil ||
		restored.Packing == nil || restored.Parallel == nil ||
		restored.CacheRAMMiB == nil {
		t.Errorf("an explicit knob came back unset: %+v", restored)
	}
	if restored.HostReserveGiB != nil {
		t.Errorf("host_reserve_gib = %v, want it left unset", restored.HostReserveGiB)
	}

	// The unset section writes NOTHING, so a config.yaml that never authored a
	// tuning knob does not grow a block of nulls on the first save.
	empty, err := yaml.Marshal(installedEmbeddedState())
	if err != nil {
		t.Fatalf("yaml.Marshal(): %v", err)
	}
	if strings.Contains(string(empty), "tuning:") {
		t.Errorf("an unauthored tuning section must be omitted, got:\n%s", empty)
	}
	var back EmbeddedLLMConfig
	if err := yaml.Unmarshal(empty, &back); err != nil {
		t.Fatalf("yaml.Unmarshal(): %v", err)
	}
	if !reflect.DeepEqual(back.Tuning, TuningConfig{}) {
		t.Errorf("tuning = %+v, want the all-unset zero value", back.Tuning)
	}

	// And both spellings plan identically, which is what makes the distinction
	// safe to keep: it records intent without changing the outcome.
	fromExplicit, err := explicit.ToTuning()
	if err != nil {
		t.Fatalf("explicit ToTuning(): %v", err)
	}
	if fromExplicit.KVType != "" || fromExplicit.Packing != "" {
		t.Errorf("an explicit auto must collapse onto the planner's own sentinel, got kv=%q packing=%q",
			fromExplicit.KVType, fromExplicit.Packing)
	}
	if fromExplicit.Context.Mode != embeddedllm.ContextAuto ||
		fromExplicit.Offload.Mode != embeddedllm.OffloadAuto {
		t.Errorf("an explicit auto must collapse onto the planner's own sentinel, got %+v / %+v",
			fromExplicit.Context, fromExplicit.Offload)
	}
	// …while the knobs where nil and an explicit value genuinely differ keep
	// the difference.
	if fromExplicit.CacheRAMMiB == nil || *fromExplicit.CacheRAMMiB != 0 {
		t.Errorf("cache_ram_mib = %v, want an explicit 0 (disables the prompt cache), not nil",
			fromExplicit.CacheRAMMiB)
	}
	if fromExplicit.FitEnabled == nil {
		t.Error("fit = nil, want an explicit true (nil lets the exclusivity rule decide)")
	} else if !*fromExplicit.FitEnabled {
		t.Errorf("fit = %v, want an explicit true", *fromExplicit.FitEnabled)
	}
}

// TestEmbeddedLLMTuningZeroValueIsAllUnset pins the "no knob is seeded"
// contract structurally: the zero TuningConfig has every pointer nil and every
// enum absent, which IS the documented all-Auto default.
func TestEmbeddedLLMTuningZeroValueIsAllUnset(t *testing.T) {
	var zero TuningConfig
	v := reflect.ValueOf(zero)
	for i := range v.NumField() {
		field := v.Field(i)
		name := v.Type().Field(i).Name
		switch field.Kind() { //nolint:exhaustive // only the three shapes TuningConfig uses
		case reflect.Pointer:
			if !field.IsNil() {
				t.Errorf("the zero TuningConfig.%s is not nil", name)
			}
		case reflect.Struct:
			inner := field
			for j := range inner.NumField() {
				if f := inner.Field(j); f.Kind() == reflect.Pointer && !f.IsNil() {
					t.Errorf("the zero TuningConfig.%s.%s is not nil", name,
						inner.Type().Field(j).Name)
				}
			}
		default:
			t.Errorf("TuningConfig.%s is a %s: every knob must be a pointer (or a struct of pointers) so unset stays representable",
				name, field.Kind())
		}
	}
}

// TestEmbeddedLLMTuningDefaultsAreNotSeeded proves ApplyDefaults leaves the
// section alone. Seeding it would turn every "unset" into an "explicit auto" on
// the first save — and for `fit` and `cache_ram_mib` that is a different memory
// plan, not a cosmetic one.
func TestEmbeddedLLMTuningDefaultsAreNotSeeded(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if !reflect.DeepEqual(cfg.EmbeddedLLM.Tuning, TuningConfig{}) {
		t.Errorf("ApplyDefaults seeded embedded_llm.tuning = %+v, want it left unset",
			cfg.EmbeddedLLM.Tuning)
	}

	// An explicit value survives ApplyDefaults untouched, including an explicit
	// 0 that must not be read as "unset".
	explicit := &Config{}
	explicit.EmbeddedLLM.Tuning.CacheRAMMiB = embeddedIntPtr(0)
	explicit.EmbeddedLLM.Tuning.Parallel = embeddedIntPtr(3)
	ApplyDefaults(explicit)
	if got := explicit.EmbeddedLLM.Tuning.CacheRAMMiB; got == nil || *got != 0 {
		t.Errorf("cache_ram_mib = %v, want the operator's explicit 0", got)
	}
	if got := explicit.EmbeddedLLM.Tuning.Parallel; got == nil || *got != 3 {
		t.Errorf("parallel = %v, want the operator's 3", got)
	}
}

// TestEmbeddedLLMTuningValidateRejected verifies validate() fails fast on a bad
// knob with an actionable message naming the key. The four rows the override
// surface is explicitly required to catch are the first four.
func TestEmbeddedLLMTuningValidateRejected(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*TuningConfig)
		wantMsg string
	}{
		"kv_cache_type outside the allow-list": {
			mutate:  func(c *TuningConfig) { c.KVCacheType = embeddedStrPtr("q5_0") },
			wantMsg: `embedded_llm.tuning.kv_cache_type "q5_0" is not valid`,
		},
		"negative fit_target_mib": {
			mutate:  func(c *TuningConfig) { c.FitTargetMiB = embeddedIntPtr(-1) },
			wantMsg: "embedded_llm.tuning.fit_target_mib -1 is not valid",
		},
		"parallel below 1": {
			mutate:  func(c *TuningConfig) { c.Parallel = embeddedIntPtr(0) },
			wantMsg: "embedded_llm.tuning.parallel 0 is not valid",
		},
		"context above the model's own training context": {
			mutate: func(c *TuningConfig) {
				c.Context.Mode = embeddedStrPtr(EmbeddedLLMContextExact)
				c.Context.Tokens = embeddedIntPtr(262145)
			},
			wantMsg: "embedded_llm.tuning.context.tokens 262145 is not valid",
		},

		"kv_cache_type that does not exist": {
			mutate:  func(c *TuningConfig) { c.KVCacheType = embeddedStrPtr("q4_1") },
			wantMsg: `embedded_llm.tuning.kv_cache_type "q4_1" is not valid`,
		},
		"packing outside the pinned set": {
			mutate:  func(c *TuningConfig) { c.Packing = embeddedStrPtr("Q4_K_M") },
			wantMsg: `embedded_llm.tuning.packing "Q4_K_M" is not valid`,
		},
		"context.mode outside the closed set": {
			mutate:  func(c *TuningConfig) { c.Context.Mode = embeddedStrPtr("ladder") },
			wantMsg: `embedded_llm.tuning.context.mode "ladder" is not valid`,
		},
		"offload.mode outside the closed set": {
			mutate:  func(c *TuningConfig) { c.Offload.Mode = embeddedStrPtr("some") },
			wantMsg: `embedded_llm.tuning.offload.mode "some" is not valid`,
		},
		"exact context with no token count": {
			mutate:  func(c *TuningConfig) { c.Context.Mode = embeddedStrPtr(EmbeddedLLMContextExact) },
			wantMsg: `embedded_llm.tuning.context.mode "exact" is not valid`,
		},
		"layers mode with no layer count": {
			mutate:  func(c *TuningConfig) { c.Offload.Mode = embeddedStrPtr(EmbeddedLLMOffloadLayers) },
			wantMsg: `embedded_llm.tuning.offload.mode "layers" is not valid`,
		},
		"negative layer count": {
			mutate: func(c *TuningConfig) {
				c.Offload.Mode = embeddedStrPtr(EmbeddedLLMOffloadLayers)
				c.Offload.Layers = embeddedIntPtr(-4)
			},
			wantMsg: "embedded_llm.tuning.offload.layers -4 is not valid",
		},
		"zero context": {
			mutate: func(c *TuningConfig) {
				c.Context.Mode = embeddedStrPtr(EmbeddedLLMContextExact)
				c.Context.Tokens = embeddedIntPtr(0)
			},
			wantMsg: "embedded_llm.tuning.context.tokens 0 is not valid",
		},
		"negative context": {
			mutate: func(c *TuningConfig) {
				c.Context.Mode = embeddedStrPtr(EmbeddedLLMContextExact)
				c.Context.Tokens = embeddedIntPtr(-8192)
			},
			wantMsg: "embedded_llm.tuning.context.tokens -8192 is not valid",
		},
		"fit_min_context above the ceiling": {
			mutate:  func(c *TuningConfig) { c.FitMinContext = embeddedIntPtr(262145) },
			wantMsg: "embedded_llm.tuning.fit_min_context 262145 is not valid",
		},
		"zero fit_min_context": {
			mutate:  func(c *TuningConfig) { c.FitMinContext = embeddedIntPtr(0) },
			wantMsg: "embedded_llm.tuning.fit_min_context 0 is not valid",
		},
		"negative cache_ram_mib": {
			mutate:  func(c *TuningConfig) { c.CacheRAMMiB = embeddedIntPtr(-1) },
			wantMsg: "embedded_llm.tuning.cache_ram_mib -1 is not valid",
		},
		"negative host_reserve_gib": {
			mutate:  func(c *TuningConfig) { c.HostReserveGiB = embeddedFloatPtr(-0.5) },
			wantMsg: "embedded_llm.tuning.host_reserve_gib -0.5 is not valid",
		},
		"negative parallel": {
			mutate:  func(c *TuningConfig) { c.Parallel = embeddedIntPtr(-2) },
			wantMsg: "embedded_llm.tuning.parallel -2 is not valid",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := minimalValidConfig()
			tt.mutate(&cfg.EmbeddedLLM.Tuning)

			err := validate(cfg)
			if err == nil {
				t.Fatalf("validate() accepted an invalid embedded_llm.tuning section: %+v",
					cfg.EmbeddedLLM.Tuning)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
			// Actionable, and naming the key: the operator edits config.yaml by
			// hand, so the message has to say which line to change and what to
			// put there.
			if !strings.Contains(err.Error(), "must be") {
				t.Errorf("error %q is not actionable (no remediation hint)", err)
			}
			if !strings.Contains(err.Error(), "embedded_llm.tuning.") {
				t.Errorf("error %q does not name the offending key", err)
			}
		})
	}
}

// TestEmbeddedLLMTuningValidateAccepted verifies the legal shapes pass,
// including the explicit zeros that mean something different from "unset" and
// the case-insensitive spellings.
func TestEmbeddedLLMTuningValidateAccepted(t *testing.T) {
	for name, mutate := range map[string]func(*TuningConfig){
		"all unset": func(*TuningConfig) {},
		"all explicit auto": func(c *TuningConfig) {
			c.Context.Mode = embeddedStrPtr(EmbeddedLLMTuningAuto)
			c.KVCacheType = embeddedStrPtr(EmbeddedLLMTuningAuto)
			c.Offload.Mode = embeddedStrPtr(EmbeddedLLMTuningAuto)
			c.Packing = embeddedStrPtr(EmbeddedLLMTuningAuto)
		},
		"empty strings read as auto": func(c *TuningConfig) {
			c.KVCacheType = embeddedStrPtr("")
			c.Packing = embeddedStrPtr("  ")
		},
		"every knob specified": func(c *TuningConfig) { *c = fullySpecifiedTuning() },
		// 0 is a legitimate choice for these three, NOT a stand-in for unset:
		// fit_target_mib 0 keeps the runtime's own target, cache_ram_mib 0
		// disables the prompt cache, offload.layers 0 is `-ngl 0`.
		"explicit zeros": func(c *TuningConfig) {
			c.FitTargetMiB = embeddedIntPtr(0)
			c.CacheRAMMiB = embeddedIntPtr(0)
			c.Offload.Mode = embeddedStrPtr(EmbeddedLLMOffloadLayers)
			c.Offload.Layers = embeddedIntPtr(0)
			c.HostReserveGiB = embeddedFloatPtr(0)
		},
		"lowest legal context": func(c *TuningConfig) {
			c.Context.Mode = embeddedStrPtr(EmbeddedLLMContextExact)
			c.Context.Tokens = embeddedIntPtr(EmbeddedLLMMinContextTokens)
		},
		"highest legal context": func(c *TuningConfig) {
			ceiling, err := EmbeddedLLMMaxContextTokens()
			if err != nil {
				t.Fatalf("EmbeddedLLMMaxContextTokens(): %v", err)
			}
			c.Context.Mode = embeddedStrPtr(EmbeddedLLMContextExact)
			c.Context.Tokens = embeddedIntPtr(ceiling)
		},
		"case-insensitive packing": func(c *TuningConfig) {
			c.Packing = embeddedStrPtr("ptq1_0")
		},
		"case-insensitive kv type": func(c *TuningConfig) {
			c.KVCacheType = embeddedStrPtr(" Q4_0 ")
		},
		"offload all": func(c *TuningConfig) {
			c.Offload.Mode = embeddedStrPtr(EmbeddedLLMOffloadAll)
		},
		"offload cpu": func(c *TuningConfig) {
			c.Offload.Mode = embeddedStrPtr(EmbeddedLLMOffloadCPU)
		},
		"parallel 1":   func(c *TuningConfig) { c.Parallel = embeddedIntPtr(1) },
		"fit enabled":  func(c *TuningConfig) { c.Fit = embeddedBoolPtr(true) },
		"kv on device": func(c *TuningConfig) { c.KVOffload = embeddedBoolPtr(true) },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := minimalValidConfig()
			mutate(&cfg.EmbeddedLLM.Tuning)
			if err := validate(cfg); err != nil {
				t.Fatalf("validate() rejected a legal embedded_llm.tuning section: %v", err)
			}
		})
	}
}

// TestEmbeddedLLMTuningValidatedWhileInert is the auto_unload.minutes rule
// applied to the tuning section: a knob is validated even when nothing reads it
// yet, so flipping the switch that arms it can never activate a dead budget.
func TestEmbeddedLLMTuningValidatedWhileInert(t *testing.T) {
	inert := map[string]func(*EmbeddedLLMConfig){
		// The model is not installed, so no plan is ever built from this.
		"not installed": func(c *EmbeddedLLMConfig) {
			c.Installed = false
			c.Tuning.KVCacheType = embeddedStrPtr("q5_0")
		},
		// fit is off, so fit_min_context and fit_target_mib are never emitted.
		"fit disabled": func(c *EmbeddedLLMConfig) {
			c.Tuning.Fit = embeddedBoolPtr(false)
			c.Tuning.FitMinContext = embeddedIntPtr(0)
			c.Tuning.FitTargetMiB = embeddedIntPtr(-2048)
		},
		// The context is pinned exactly, so the fit floor is never consulted.
		"exact context": func(c *EmbeddedLLMConfig) {
			c.Tuning.Context.Mode = embeddedStrPtr(EmbeddedLLMContextExact)
			c.Tuning.Context.Tokens = embeddedIntPtr(8192)
			c.Tuning.FitMinContext = embeddedIntPtr(-1)
		},
		// The offload is pinned to "all", so the layer count is never read.
		"offload all": func(c *EmbeddedLLMConfig) {
			c.Tuning.Offload.Mode = embeddedStrPtr(EmbeddedLLMOffloadAll)
			c.Tuning.Offload.Layers = embeddedIntPtr(-9)
		},
		// The KV cache stays in host RAM, so its precision is moot.
		"kv not offloaded": func(c *EmbeddedLLMConfig) {
			c.Tuning.KVOffload = embeddedBoolPtr(false)
			c.Tuning.KVCacheType = embeddedStrPtr("iq4_nl")
		},
	}

	for name, mutate := range inert {
		t.Run(name, func(t *testing.T) {
			cfg := minimalValidConfig()
			mutate(&cfg.EmbeddedLLM)
			if err := validate(cfg); err == nil {
				t.Fatalf("validate() accepted an inert-but-invalid tuning knob: %+v",
					cfg.EmbeddedLLM.Tuning)
			} else if !strings.Contains(err.Error(), "embedded_llm.tuning.") {
				t.Errorf("error %q does not name the offending key", err)
			}
		})
	}
}

// TestEmbeddedLLMTuningToTuning pins the translation onto the planner's own
// vocabulary, knob by knob.
func TestEmbeddedLLMTuningToTuning(t *testing.T) {
	got, err := fullySpecifiedTuning().ToTuning()
	if err != nil {
		t.Fatalf("ToTuning(): %v", err)
	}

	if got.Context.Mode != embeddedllm.ContextExact || got.Context.Tokens != 49152 {
		t.Errorf("context = %+v, want {ContextExact 49152}", got.Context)
	}
	if got.KVType != embeddedllm.KVTypeQ8_0 {
		t.Errorf("kv_cache_type = %q, want %q", got.KVType, embeddedllm.KVTypeQ8_0)
	}
	if got.Offload.Mode != embeddedllm.OffloadLayers || got.Offload.Layers != 40 {
		t.Errorf("offload = %+v, want {OffloadLayers 40}", got.Offload)
	}
	if got.FitEnabled == nil || *got.FitEnabled {
		t.Errorf("fit = %v, want an explicit false", got.FitEnabled)
	}
	if got.FitTargetMiB == nil || *got.FitTargetMiB != 2048 {
		t.Errorf("fit_target_mib = %v, want 2048", got.FitTargetMiB)
	}
	if got.FitMinContext == nil || *got.FitMinContext != 32768 {
		t.Errorf("fit_min_context = %v, want 32768", got.FitMinContext)
	}
	if got.KVOffload == nil || *got.KVOffload {
		t.Errorf("kv_offload = %v, want an explicit false", got.KVOffload)
	}
	if got.MMProjOffload == nil || *got.MMProjOffload {
		t.Errorf("mmproj_offload = %v, want an explicit false", got.MMProjOffload)
	}
	if got.Packing != embeddedllm.PackingPTQ1_0 {
		t.Errorf("packing = %q, want %q", got.Packing, embeddedllm.PackingPTQ1_0)
	}
	if got.Parallel == nil || *got.Parallel != 2 {
		t.Errorf("parallel = %v, want 2", got.Parallel)
	}
	if got.CacheRAMMiB == nil || *got.CacheRAMMiB != 512 {
		t.Errorf("cache_ram_mib = %v, want 512", got.CacheRAMMiB)
	}
	if got.HostReserveGiB == nil || *got.HostReserveGiB != 6.5 {
		t.Errorf("host_reserve_gib = %v, want 6.5", got.HostReserveGiB)
	}

	// The unset section translates to the planner's zero value, which plan.go
	// documents as the all-Auto plan.
	fromZero, err := TuningConfig{}.ToTuning()
	if err != nil {
		t.Fatalf("zero ToTuning(): %v", err)
	}
	if !reflect.DeepEqual(fromZero, embeddedllm.Tuning{}) {
		t.Errorf("the unset section translated to %+v, want the planner's zero value", fromZero)
	}

	// The two knobs this surface deliberately does not expose stay at their own
	// zero sentinels, so an operator cannot reach them through config.yaml.
	if len(fromZero.Devices) != 0 || fromZero.SplitMode != embeddedllm.SplitModeAuto {
		t.Errorf("devices/split_mode = %v/%q, want them unreachable from config",
			fromZero.Devices, fromZero.SplitMode)
	}
}

// TestEmbeddedLLMTuningToTuningClonesPointers proves the translation cannot
// hand a caller a handle back into the live config: mutating the returned
// Tuning must not change what config.yaml would be saved as.
func TestEmbeddedLLMTuningToTuningClonesPointers(t *testing.T) {
	src := TuningConfig{
		Fit:            embeddedBoolPtr(true),
		FitTargetMiB:   embeddedIntPtr(1024),
		FitMinContext:  embeddedIntPtr(65536),
		KVOffload:      embeddedBoolPtr(true),
		MMProjOffload:  embeddedBoolPtr(true),
		Parallel:       embeddedIntPtr(1),
		CacheRAMMiB:    embeddedIntPtr(256),
		HostReserveGiB: embeddedFloatPtr(4),
	}
	got, err := src.ToTuning()
	if err != nil {
		t.Fatalf("ToTuning(): %v", err)
	}

	*got.FitEnabled = false
	*got.FitTargetMiB = 1
	*got.FitMinContext = 1
	*got.KVOffload = false
	*got.MMProjOffload = false
	*got.Parallel = 99
	*got.CacheRAMMiB = 1
	*got.HostReserveGiB = 1

	if !reflect.DeepEqual(src, TuningConfig{
		Fit:            embeddedBoolPtr(true),
		FitTargetMiB:   embeddedIntPtr(1024),
		FitMinContext:  embeddedIntPtr(65536),
		KVOffload:      embeddedBoolPtr(true),
		MMProjOffload:  embeddedBoolPtr(true),
		Parallel:       embeddedIntPtr(1),
		CacheRAMMiB:    embeddedIntPtr(256),
		HostReserveGiB: embeddedFloatPtr(4),
	}) {
		t.Errorf("mutating the translated Tuning reached back into the config: %+v", src)
	}
}

// TestEmbeddedLLMTuningEnumsCoverCoreVocabulary keeps the two closed sets this
// file transcribes (context modes, offload modes) honest against core's enums,
// and the two it READS from core (KV precisions, packings) non-empty. Without
// it, a new OffloadMode in plan.go would be silently unreachable from config.
func TestEmbeddedLLMTuningEnumsCoverCoreVocabulary(t *testing.T) {
	// Every non-Auto offload mode must be reachable from a YAML spelling.
	reachable := map[embeddedllm.OffloadMode]bool{}
	for _, spelling := range embeddedLLMOffloadModes {
		cfg := TuningConfig{Offload: EmbeddedLLMOffloadConfig{
			Mode:   embeddedStrPtr(spelling),
			Layers: embeddedIntPtr(1),
		}}
		got, err := cfg.ToTuning()
		if err != nil {
			t.Fatalf("offload.mode %q: %v", spelling, err)
		}
		reachable[got.Offload.Mode] = true
	}
	for _, mode := range []embeddedllm.OffloadMode{
		embeddedllm.OffloadAll, embeddedllm.OffloadCPU, embeddedllm.OffloadLayers,
	} {
		if !reachable[mode] {
			t.Errorf("embeddedllm.OffloadMode %d is not reachable from embedded_llm.tuning.offload.mode", mode)
		}
	}

	// And the exact-context mode.
	exact, err := TuningConfig{Context: EmbeddedLLMContextConfig{
		Mode: embeddedStrPtr(EmbeddedLLMContextExact), Tokens: embeddedIntPtr(1024),
	}}.ToTuning()
	if err != nil {
		t.Fatalf("context.mode exact: %v", err)
	}
	if exact.Context.Mode != embeddedllm.ContextExact {
		t.Errorf("context.mode %q translated to %d, want ContextExact",
			EmbeddedLLMContextExact, exact.Context.Mode)
	}

	if len(tuningKVChoices()) == 0 {
		t.Error("no KV precision choices were read from core")
	}
	if len(tuningPackingChoices()) == 0 {
		t.Error("no packing choices were read from core")
	}
}

// TestEmbeddedLLMTuningCeilingMatchesCore pins the context ceiling to core's
// own figure rather than to a transcription of it: validate() must reject
// exactly what plan.go's planTargetContext would reject, and the two read the
// same pinned profile.
func TestEmbeddedLLMTuningCeilingMatchesCore(t *testing.T) {
	ceiling, err := EmbeddedLLMMaxContextTokens()
	if err != nil {
		t.Fatalf("EmbeddedLLMMaxContextTokens(): %v", err)
	}
	profile, err := embeddedllm.PinnedMemoryProfile()
	if err != nil {
		t.Fatalf("PinnedMemoryProfile(): %v", err)
	}
	if ceiling != profile.MaxContext {
		t.Errorf("the context ceiling = %d, want core's %d", ceiling, profile.MaxContext)
	}
	if ceiling != 262144 {
		t.Errorf("the pinned model's training context = %d, want 262144", ceiling)
	}
	// The boundary is inclusive at the top and exclusive one step past it.
	at := TuningConfig{Context: EmbeddedLLMContextConfig{
		Mode: embeddedStrPtr(EmbeddedLLMContextExact), Tokens: embeddedIntPtr(ceiling),
	}}
	if _, err := at.ToTuning(); err != nil {
		t.Errorf("a context of exactly %d must be accepted: %v", ceiling, err)
	}
	over := TuningConfig{Context: EmbeddedLLMContextConfig{
		Mode: embeddedStrPtr(EmbeddedLLMContextExact), Tokens: embeddedIntPtr(ceiling + 1),
	}}
	if _, err := over.ToTuning(); err == nil {
		t.Errorf("a context of %d must be rejected", ceiling+1)
	}
}

// TestEmbeddedLLMTuningLoadFromYAML drives the real load path: a hand-written
// config.yaml with a tuning section must arrive intact, and a bad one must be
// reported as a load error naming the key rather than silently dropped.
func TestEmbeddedLLMTuningLoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Written by Save, so the file is the real persisted shape rather than a
	// hand-appended block that could collide with the section ApplyDefaults
	// already seeds.
	good := minimalValidConfig()
	good.EmbeddedLLM.Tuning = TuningConfig{
		Context: EmbeddedLLMContextConfig{
			Mode:   embeddedStrPtr(EmbeddedLLMContextExact),
			Tokens: embeddedIntPtr(16384),
		},
		KVCacheType:    embeddedStrPtr("q4_0"),
		Offload:        EmbeddedLLMOffloadConfig{Mode: embeddedStrPtr(EmbeddedLLMOffloadAll)},
		Fit:            embeddedBoolPtr(false),
		Parallel:       embeddedIntPtr(1),
		CacheRAMMiB:    embeddedIntPtr(0),
		HostReserveGiB: embeddedFloatPtr(8),
	}
	if err := Save(good, path); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	result, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult(): %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Fatalf("a legal tuning section produced load errors: %v", result.LoadErrors)
	}
	tuning := result.Config.EmbeddedLLM.Tuning
	if tuning.Context.Mode == nil || *tuning.Context.Mode != EmbeddedLLMContextExact {
		t.Errorf("context.mode = %v, want exact", tuning.Context.Mode)
	}
	if tuning.Context.Tokens == nil || *tuning.Context.Tokens != 16384 {
		t.Errorf("context.tokens = %v, want 16384", tuning.Context.Tokens)
	}
	if tuning.KVCacheType == nil || *tuning.KVCacheType != "q4_0" {
		t.Errorf("kv_cache_type = %v, want q4_0", tuning.KVCacheType)
	}
	if tuning.Offload.Mode == nil || *tuning.Offload.Mode != EmbeddedLLMOffloadAll {
		t.Errorf("offload.mode = %v, want all", tuning.Offload.Mode)
	}
	if tuning.Fit == nil || *tuning.Fit {
		t.Errorf("fit = %v, want an explicit false", tuning.Fit)
	}
	if tuning.CacheRAMMiB == nil || *tuning.CacheRAMMiB != 0 {
		t.Errorf("cache_ram_mib = %v, want an explicit 0", tuning.CacheRAMMiB)
	}
	if tuning.HostReserveGiB == nil || *tuning.HostReserveGiB != 8 {
		t.Errorf("host_reserve_gib = %v, want 8", tuning.HostReserveGiB)
	}
	// A knob the file never mentioned stays unset, not auto-filled.
	if tuning.Parallel == nil || *tuning.Parallel != 1 {
		t.Errorf("parallel = %v, want the file's 1", tuning.Parallel)
	}
	if tuning.Packing != nil {
		t.Errorf("packing = %v, want it left unset", tuning.Packing)
	}

	// And the same file with one bad knob is refused, with the key named. Save
	// does not validate, so the corrupt value really does reach the load path.
	bad := minimalValidConfig()
	bad.EmbeddedLLM.Tuning.KVCacheType = embeddedStrPtr("q5_0")
	if err := Save(bad, path); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if _, err := LoadWithResult(path); err == nil {
		t.Fatal("LoadWithResult() accepted kv_cache_type: q5_0")
	} else if !strings.Contains(err.Error(), "embedded_llm.tuning.kv_cache_type") {
		t.Errorf("load error %q does not name the offending key", err)
	}
}

// TestEmbeddedLLMTuningKeepsTheImportDirection asserts the layering the mapping
// depends on: backend/config may import core/embeddedllm (backend sits above
// core), and core/embeddedllm must never import back — which is also why the
// translation lives HERE and not in the planner. The compiler already forbids
// the cycle (this package's own import of core would not build), so the scan is
// the readable form of the guarantee rather than the only one.
func TestEmbeddedLLMTuningKeepsTheImportDirection(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "core", "embeddedllm"))
	if err != nil {
		t.Fatalf("read core/embeddedllm: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		scanned++
		file, err := parser.ParseFile(fset, filepath.Join("..", "..", "core", "embeddedllm", entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "github.com/v0lka/c0wrk/backend") {
				t.Errorf("core/embeddedllm/%s imports %q: core must not import backend", entry.Name(), path)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no core/embeddedllm sources were scanned — the assertion is vacuous")
	}
}
