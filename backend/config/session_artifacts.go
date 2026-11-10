// SPDX-License-Identifier: Apache-2.0
//
// session_artifacts.go — the `session_artifacts` config section (ADR-116):
// compression of the append-only session artifacts (the per-session step
// dumps and logs/session_<sessionID>.log). One algorithm knob — none | gzip | zstd; xz
// is deliberately excluded because the pure-Go xz writer has no streaming
// Flush, so a live tail of a compressed artifact would lag a whole block
// behind the writer — plus the zstd window (zstd_window_log 10..27, default
// 25). Load-time validity is FAIL-SOFT (warned + reset in place, mirroring
// normalizeWebAccess, so the persisted shape is always valid); the save path
// FAIL-CLOSED (validateSessionArtifactsForSave) so an in-memory mutation can
// never persist an unusable section. See ADR-116.

package config

import (
	"fmt"
)

// Session artifact compression algorithms — the closed enum behind
// `session_artifacts.compression.algorithm` (ADR-116).
const (
	// SessionCompressionNone writes records verbatim (byte-identical to
	// pre-compression builds).
	SessionCompressionNone = "none"
	// SessionCompressionGzip uses the stdlib compress/gzip writer (default
	// level). Measured x3.0–x3.5 on live session dumps — the 32 KiB LZ77
	// window cannot reach the cross-record dedup (each request resends the
	// growing conversation context).
	SessionCompressionGzip = "gzip"
	// SessionCompressionZstd uses klauspost/compress/zstd — the default:
	// measured x112–x461 on live session dumps (332 MiB step dump →
	// 755 KiB, 242 ms encode), an order of magnitude past gzip.
	SessionCompressionZstd = "zstd"
)

// Session artifact compression defaults and bounds (ADR-116).
const (
	// DefaultSessionCompressionAlgorithm — zstd, the measured best ratio.
	DefaultSessionCompressionAlgorithm = SessionCompressionZstd
	// DefaultSessionZstdWindowLog — a 32 MiB window (2^25): covers the
	// observed dedup distances with memory bounded at ~2× the window
	// (encoder history) plus hash tables.
	DefaultSessionZstdWindowLog = 25
	// MinSessionZstdWindowLog — the zstd format floor (1 KiB window;
	// klauspost/compress MinWindowSize = 1<<10).
	MinSessionZstdWindowLog = 10
	// MaxSessionZstdWindowLog — the operator ceiling: a 128 MiB window,
	// 4× below the library max (MaxWindowSize = 1<<29) so a typo cannot
	// allocate half a GiB per stream.
	MaxSessionZstdWindowLog = 27
)

// ValidSessionCompressionAlgorithm reports whether raw is a recognized
// algorithm. Empty is deliberately NOT valid — it is the "unset, resolve to
// the default" signal (silently seeded by ApplyDefaults; unrecognized
// non-empty values are the load path's warning branch). The closed-enum
// contract shared by every consumer: ADR-116.
func ValidSessionCompressionAlgorithm(raw string) bool {
	switch raw {
	case SessionCompressionNone, SessionCompressionGzip, SessionCompressionZstd:
		return true
	}
	return false
}

// SessionArtifactsConfig is the top-level `session_artifacts:` section
// (ADR-116): operator knobs for the session artifact store. WHICH artifacts
// are compressed is not operator-configurable — the scope is fixed by the
// artifact writers.
type SessionArtifactsConfig struct {
	// Compression configures how the append-only session artifacts (step
	// dumps, logs/session_<sessionID>.log) are written to disk. See
	// SessionCompressionConfig.
	Compression SessionCompressionConfig `yaml:"compression"`
}

