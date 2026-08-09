package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadState_MissingFileReturnsZero verifies that a non-existent state file
// (first run / clean state) returns a zero State without error so the update
// flow is never blocked on a fresh install.
func TestLoadState_MissingFileReturnsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState missing file: unexpected error %v", err)
	}
	if s.SkippedVersion != "" {
		t.Errorf("SkippedVersion = %q, want empty", s.SkippedVersion)
	}
	if !s.LastCheck.IsZero() {
		t.Errorf("LastCheck = %v, want zero", s.LastCheck)
	}
}

// TestSaveLoadState_RoundTrip verifies that a State saved and reloaded is
// preserved exactly, including a populated SkippedVersion and LastCheck.
func TestSaveLoadState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	original := State{
		SkippedVersion: "v1.2.4",
		LastCheck:      time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
	}
	if err := SaveState(path, original); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.SkippedVersion != original.SkippedVersion {
		t.Errorf("SkippedVersion = %q, want %q", got.SkippedVersion, original.SkippedVersion)
	}
	if !got.LastCheck.Equal(original.LastCheck) {
		t.Errorf("LastCheck = %v, want %v", got.LastCheck, original.LastCheck)
	}
}

// TestSaveLoadState_EmptyFields verifies that an empty State saves and loads
// without losing information (an all-zero State round-trips to a zero value).
func TestSaveLoadState_EmptyFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	if err := SaveState(path, State{}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.SkippedVersion != "" || !got.LastCheck.IsZero() {
		t.Errorf("LoadState of empty State = %+v, want zero value", got)
	}
}

// TestSaveState_IsAtomic verifies that SaveState leaves no temp file behind on
// success (the tmp file is renamed into place).
func TestSaveState_IsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	if err := SaveState(path, State{SkippedVersion: "v2.0.0"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	// The final file must exist...
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file missing after save: %v", err)
	}
	// ...and no leftover .tmp file.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("leftover .tmp file present; err=%v", err)
	}
}

// TestSaveState_OverwritesExisting verifies that saving over an existing state
// file replaces its contents rather than appending or erroring.
func TestSaveState_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	if err := SaveState(path, State{SkippedVersion: "v1.0.0"}); err != nil {
		t.Fatalf("first SaveState: %v", err)
	}
	if err := SaveState(path, State{SkippedVersion: "v2.0.0"}); err != nil {
		t.Fatalf("second SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.SkippedVersion != "v2.0.0" {
		t.Errorf("SkippedVersion = %q, want %q after overwrite", got.SkippedVersion, "v2.0.0")
	}
}

// TestLoadState_CorruptJSONReturnsZero verifies that a malformed state file is
// treated as a clean slate (zero State, nil error) rather than failing the
// update flow.
func TestLoadState_CorruptJSONReturnsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState corrupt file: want nil error, got %v", err)
	}
	if got.SkippedVersion != "" || !got.LastCheck.IsZero() {
		t.Errorf("LoadState corrupt file = %+v, want zero value", got)
	}
}

// TestSaveState_ProducesValidJSON verifies the on-disk file is valid JSON with
// the expected keys (omitting empty fields).
func TestSaveState_ProducesValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	want := State{
		SkippedVersion: "v1.2.4",
		LastCheck:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	if err := SaveState(path, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("on-disk file is not valid JSON: %v", err)
	}
	if got["skipped_version"] != "v1.2.4" {
		t.Errorf("skipped_version key = %v, want v1.2.4", got["skipped_version"])
	}
	if _, ok := got["last_check"]; !ok {
		t.Error("last_check key missing from saved JSON")
	}
}

// TestSaveState_FileMode verifies the state file is created with mode 0600
// (per-user runtime data, not group/world readable).
func TestSaveState_FileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	if err := SaveState(path, State{SkippedVersion: "v1.0.0"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Compare only the permission bits (mask off type).
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
}

// ---------------------------------------------------------------------------
// Interval logic (ShouldCheck)
// ---------------------------------------------------------------------------

func TestShouldCheck_NeverCheckedAlwaysDue(t *testing.T) {
	now := time.Now()
	if !ShouldCheck(time.Time{}, 6*time.Hour, now) {
		t.Error("zero lastCheck should always be due")
	}
}

func TestShouldCheck_ZeroOrNegativeIntervalAlwaysDue(t *testing.T) {
	now := time.Now()
	recent := now.Add(-1 * time.Minute)
	// interval <= 0 is treated as "always check" defensively.
	if !ShouldCheck(recent, 0, now) {
		t.Error("interval 0 should always be due")
	}
	if !ShouldCheck(recent, -1*time.Second, now) {
		t.Error("negative interval should always be due")
	}
}

func TestShouldCheck_WithinIntervalNotDue(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	// Checked 1 hour ago, interval 6h -> not due.
	lastCheck := now.Add(-1 * time.Hour)
	if ShouldCheck(lastCheck, 6*time.Hour, now) {
		t.Error("check 1h ago with 6h interval should NOT be due")
	}
}

func TestShouldCheck_IntervalExactlyElapsedIsDue(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	// Checked exactly 6h ago -> due (>= interval).
	lastCheck := now.Add(-6 * time.Hour)
	if !ShouldCheck(lastCheck, 6*time.Hour, now) {
		t.Error("check exactly 6h ago with 6h interval should be due")
	}
}

func TestShouldCheck_PastIntervalIsDue(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	// Checked 7h ago, interval 6h -> due.
	lastCheck := now.Add(-7 * time.Hour)
	if !ShouldCheck(lastCheck, 6*time.Hour, now) {
		t.Error("check 7h ago with 6h interval should be due")
	}
}

func TestShouldCheck_JustBeforeIntervalNotDue(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	// Checked 5h59m ago, interval 6h -> not due.
	lastCheck := now.Add(-(6*time.Hour - time.Minute))
	if ShouldCheck(lastCheck, 6*time.Hour, now) {
		t.Error("check 5h59m ago with 6h interval should NOT be due")
	}
}

// TestShouldCheck_FarFutureLastCheckNotDue verifies that a LastCheck in the
// future (clock skew / manual edit) is treated as "not due" rather than
// triggering an immediate re-check.
func TestShouldCheck_FarFutureLastCheckNotDue(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	lastCheck := now.Add(24 * time.Hour) // clock ahead
	if ShouldCheck(lastCheck, 6*time.Hour, now) {
		t.Error("future lastCheck should NOT be due")
	}
}
