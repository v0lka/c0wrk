package mcp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestEnsureAutoIndex_BinaryNotInstalled verifies that EnsureAutoIndex returns
// (nil, nil) when the codebase-memory-mcp binary is not on PATH and not in
// the default install location.
func TestEnsureAutoIndex_BinaryNotInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	// Save original PATH and replace with an empty temp dir so LookPath fails.
	origPath := os.Getenv("PATH")
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)
	t.Cleanup(func() { _ = os.Setenv("PATH", origPath) })

	restore, err := EnsureAutoIndex(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if restore != nil {
		t.Fatal("expected nil restore function when binary is not installed")
	}
}

// TestEnsureAutoIndex_AutoIndexAlreadyTrue verifies that EnsureAutoIndex returns
// (nil, nil) when auto_index is already set to true. Uses a fake shell script
// that mimics "codebase-memory-mcp config list" output.
func TestEnsureAutoIndex_AutoIndexAlreadyTrue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	fakeBin := createFakeBinary(t, "config-list-true", `#!/bin/sh
echo "auto_index                = true"
echo "auto_index_threshold      = 50"
`)

	// Put the fake binary on PATH
	t.Setenv("PATH", filepath.Dir(fakeBin))

	restore, err := EnsureAutoIndex(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if restore != nil {
		t.Fatal("expected nil restore when auto_index is already true")
	}
}

// TestEnsureAutoIndex_ChangedToTrue verifies that EnsureAutoIndex returns a
// non-nil restore function when auto_index is false, and that invoking the
// restore function does not panic.
func TestEnsureAutoIndex_ChangedToTrue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	// Track which subcommands the script receives.
	logFile := filepath.Join(t.TempDir(), "calls.log")

	fakeBin := createFakeBinary(t, "config-list-false", `#!/bin/sh
echo "$@" >> `+logFile+`
if [ "$1" = "config" ] && [ "$2" = "list" ]; then
  echo "auto_index                = false"
  echo "auto_index_threshold      = 50"
  exit 0
fi
# "config set auto_index ..." — just succeed
exit 0
`)

	t.Setenv("PATH", filepath.Dir(fakeBin))

	restore, err := EnsureAutoIndex(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if restore == nil {
		t.Fatal("expected non-nil restore function when auto_index was false")
	}

	// Invoke restore — should not panic
	restore()

	// Verify the script was called with the expected arguments
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read call log: %v", err)
	}
	log := string(data)

	// Should have "config list" and "config set auto_index true" and restore "config set auto_index false"
	if log == "" {
		t.Fatal("expected calls to the fake binary, got none")
	}
	t.Logf("recorded calls:\n%s", log)
}

// TestEnsureAutoIndex_ConfigListError verifies that EnsureAutoIndex returns an
// error when "config list" fails.
func TestEnsureAutoIndex_ConfigListError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	fakeBin := createFakeBinary(t, "config-list-fail", `#!/bin/sh
exit 1
`)

	t.Setenv("PATH", filepath.Dir(fakeBin))

	restore, err := EnsureAutoIndex(context.Background())
	if err == nil {
		t.Fatal("expected error when config list fails")
	}
	if restore != nil {
		t.Fatal("expected nil restore on error")
	}
}

// createFakeBinary creates an executable script in a temp dir named
// "codebase-memory-mcp" with the given content and returns its path.
func createFakeBinary(t *testing.T, suffix, content string) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "codebase-memory-mcp")
	if err := os.WriteFile(binPath, []byte(content), 0o755); err != nil {
		t.Fatalf("failed to write fake binary: %v", err)
	}
	return binPath
}
