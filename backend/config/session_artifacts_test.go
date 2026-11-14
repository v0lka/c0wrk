// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// writeSessionArtifactsConfig writes a minimal-but-valid config carrying the
// given session_artifacts YAML fragment and returns its path.
func writeSessionArtifactsConfig(t *testing.T, fragment string) string {
	t.Helper()
	content := `
llm:
  default_model: anthropic/claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
` + fragment
	return writeTestConfig(t, content)
}

// TestSessionArtifactsDefaults checks that an absent section resolves to the
// compiled-in defaults (zstd/25) with no load warnings.
func TestSessionArtifactsDefaults(t *testing.T) {
	configPath := writeSessionArtifactsConfig(t, "")

	result, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Errorf("expected no load warnings, got %v", result.LoadErrors)
	}
	got := result.Config.SessionArtifacts.Compression
	if got.Algorithm != DefaultSessionCompressionAlgorithm {
		t.Errorf("algorithm = %q, want default %q", got.Algorithm, DefaultSessionCompressionAlgorithm)
	}
	if got.ZstdWindowLog != DefaultSessionZstdWindowLog {
		t.Errorf("zstd_window_log = %d, want default %d", got.ZstdWindowLog, DefaultSessionZstdWindowLog)
	}
}

// TestSessionArtifactsRoundtrip checks that explicit values survive a full
// Save → Load cycle, that the unset section materializes the defaults in the
// persisted shape, and that both roundtrips are warning-free.
func TestSessionArtifactsRoundtrip(t *testing.T) {
	// Defaults materialize explicitly on the first save.
	path := writeSessionArtifactsConfig(t, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save(defaults) failed: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(saved defaults) failed: %v", err)
	}
	if got := reloaded.SessionArtifacts.Compression; got.Algorithm != SessionCompressionZstd || got.ZstdWindowLog != 25 {
		t.Errorf("persisted defaults = %q/%d, want zstd/25", got.Algorithm, got.ZstdWindowLog)
	}

	// Explicit custom values survive verbatim.
	cfg.SessionArtifacts.Compression.Algorithm = SessionCompressionGzip
	cfg.SessionArtifacts.Compression.ZstdWindowLog = 20
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save(custom) failed: %v", err)
	}
	reloaded, err = Load(path)
	if err != nil {
		t.Fatalf("Load(saved custom) failed: %v", err)
	}
	if got := reloaded.SessionArtifacts.Compression; got.Algorithm != SessionCompressionGzip || got.ZstdWindowLog != 20 {
		t.Errorf("roundtrip = %q/%d, want gzip/20", got.Algorithm, got.ZstdWindowLog)
	}
	result, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult(saved custom) failed: %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Errorf("expected no load warnings, got %v", result.LoadErrors)
	}
}

