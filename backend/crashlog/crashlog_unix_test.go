//go:build unix

package crashlog

import (
	"syscall"
	"testing"
	"time"
)

// TestInstallRefusesNonRegularStderrLog proves the write-open guard on the
// startup path: a FIFO planted at the stderr log path must be refused with a
// clean error instead of blocking the O_WRONLY open forever. install runs
// synchronously in main before Wails starts, where a blocking write-open would
// freeze the app before a window ever opens. A watchdog fails the test instead
// of hanging CI.
func TestInstallRefusesNonRegularStderrLog(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(StderrLogPath(dir), 0o640); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		c, err := install(dir, false)
		if c != nil {
			_ = c.file.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a FIFO stderr log, got nil (blocking open?)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("install hung on a FIFO stderr log (blocking write-open)")
	}
}
