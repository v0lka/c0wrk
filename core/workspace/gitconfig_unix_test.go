//go:build unix

package workspace

import (
	"os"
	"path/filepath"
	"strings"
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

// TestScanGitConfigRefusesNonRegularInfoAttributes proves the same FIFO
// guard for the .git/info/attributes routing source ([1]a): the attributes
// mechanism has no config kill-switch, so an unscannable source must fail
// the scan closed instead of running git with invisible routing — and must
// not hang the synchronous scan first.
func TestScanGitConfigRefusesNonRegularInfoAttributes(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(gitDir, "info", "attributes"), 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := ScanGitConfig(root)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "fail closed") {
			t.Fatalf("expected fail-closed error for a FIFO info/attributes, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ScanGitConfig hung on a FIFO info/attributes (blocking open)")
	}
}

// TestScanGitConfigRefusesNonRegularCommondir covers the guard for the
// worktree commondir pointer: a blocking open there would hang the scan of
// every linked worktree.
func TestScanGitConfigRefusesNonRegularCommondir(t *testing.T) {
	root := t.TempDir()
	fakeGitDir := filepath.Join(root, "fakegit")
	if err := os.MkdirAll(fakeGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeGitDir, "gitdir"), []byte(root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(fakeGitDir, "commondir"), 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+fakeGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := ScanGitConfig(root)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a FIFO commondir")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ScanGitConfig hung on a FIFO commondir (blocking open)")
	}
}

// TestReadCappedRefusesNonRegularAfterOpen pins the review [14] mechanism:
// regularity is decided by fstat on the ALREADY-OPEN non-blocking
// descriptor, with no Stat→Open window a racing local adversary could
// exploit. Every non-regular form — a directory (whose open SUCCEEDS on
// unix), a character device, and a FIFO (whose read-only O_NONBLOCK open
// returns immediately even with no writer) — must be refused promptly with
// the non-regular error instead of blocking or reading.
func TestReadCappedRefusesNonRegularAfterOpen(t *testing.T) {
	root := t.TempDir()

	// Directory: the open succeeds on unix; only the fstat refuses it.
	if _, err := readCapped(root, maxGitConfigBytes); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("readCapped(directory) err = %v, want a non-regular-file refusal", err)
	}

	// Character device.
	if _, err := os.Stat("/dev/null"); err == nil {
		if _, err := readCapped("/dev/null", maxGitConfigBytes); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("readCapped(/dev/null) err = %v, want a non-regular-file refusal", err)
		}
	}

	// FIFO with a watchdog: a regression to a blocking open hangs here.
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readCapped(fifo, maxGitConfigBytes)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("readCapped(FIFO) err = %v, want a non-regular-file refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("readCapped hung on a FIFO (O_NONBLOCK open or post-open fstat check missing)")
	}
}
