package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dupFile returns an independent, WRITABLE handle mirroring the source file's
// open mode. The real consumer (manager.go) opens the dump log
// O_WRONLY|O_APPEND and hands a duplicated handle to background goroutines
// (title generation, ToolJudge) that append JSONL records to it. This test
// enforces that contract: the duplicated handle must accept writes and the
// bytes must land in the underlying file. On Windows this guards against a
// regression where dupFile re-opened the file read-only (os.Open), causing
// silent ERROR_ACCESS_DENIED on every append.
func TestDupFile_HandleIsWritable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "dump.jsonl")

	// Seed the file with initial content so we can distinguish append from
	// overwrite, then re-open it exactly as manager.go does.
	if err := os.WriteFile(path, []byte(`{"seed":true}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) failed: %v", path, err)
	}

	src, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q, O_WRONLY|O_APPEND) failed: %v", path, err)
	}
	t.Cleanup(func() { _ = src.Close() })

	dup, err := dupFile(src)
	if err != nil {
		t.Fatalf("dupFile(%q) failed: %v", path, err)
	}

	const marker = `{"event":"dup_write_test"}`
	if _, err := dup.WriteString(marker + "\n"); err != nil {
		t.Fatalf("WriteString on duplicated handle failed (handle is not writable): %v", err)
	}
	if err := dup.Close(); err != nil {
		t.Fatalf("Close on duplicated handle failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", path, err)
	}

	if !strings.Contains(string(got), marker) {
		t.Errorf("dupFile write did not reach the file: got content %q, want it to contain %q", string(got), marker)
	}

	// Seed content must survive: an append must not truncate the log.
	const seed = `{"seed":true}`
	if !strings.Contains(string(got), seed) {
		t.Errorf("dupFile write truncated pre-existing content: got %q, want it to still contain %q", string(got), seed)
	}
}
