//go:build unix

package session

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestProcessImage_RefusesFifoWithoutHanging guards the attachment read path:
// processImage runs synchronously while an image attachment is staged, so a
// FIFO at the attachment path would block the read-open forever and freeze the
// staging RPC. A watchdog turns a regression into a failure, not a hung CI.
func TestProcessImage_RefusesFifoWithoutHanging(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "img.jpg")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, _, _, err := processImage(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("processImage(FIFO) succeeded, want an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("processImage hung on a FIFO (blocking open)")
	}
}
