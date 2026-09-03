//go:build unix

package workspace

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestScanGitConfigRefusesNonRegularConfig proves the FIFO/DoS guard: a FIFO
// planted as .git/config (or as the .git pointer file) must be refused with
// an error instead of blocking the open forever — the scan runs synchronously
// on the SwitchProject RPC path, where a blocking open would hang the app.
// The scan is executed with a watchdog so a regression fails the test instead
// of hanging CI.
func TestScanGitConfigRefusesNonRegularConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, ".git", "config"), 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := ScanGitConfig(root)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a non-regular .git/config")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ScanGitConfig hung on a FIFO .git/config (blocking open)")
	}
}

// TestScanGitConfigRefusesNonRegularGitPointer covers the same guard for the
// .git pointer file itself.
func TestScanGitConfigRefusesNonRegularGitPointer(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, ".git"), 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := ScanGitConfig(root)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a FIFO .git pointer")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ScanGitConfig hung on a FIFO .git pointer (blocking open)")
	}
}
