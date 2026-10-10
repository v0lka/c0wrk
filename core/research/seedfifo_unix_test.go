//go:build unix

package research

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// runBounded runs fn under a hang watchdog: the planted-FIFO defects these
// tests pin are uninterruptible open(2) blocks, so a regression hangs the
// call forever — the watchdog turns that into a test failure instead of a
// wedged run. The timeout is a hang detector, never a synchronization wait.
func runBounded(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return within the watchdog (planted-FIFO hang?)", what)
	}
}

// TestDiskFileSet_SkipsPlantedFIFO pins the non-regular skip: a readerless
// FIFO on a NON-dot path inside a seeded pack directory used to be opened by
// fs.ReadFile with a plain blocking open, hanging the startup seeding
// forever. The scan must skip it (and keep the regular content) instead.
func TestDiskFileSet_SkipsPlantedFIFO(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(reg, []byte("# skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "notes"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	var files map[string][]byte
	var err error
	runBounded(t, "diskFileSet", func() {
		files, err = diskFileSet(dir)
	})
	if err != nil {
		t.Fatalf("diskFileSet: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("file count = %d, want 1 (only the regular file)", len(files))
	}
	if string(files["SKILL.md"]) != "# skill\n" {
		t.Errorf("SKILL.md = %q, want the regular content", files["SKILL.md"])
	}
	if _, ok := files["notes"]; ok {
		t.Error("the planted FIFO must not appear in the file set")
	}
}

// TestWriteMarkerAtomic_RefusesPlantedFIFOTmp pins the marker write: the
// previous implementation staged at the deterministic "<dir>/.seed-version.tmp"
// name with a bare os.WriteFile, so a FIFO planted there blocked the write
// open forever. The randomized staging name of safeio.WriteFileAtomic must
// leave the planted entry alone and still publish the marker.
func TestWriteMarkerAtomic_RefusesPlantedFIFOTmp(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "."+seedVersionFile+".tmp"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	runBounded(t, "writeMarkerAtomic", func() {
		if err := writeMarkerAtomic(dir, "v1"); err != nil {
			t.Errorf("writeMarkerAtomic: %v", err)
		}
	})
	data, err := os.ReadFile(filepath.Join(dir, seedVersionFile))
	if err != nil {
		t.Fatalf("reading published marker: %v", err)
	}
	if string(data) != "v1" {
		t.Errorf("marker = %q, want %q", data, "v1")
	}
}
