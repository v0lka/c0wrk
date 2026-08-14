package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// State is the persisted self-update runtime state, stored as update_state.json
// in the agent directory. It is deliberately separate from config.yaml
// (operator config): it records the ephemeral runtime state that drives the
// background auto-check interval logic and the user-dismissed release tag.
//
// Field semantics:
//   - SkippedVersion is the authoritative record of the release tag the user
//     dismissed (set via SkipVersion, see backend/frontend_api_updater.go);
//     a release equal to this tag is suppressed until a newer one is
//     published.
//   - LastCheck is the wall-clock time of the most recent automatic check
//     (attempted — whether or not an update was found). A zero value means
//     "never checked", which always triggers an immediate check.
type State struct {
	// SkippedVersion is the release tag the user dismissed; a release equal to
	// this tag is suppressed until a newer one is published.
	SkippedVersion string `json:"skipped_version,omitempty"`
	// LastCheck is the wall-clock time of the most recent automatic update
	// check. Zero means "never checked". (No omitempty: time.Time is a struct,
	// so omitempty has no effect — it always serializes, round-tripping to a
	// zero time that ShouldCheck treats as "never checked".)
	LastCheck time.Time `json:"last_check"`
}

// LoadState reads the update state from path. A missing file returns a zero
// State and a nil error (first run / clean state) so a fresh install never
// blocks the update flow. A corrupt JSON file also returns a zero State and a
// nil error: a malformed state file must not prevent the app from starting or
// from re-checking. Genuine I/O errors (e.g. permission denied) are returned so
// the caller can decide; the background check treats any error as a zero state.
func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("read update state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt state file is treated as a clean slate rather than failing
		// the update flow; the next SaveState overwrites it.
		return State{}, nil //nolint:nilerr // corrupt state file → clean slate, never block
	}
	return s, nil
}

// SaveState writes the update state to path atomically (write to a temp file
// then rename) so a crash never leaves a truncated state file. The parent
// directory must already exist. The temp file is created with mode 0600 since
// the state file is per-user runtime data.
func SaveState(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal update state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write update state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit update state: %w", err)
	}
	return nil
}

// ShouldCheck reports whether an automatic update check is due, given the time
// of the last check, the minimum interval between checks, and the current time.
// It is a pure function (no I/O) so the interval logic is unit-testable in
// isolation.
//
// A zero lastCheck (never checked) always returns true. A non-positive interval
// is treated as "always check" (defensive: a misconfigured interval never
// permanently disables checks).
func ShouldCheck(lastCheck time.Time, interval time.Duration, now time.Time) bool {
	if interval <= 0 {
		return true
	}
	if lastCheck.IsZero() {
		return true
	}
	return now.Sub(lastCheck) >= interval
}
