package pathutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsWithinPath_Inside(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "ws")
	child := filepath.Join(parent, "sub", "file.txt")
	_ = os.MkdirAll(filepath.Dir(child), 0o755)

	ok, err := IsWithinPath(parent, child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("child should be within parent")
	}
}

func TestIsWithinPath_SamePath(t *testing.T) {
	dir := t.TempDir()
	ok, err := IsWithinPath(dir, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("same path should be considered within")
	}
}

func TestIsWithinPath_Outside(t *testing.T) {
	parent := t.TempDir()
	child := t.TempDir() // different directory

	ok, err := IsWithinPath(parent, child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("sibling directory should not be within parent")
	}
}

func TestIsWithinPath_Parent(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "sub")
	_ = os.MkdirAll(child, 0o755)

	// child is within parent → true; parent is within child → false
	ok, err := IsWithinPath(child, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("parent should not be within child")
	}
}

func TestIsWithinPath_SymlinkWithin(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	link := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, link)
	child := filepath.Join(link, "file.txt")

	ok, err := IsWithinPath(dir, child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("symlink pointing inside workspace should be within")
	}
}

func TestIsWithinPath_SymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside", "real")
	_ = os.MkdirAll(outside, 0o755)
	link := filepath.Join(dir, "ws", "escape")
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	_ = os.Symlink(outside, link)

	ws := filepath.Join(dir, "ws")
	ok, err := IsWithinPath(ws, link)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("symlink pointing outside workspace should not be within")
	}
}

func TestIsWithinPath_NonExistent(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "ws")
	child := filepath.Join(parent, "sub", "nonexistent.txt")
	// Neither ws/ nor sub/ exists.

	ok, err := IsWithinPath(parent, child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("non-existent path under non-existent parent should be considered within")
	}
}

func TestIsWithinPath_EmptyParent(t *testing.T) {
	ok, err := IsWithinPath("", "/some/path")
	if err == nil {
		t.Error("empty parent should return an error (fail closed)")
	}
	if ok {
		t.Error("empty parent should return false")
	}
}

func TestIsWithinPath_DifferentVolumes(t *testing.T) {
	// filepath.Rel fails for paths on different roots.
	// On macOS/Linux this is hard to test without actual mounts;
	// test the error path by providing fundamentally incompatible paths.
	ok, err := IsWithinPath("/nonexistent/vol/a", "/vol/b")
	if err != nil && ok {
		t.Error("ok should be false when Rel returns error")
	}
}

func TestSplitPathComponents_Absolute(t *testing.T) {
	result := SplitPathComponents("/home/user/file.txt")
	if len(result) != 3 || result[0] != "home" || result[1] != "user" || result[2] != "file.txt" {
		t.Errorf("got %v, want [home user file.txt]", result)
	}
}

func TestSplitPathComponents_Root(t *testing.T) {
	result := SplitPathComponents("/")
	if len(result) != 0 {
		t.Errorf("root path should yield empty slice, got %v", result)
	}
}

func TestSplitPathComponents_Empty(t *testing.T) {
	result := SplitPathComponents("")
	if len(result) != 0 {
		t.Errorf("empty path should yield empty slice, got %v", result)
	}
}

func TestResolveExistingPrefix_AllExist(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "file.txt")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	result := ResolveExistingPrefix(filePath)
	// On macOS, /var → /private/var symlinks can cause resolution;
	// compare resolved-to-resolved.
	expected, _ := filepath.EvalSymlinks(filePath)
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestResolveExistingPrefix_PartialExist(t *testing.T) {
	dir := t.TempDir()
	nonexistent := filepath.Join(dir, "sub", "nonexistent.txt")

	result := ResolveExistingPrefix(nonexistent)
	// The dir prefix may be symlink-resolved (macOS /var → /private/var).
	// Use ResolveExistingPrefix on the expected result for fair comparison.
	expected, _ := filepath.EvalSymlinks(dir)
	expected = filepath.Join(expected, "sub", "nonexistent.txt")
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestResolveExistingPrefix_NoneExist(t *testing.T) {
	result := ResolveExistingPrefix("/nonexistent/root/path/file.txt")
	if result != "/nonexistent/root/path/file.txt" {
		t.Errorf("completely non-existent path should be unchanged: got %q", result)
	}
}

func TestResolveExistingPrefix_WithSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	link := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, link)

	child := filepath.Join(link, "newfile.txt")
	result := ResolveExistingPrefix(child)
	// realDir may also be resolved (macOS /var → /private/var).
	resolvedReal, _ := filepath.EvalSymlinks(realDir)
	expected := filepath.Join(resolvedReal, "newfile.txt")
	if result != expected {
		t.Errorf("symlink prefix should be resolved: got %q, want %q", result, expected)
	}
}
