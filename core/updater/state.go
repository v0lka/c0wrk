package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/sp4rk/safeio"
)

// State is the persisted self-update runtime state (update_state.json in the
// agent directory), deliberately separate from config.yaml (operator
// config): the ephemeral runtime state driving the background auto-check
// interval and the user-dismissed release tag (set via SkipVersion,
// backend/frontend_api_updater.go).
type State struct {
	// SkippedVersion is the release tag the user dismissed; a release equal to
	// this tag is suppressed until a newer one is published.
	SkippedVersion string `json:"skipped_version,omitempty"`
	// LastCheck is the wall-clock time of the most recent automatic check
	// (attempted — whether or not an update was found); zero means "never
	// checked", which always triggers an immediate check. No omitempty:
	// time.Time always serializes, round-tripping a zero time that ShouldCheck
	// treats as "never checked".
	LastCheck time.Time `json:"last_check"`
}

// LoadState reads the update state from path. A missing or corrupt file
// returns a zero State and a nil error — a fresh install or malformed state
// must never block startup or re-checking (the next SaveState overwrites
// it). Genuine I/O errors (e.g. permission denied) are returned for the
// caller to decide; the background check treats any error as a zero state.
func LoadState(path string) (State, error) {
	data, err := safeio.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("read update state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, nil //nolint:nilerr // corrupt state file → clean slate, never block
	}
	return s, nil
}

// SaveState writes the update state to path atomically (temp file + rename)
// so a crash never leaves a truncated state file. The parent directory must
// already exist; mode 0600 — per-user runtime data.
func SaveState(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal update state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update_state-*.tmp")
	if err != nil {
		return fmt.Errorf("create update state temp file: %w", err)
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod update state temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write update state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close update state temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("commit update state: %w", err)
	}
	return nil
}

// ShouldCheck reports whether an automatic update check is due. A zero
// lastCheck ("never checked") always returns true; a non-positive interval
// is defensively treated as "always check", so a misconfigured interval
// never permanently disables checks. Pure function (no I/O) so the interval
// logic is unit-testable in isolation.
func ShouldCheck(lastCheck time.Time, interval time.Duration, now time.Time) bool {
	if interval <= 0 {
		return true
	}
	if lastCheck.IsZero() {
		return true
	}
	return now.Sub(lastCheck) >= interval
}
