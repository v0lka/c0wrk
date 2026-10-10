package desktop

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

func TestLoadDialogState_MissingFileReturnsZeroValue(t *testing.T) {
	got := LoadDialogState(t.TempDir())
	if got.LastDirectory != "" {
		t.Fatalf("expected zero value for missing file, got %+v", got)
	}
}

func TestLoadDialogState_MalformedFileReturnsZeroValue(t *testing.T) {
	agentDir := t.TempDir()
	if err := os.WriteFile(config.DialogStatePath(agentDir), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := LoadDialogState(agentDir)
	if got.LastDirectory != "" {
		t.Fatalf("expected zero value for malformed file, got %+v", got)
	}
}

func TestLoadDialogState_StaleDirectoryDropped(t *testing.T) {
	agentDir := t.TempDir()
	stale := filepath.Join(agentDir, "deleted-dir")
	if err := writeDialogState(agentDir, DialogState{LastDirectory: stale}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := LoadDialogState(agentDir)
	if got.LastDirectory != "" {
		t.Fatalf("expected stale directory to be dropped, got %q", got.LastDirectory)
	}
}

func TestLoadDialogState_FilePathDropped(t *testing.T) {
	agentDir := t.TempDir()
	filePath := filepath.Join(agentDir, "some_file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writeDialogState(agentDir, DialogState{LastDirectory: filePath}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	got := LoadDialogState(agentDir)
	if got.LastDirectory != "" {
		t.Fatalf("expected non-directory path to be dropped, got %q", got.LastDirectory)
	}
}

func TestLoadWriteDialogState_RoundTrip(t *testing.T) {
	agentDir := t.TempDir()
	want := DialogState{LastDirectory: agentDir}
	if err := writeDialogState(agentDir, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := LoadDialogState(agentDir)
	if got != want {
		t.Fatalf("round-trip mismatch: want %+v, got %+v", want, got)
	}
}

func TestWriteDialogState_NoTempFileLeftBehind(t *testing.T) {
	agentDir := t.TempDir()
	if err := writeDialogState(agentDir, DialogState{LastDirectory: agentDir}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(config.DialogStatePath(agentDir) + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file should not exist after successful save")
	}
}

func TestRememberDialogDirectory(t *testing.T) {
	agentDir := t.TempDir()

	// Empty pick (cancel) is a no-op and must not create the state file.
	rememberDialogDirectory(agentDir, "", slog.Default())
	if _, err := os.Stat(config.DialogStatePath(agentDir)); !os.IsNotExist(err) {
		t.Fatal("cancel must not write dialog state")
	}

	// A real pick persists the directory and is read back by LoadDialogState.
	picked := t.TempDir()
	rememberDialogDirectory(agentDir, picked, slog.Default())
	got := LoadDialogState(agentDir)
	if got.LastDirectory != picked {
		t.Fatalf("expected persisted %q, got %q", picked, got.LastDirectory)
	}
}

// TestWriteDialogState_ReplacesSymlinkInsteadOfTarget pins the #65 fix for
// the dialog state file: a symlink planted at the fixed dialog_state.json
// path must be replaced by the atomic rename, never written through.
func TestWriteDialogState_ReplacesSymlinkInsteadOfTarget(t *testing.T) {
	agentDir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	path := config.DialogStatePath(agentDir)
	if err := os.Symlink(victim, path); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	if err := writeDialogState(agentDir, DialogState{LastDirectory: "/tmp"}); err != nil {
		t.Fatalf("writeDialogState: %v", err)
	}

	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(data) != "keep me" {
		t.Fatalf("symlink target was overwritten: %q", data)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("state file must be a replaced regular file, got %v (%v)", fi, err)
	}
}
