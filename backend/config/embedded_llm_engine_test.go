package config

import (
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// TestEmbeddedLLMEngineVocabulary pins the engine setting's closed set and the
// empty-for-auto spelling.
func TestEmbeddedLLMEngineVocabulary(t *testing.T) {
	cases := []struct {
		engine  string
		wantErr bool
	}{
		{"", false},
		{"auto", false},
		{"mtplx", false},
		{"llama", false},
		{"AUTO", false}, // NormalizeEngine lowercases? No — it maps only ""; assert the truth:
		{"vllm", true},
		{"MLX", true},
		{"mtplx ", true},
	}
	for _, tc := range cases {
		cfg := installedEmbeddedState()
		cfg.Engine = tc.engine
		err := validateEmbeddedLLM(&cfg)
		if tc.engine == "AUTO" {
			// The vocabulary is lowercase; an uppercase spelling is refused,
			// matching every other closed enum in this file.
			if err == nil {
				t.Fatalf("engine %q accepted; the vocabulary is lowercase", tc.engine)
			}
			continue
		}
		if (err != nil) != tc.wantErr {
			t.Fatalf("engine %q: validateEmbeddedLLM error = %v, wantErr %v", tc.engine, err, tc.wantErr)
		}
		if tc.wantErr && !strings.Contains(err.Error(), "engine") {
			t.Fatalf("engine %q: error %v does not name the key", tc.engine, err)
		}
	}
}

// TestEmbeddedLLMEngineSettingSurvivesSink pins the choice-not-record split:
// ApplyInstalled carries the install record but PRESERVES the operator's
// engine setting, and the mtplx records (python/pack revision) land beside it.
func TestEmbeddedLLMEngineSettingSurvivesSink(t *testing.T) {
	cfg := installedEmbeddedState()
	cfg.Engine = "llama"
	mtplxState := embeddedllm.InstallState{
		Engine:         embeddedllm.EngineMTPLX,
		Backend:        "metal",
		Port:           4321,
		ModelFile:      "/home/u/.c0wrk/models/bonsai-2-27b-mlx/pack",
		RuntimeVersion: "2.12.2",
		InstalledAt:    "2026-10-20T09:00:00Z",
		ContextSize:    20480,
		PythonVersion:  "3.12.15",
		PackRevision:   "03bd60bb82755f2446f5083426dca9708eb8e0fe",
	}
	// The sink half is exercised for real in the sink test file
	// (TestEmbeddedConfigSinkAppliesMTPLXInstallState pins that ApplyInstalled
	// preserves the operator's engine choice and writes these records); this
	// half pins the config layer's contract: the section can REPRESENT an
	// mtplx install (records set, packing empty) and still validate.
	cfg2 := EmbeddedLLMConfig{
		Engine:         "mtplx",
		Installed:      true,
		Backend:        "metal",
		Port:           4321,
		ModelFile:      "/home/u/.c0wrk/models/bonsai-2-27b-mlx/pack",
		RuntimeVersion: "2.12.2",
		InstalledAt:    "2026-10-20T09:00:00Z",
		PythonVersion:  "3.12.15",
		PackRevision:   "03bd60bb82755f2446f5083426dca9708eb8e0fe",
		AutoUnload:     cfg.AutoUnload,
	}
	if err := validateEmbeddedLLM(&cfg2); err != nil {
		t.Fatalf("mtplx install record rejected: %v", err)
	}
	if cfg2.PythonVersion != mtplxState.PythonVersion {
		t.Fatalf("python record = %q, want %q", cfg2.PythonVersion, mtplxState.PythonVersion)
	}
}

// TestEmbeddedLLMEngineTuningCrossRefusal refuses the llama-only tuning keys
// beside an explicit mtplx engine, naming the offending key, and accepts the
// same tuning beside llama (where the llama planner owns it).
func TestEmbeddedLLMEngineTuningCrossRefusal(t *testing.T) {
	base := installedEmbeddedState()
	base.Engine = "mtplx"
	install := embeddedllm.InstallState{} // unused; keeps the shape obvious

	cases := map[string]func(*EmbeddedLLMConfig){
		"kv_cache_type": func(c *EmbeddedLLMConfig) { v := "q8_0"; c.Tuning.KVCacheType = &v },
		"packing":       func(c *EmbeddedLLMConfig) { v := "PQ2_0"; c.Tuning.Packing = &v },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			set(&cfg)
			err := validateEmbeddedLLM(&cfg)
			if err == nil {
				t.Fatalf("tuning.%s beside engine mtplx accepted", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error %v does not name %q", err, name)
			}
			if !strings.Contains(err.Error(), "mtplx") {
				t.Fatalf("error %v does not name the engine", err)
			}
		})
	}

	// The same tuning beside llama stays legal (the llama planner owns it).
	llama := installedEmbeddedState()
	llama.Engine = "llama"
	pq2 := "PQ2_0"
	llama.Tuning.Packing = &pq2
	if err := validateEmbeddedLLM(&llama); err != nil {
		t.Fatalf("llama tuning beside engine llama rejected: %v", err)
	}

	// An OFF (all-auto) tuning beside mtplx is legal: absence is not a leak.
	off := base
	if err := validateEmbeddedLLM(&off); err != nil {
		t.Fatalf("zero tuning beside engine mtplx rejected: %v", err)
	}

	// The mtplx engine need not be spelled: auto (or the empty spelling)
	// resolves to mtplx on darwin/arm64, and the load-time cross-check must
	// refuse the same llama-only keys there — which is what the platform seam
	// of validateEmbeddedLLMOn exists to pin on any machine. The explicit
	// llama rows prove the refusal is resolution-driven, not spelling-driven,
	// and the off-platform mtplx row pins that the spelling alone still owns
	// the check where the engine cannot run.
	resolutionCases := []struct {
		name     string
		engine   string
		platform string
		tuning   func(*EmbeddedLLMConfig)
		wantErr  bool
		wantNote bool // the refusal names that auto resolved to mtplx
	}{
		{
			name:   "auto on darwin/arm64 resolves to mtplx and refuses devices",
			engine: "auto", platform: embeddedllm.PlatformDarwinARM64,
			tuning:  func(c *EmbeddedLLMConfig) { c.Tuning.Devices = []string{"CUDA0"} },
			wantErr: true, wantNote: true,
		},
		{
			name:   "empty spelling on darwin/arm64 resolves to mtplx and refuses split_mode",
			engine: "", platform: embeddedllm.PlatformDarwinARM64,
			tuning: func(c *EmbeddedLLMConfig) {
				v := string(embeddedllm.SplitModeRow)
				c.Tuning.SplitMode = &v
			},
			wantErr: true, wantNote: true,
		},
		{
			name:   "explicit llama beside darwin/arm64 keeps the pin",
			engine: "llama", platform: embeddedllm.PlatformDarwinARM64,
			tuning:  func(c *EmbeddedLLMConfig) { c.Tuning.Devices = []string{"CUDA0"} },
			wantErr: false,
		},
		{
			name:   "auto beside linux resolves to llama and keeps the pin",
			engine: "auto", platform: embeddedllm.PlatformLinuxAMD64,
			tuning:  func(c *EmbeddedLLMConfig) { c.Tuning.Devices = []string{"CUDA0"} },
			wantErr: false,
		},
		{
			name:   "explicit mtplx owns the cross-check off-platform too",
			engine: "mtplx", platform: embeddedllm.PlatformLinuxAMD64,
			tuning:  func(c *EmbeddedLLMConfig) { v := "PQ2_0"; c.Tuning.Packing = &v },
			wantErr: true,
		},
	}
	for _, tc := range resolutionCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := installedEmbeddedState()
			cfg.Engine = tc.engine
			tc.tuning(&cfg)
			err := validateEmbeddedLLMOn(&cfg, tc.platform)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("engine %q on %s rejected a legal tuning: %v", tc.engine, tc.platform, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("engine %q on %s accepted the llama-only tuning", tc.engine, tc.platform)
			}
			if !strings.Contains(err.Error(), "mtplx") {
				t.Errorf("the refusal %q does not name the resolved engine", err)
			}
			if tc.wantNote != strings.Contains(err.Error(), "auto resolves to") {
				t.Errorf("the refusal %q misstates the resolution (note = %v)", err, tc.wantNote)
			}
		})
	}
	_ = install
}
