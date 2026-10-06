//go:build unix

package vectorindex

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestIsBinaryHeader_RefusesFifoWithoutHanging guards the indexer's header
// pre-read: processFile stat-checks regularity and skips non-regular entries,
// but isBinaryHeader opens the file directly, so it must refuse a FIFO itself
// rather than block the indexing pass — and any shutdown join waiting on it —
// forever. A watchdog fails the test on a regression to a blocking open.
func TestIsBinaryHeader_RefusesFifoWithoutHanging(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "asset.bin")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := isBinaryHeader(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("isBinaryHeader(FIFO) succeeded, want a non-regular refusal")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("isBinaryHeader hung on a FIFO (blocking open)")
	}
}