// SessionCompressionConfig is the `session_artifacts.compression:`
// subsection. The zstd window is inert for non-zstd algorithms; both knobs
// are validated regardless of the algorithm so the persisted shape stays
// canonical (flipping the algorithm later cannot surface a stale invalid
// value).
type SessionCompressionConfig struct {
	// Algorithm: none | gzip | zstd. Default zstd. xz is deliberately
	// excluded — the pure-Go xz writer has no streaming Flush, so a live
	// tail of the artifact would lag behind the writer (ADR-116). An
	// unrecognized value fails soft at load and is refused at save.
	Algorithm string `yaml:"algorithm"`
	// ZstdWindowLog is log2 of the zstd compression window: 2^N bytes of
	// back-reference history, 10..27 (1 KiB .. 128 MiB; default 25 =
	// 32 MiB). Memory scales linearly with the window — the decoder needs
	// ~1× the window, the encoder ~2× (history buffer) plus hash tables.
	// Inert for non-zstd algorithms.
	ZstdWindowLog int `yaml:"zstd_window_log"`
}

// ValidateSessionZstdWindowLog is the shared window range rule: the floor is
// the zstd format minimum, the ceiling the operator cap (4× below the
// library max). Exported for runtime mutators (the same rule the load
// warnings and the save gate enforce, so it cannot drift between them).
func ValidateSessionZstdWindowLog(windowLog int) error {
	if windowLog < MinSessionZstdWindowLog || windowLog > MaxSessionZstdWindowLog {
		return fmt.Errorf(
			"session_artifacts: compression.zstd_window_log %d is out of range [%d, %d]",
			windowLog, MinSessionZstdWindowLog, MaxSessionZstdWindowLog)
	}
	return nil
}

// validate is the full-section validity rule, shared verbatim by the save
// gate (validateSessionArtifactsForSave). Zero values are the "unset"
// signal and pass — the load pipeline's ApplyDefaults resolves them to the
// defaults, so an unset section is never a persistence error.
func (c SessionCompressionConfig) validate() error {
	if c.Algorithm != "" && !ValidSessionCompressionAlgorithm(c.Algorithm) {
		return fmt.Errorf(
			"session_artifacts: compression.algorithm %q is not valid; must be one of: %s, %s or %s (xz excluded — no streaming Flush, ADR-116)",
			c.Algorithm, SessionCompressionNone, SessionCompressionGzip, SessionCompressionZstd)
	}
	if c.ZstdWindowLog != 0 {
		if err := ValidateSessionZstdWindowLog(c.ZstdWindowLog); err != nil {
			return err
		}
	}
	return nil
}

// normalizeSessionArtifacts validates the session_artifacts section in place
// and returns load warnings — fail-soft, one warning + reset per invalid
// knob (mirroring normalizeCapabilities' per-field shape, so one typo cannot
// discard the other knob). Runs AFTER ApplyDefaults (which resolves the
// unset algorithm/window to the defaults, making the persisted shape
// explicit), BEFORE validate — the persisted shape is always valid. The
// window is checked even under non-zstd algorithms: inert is not invalid,
// and the config must stay canonical.
func normalizeSessionArtifacts(cfg *Config) []string {
	c := &cfg.SessionArtifacts.Compression
	var warnings []string
	if !ValidSessionCompressionAlgorithm(c.Algorithm) {
		warnings = append(warnings, fmt.Sprintf(
			"session_artifacts.compression.algorithm ignored (falling back to %q): unrecognized algorithm %q",
			DefaultSessionCompressionAlgorithm, c.Algorithm))
		c.Algorithm = DefaultSessionCompressionAlgorithm
	}
	if err := ValidateSessionZstdWindowLog(c.ZstdWindowLog); err != nil {
		warnings = append(warnings, fmt.Sprintf(
			"session_artifacts.compression.zstd_window_log ignored (falling back to %d): %v",
			DefaultSessionZstdWindowLog, err))
		c.ZstdWindowLog = DefaultSessionZstdWindowLog
	}
	return warnings
}

// validateSessionArtifactsForSave is the save-time fail-closed gate: an
// invalid algorithm or an out-of-range zstd window must never persist —
// the load path heals first, so hitting this means an in-memory mutation
// tried to persist an unusable section. Unset values pass (the next load
// resolves them to the defaults).
func validateSessionArtifactsForSave(cfg *Config) error {
	return cfg.SessionArtifacts.Compression.validate()
}
