package updater

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupTempGlobs_SkipsLiveEntries pins the #37 age guard: a manual
// CleanupStaleUpdaters running while another instance downloads or stages an
// update must not reap the live staging tree. Entries (and their contents)
// younger than cleanupStaleMaxAge survive; genuinely stale ones are removed.
func TestCleanupTempGlobs_SkipsLiveEntries(t *testing.T) {
	tempDir := t.TempDir()

	live := filepath.Join(tempDir, "c0wrk-update-live-123")
	stale := filepath.Join(tempDir, "c0wrk-update-stale-456")
	freshChildInStaleParent := filepath.Join(tempDir, "c0wrk-update-stale-parent-789")
	// File-shaped artifacts (e.g. the Windows c0wrk-updater.exe): decided on
	// their own mtime, never exempted via the directory fail-open path.
	liveFile := filepath.Join(tempDir, "c0wrk-updater-live.exe")
	staleFile := filepath.Join(tempDir, "c0wrk-updater-stale.exe")
	for _, dir := range []string{live, stale, freshChildInStaleParent} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(live, "archive.zip"), "live")
	write(filepath.Join(stale, "archive.zip"), "stale")
	write(filepath.Join(freshChildInStaleParent, "download.part"), "partial")
	write(liveFile, "live updater")
	write(staleFile, "stale updater")

	old := time.Now().Add(-2 * cleanupStaleMaxAge)
	// Backdate the stale entry AND its child: both must be older than the
	// bound for the entry to be considered stale.
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(stale, "archive.zip"), old, old); err != nil {
		t.Fatal(err)
	}
	// The fresh-child parent's own mtime is backdated, but its child was
	// written just now — the guard must keep the parent.
	if err := os.Chtimes(freshChildInStaleParent, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleFile, old, old); err != nil {
		t.Fatal(err)
	}

	cleanupTempGlobs(tempDir, slog.New(slog.DiscardHandler),
		"c0wrk-update-*", "c0wrk-updater-*")

	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live staging entry was reaped: %v", err)
	}
	if _, err := os.Stat(freshChildInStaleParent); err != nil {
		t.Fatalf("entry with a fresh child was reaped: %v", err)
	}
	if _, err := os.Stat(liveFile); err != nil {
		t.Fatalf("live file artifact was reaped: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale entry survived the cleanup: stat err = %v", err)
	}
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatalf("stale file artifact survived the cleanup: stat err = %v", err)
	}
}