// TestSessionArtifactsInvalidAlgorithm: an unrecognized algorithm (including
// the deliberately excluded xz and a case-mismatched value — the enum is
// case-sensitive, mirroring the MCP modes) draws a load warning naming the
// fallback and resets to zstd; the other knob survives.
func TestSessionArtifactsInvalidAlgorithm(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fragment  string
		wantInMsg string
	}{
		{"xz excluded", `
session_artifacts:
  compression:
    algorithm: "xz"
    zstd_window_log: 20`, `"xz"`},
		{"unknown", `
session_artifacts:
  compression:
    algorithm: "brotli"
    zstd_window_log: 20`, `"brotli"`},
		{"case mismatch", `
session_artifacts:
  compression:
    algorithm: "GZIP"
    zstd_window_log: 20`, `"GZIP"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := writeSessionArtifactsConfig(t, tc.fragment)

			result, err := LoadWithResult(configPath)
			if err != nil {
				t.Fatalf("LoadWithResult() failed: %v", err)
			}
			if len(result.LoadErrors) != 1 {
				t.Fatalf("expected exactly 1 load warning, got %v", result.LoadErrors)
			}
			warning := result.LoadErrors[0]
			if !strings.Contains(warning, "session_artifacts.compression.algorithm") {
				t.Errorf("warning %q does not name the knob", warning)
			}
			if !strings.Contains(warning, tc.wantInMsg) {
				t.Errorf("warning %q does not name the offending value %s", warning, tc.wantInMsg)
			}
			got := result.Config.SessionArtifacts.Compression
			if got.Algorithm != DefaultSessionCompressionAlgorithm {
				t.Errorf("algorithm = %q, want reset to %q", got.Algorithm, DefaultSessionCompressionAlgorithm)
			}
			if got.ZstdWindowLog != 20 {
				t.Errorf("zstd_window_log = %d, want the authored 20 preserved", got.ZstdWindowLog)
			}

			// The healed shape re-persists warning-free.
			if err := Save(result.Config, configPath); err != nil {
				t.Fatalf("Save(healed) failed: %v", err)
			}
			clean, err := LoadWithResult(configPath)
			if err != nil {
				t.Fatalf("Load(healed) failed: %v", err)
			}
			if len(clean.LoadErrors) != 0 {
				t.Errorf("healed config still warns: %v", clean.LoadErrors)
			}
		})
	}
}

// TestSessionArtifactsInvalidWindowWarnedAndReset: an out-of-range
// zstd_window_log (below 10, above 27, negative) draws a load warning and
// resets to 25; an invalid algorithm survives untouched — one warning +
// reset per knob.
func TestSessionArtifactsInvalidWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window int
	}{
		{"below floor", 9},
		{"above ceiling", 28},
		{"negative", -5},
		{"far out", 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fragment := `
session_artifacts:
  compression:
    algorithm: "gzip"
    zstd_window_log: ` + strconv.Itoa(tc.window)
			configPath := writeSessionArtifactsConfig(t, fragment)

			result, err := LoadWithResult(configPath)
			if err != nil {
				t.Fatalf("LoadWithResult() failed: %v", err)
			}
			if len(result.LoadErrors) != 1 {
				t.Fatalf("expected exactly 1 load warning, got %v", result.LoadErrors)
			}
			warning := result.LoadErrors[0]
			if !strings.Contains(warning, "session_artifacts.compression.zstd_window_log") {
				t.Errorf("warning %q does not name the knob", warning)
			}
			got := result.Config.SessionArtifacts.Compression
			if got.ZstdWindowLog != DefaultSessionZstdWindowLog {
				t.Errorf("zstd_window_log = %d, want reset to %d", got.ZstdWindowLog, DefaultSessionZstdWindowLog)
			}
			if got.Algorithm != SessionCompressionGzip {
				t.Errorf("algorithm = %q, want the authored gzip preserved", got.Algorithm)
			}
		})
	}
}

// TestSessionArtifactsWindowBoundaries: both in-range extremes load
// warning-free and survive a roundtrip.
func TestSessionArtifactsWindowBoundaries(t *testing.T) {
	for _, window := range []int{MinSessionZstdWindowLog, MaxSessionZstdWindowLog} {
		fragment := `
session_artifacts:
  compression:
    algorithm: "zstd"
    zstd_window_log: ` + strconv.Itoa(window)
		configPath := writeSessionArtifactsConfig(t, fragment)

		result, err := LoadWithResult(configPath)
		if err != nil {
			t.Fatalf("LoadWithResult(%d) failed: %v", window, err)
		}
		if len(result.LoadErrors) != 0 {
			t.Errorf("window %d drew warnings: %v", window, result.LoadErrors)
		}
		if got := result.Config.SessionArtifacts.Compression.ZstdWindowLog; got != window {
			t.Errorf("zstd_window_log = %d, want %d", got, window)
		}
	}
}

// TestSessionArtifactsWindowValidatedRegardlessOfAlgorithm pins the
// canonical-shape rule: an out-of-range window warns and resets even under
// algorithm "none" where the knob is inert.
func TestSessionArtifactsWindowValidatedRegardlessOfAlgorithm(t *testing.T) {
	fragment := `
session_artifacts:
  compression:
    algorithm: "none"
    zstd_window_log: 5`
	configPath := writeSessionArtifactsConfig(t, fragment)

	result, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	if len(result.LoadErrors) != 1 {
		t.Fatalf("expected exactly 1 load warning, got %v", result.LoadErrors)
	}
	got := result.Config.SessionArtifacts.Compression
	if got.Algorithm != SessionCompressionNone {
		t.Errorf("algorithm = %q, want the authored none preserved", got.Algorithm)
	}
	if got.ZstdWindowLog != DefaultSessionZstdWindowLog {
		t.Errorf("zstd_window_log = %d, want reset to %d even though inert", got.ZstdWindowLog, DefaultSessionZstdWindowLog)
	}
}

// TestSessionArtifactsSaveGate: the save path is fail-closed — an invalid
// algorithm or an out-of-range window (settable only by an in-memory
// mutation; the load path heals first) refuses to persist, while unset
// values pass (the next load resolves them to the defaults).
func TestSessionArtifactsSaveGate(t *testing.T) {
	configPath := writeSessionArtifactsConfig(t, "")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	for _, tc := range []struct {
		name      string
		mutate    func(*SessionCompressionConfig)
		wantInErr string
	}{
		{"invalid algorithm", func(c *SessionCompressionConfig) { c.Algorithm = "xz" }, "compression.algorithm"},
		{"window below floor", func(c *SessionCompressionConfig) { c.ZstdWindowLog = 9 }, "out of range"},
		{"window above ceiling", func(c *SessionCompressionConfig) { c.ZstdWindowLog = 28 }, "out of range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := cfg.SessionArtifacts.Compression
			t.Cleanup(func() { cfg.SessionArtifacts.Compression = saved })

			tc.mutate(&cfg.SessionArtifacts.Compression)
			err := Save(cfg, configPath)
			if err == nil {
				t.Fatal("Save() succeeded, want refusal")
			}
			if !strings.Contains(err.Error(), "refusing to save config") {
				t.Errorf("error %q does not carry the refusal prefix", err)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q does not name %s", err, tc.wantInErr)
			}
			// The refusal must not have partially written the file.
			if _, statErr := os.Stat(configPath + ".tmp"); statErr == nil {
				t.Error("a staging file survived the refused save")
			}
		})
	}

	// Unset values pass the gate (the load path resolves them).
	cfg.SessionArtifacts.Compression = SessionCompressionConfig{}
	if err := Save(cfg, configPath); err != nil {
		t.Errorf("Save(unset section) failed: %v", err)
	}
}

// TestSessionArtifactsExportedRules: the exported validators enforce the
// same enum/range the pipeline does, so a runtime mutator cannot drift.
func TestSessionArtifactsExportedRules(t *testing.T) {
	for _, algo := range []string{SessionCompressionNone, SessionCompressionGzip, SessionCompressionZstd} {
		if !ValidSessionCompressionAlgorithm(algo) {
			t.Errorf("ValidSessionCompressionAlgorithm(%q) = false, want true", algo)
		}
	}
	for _, algo := range []string{"", "xz", "ZSTD", "zstd "} {
		if ValidSessionCompressionAlgorithm(algo) {
			t.Errorf("ValidSessionCompressionAlgorithm(%q) = true, want false", algo)
		}
	}
	for _, window := range []int{MinSessionZstdWindowLog, DefaultSessionZstdWindowLog, MaxSessionZstdWindowLog} {
		if err := ValidateSessionZstdWindowLog(window); err != nil {
			t.Errorf("ValidateSessionZstdWindowLog(%d) = %v, want nil", window, err)
		}
	}
	for _, window := range []int{MinSessionZstdWindowLog - 1, MaxSessionZstdWindowLog + 1, 0, -1} {
		if err := ValidateSessionZstdWindowLog(window); err == nil {
			t.Errorf("ValidateSessionZstdWindowLog(%d) = nil, want an error", window)
		}
	}
}

// TestExampleDocumentsSessionArtifacts pins the documented block against the
// compiled-in defaults: the example's compression section must parse to
// exactly zstd/25 through the real pipeline (TestExampleConfigReflectsGroupsSchema
// already proves the file is otherwise warning-free).
func TestExampleDocumentsSessionArtifacts(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	if !strings.Contains(string(data), "session_artifacts:") {
		t.Fatal("config.example.yaml no longer documents the session_artifacts section")
	}
	if !strings.Contains(string(data), "zstd_window_log") {
		t.Fatal("config.example.yaml no longer documents zstd_window_log")
	}
	if !strings.Contains(string(data), "ratio x112–x461") {
		t.Error("config.example.yaml no longer carries the measured zstd ratio")
	}
	if !strings.Contains(string(data), "no streaming\n#   Flush") {
		t.Error("config.example.yaml no longer documents why xz is excluded")
	}
}
