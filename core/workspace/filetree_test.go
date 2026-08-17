package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListDirRecursive_MissingRootErrors pins the contract that a missing (or
// unreadable) walk root surfaces as an error rather than a silent
// (nil, nil). Before this was fixed, a missing root produced a nil slice with
// a nil error: the Wails binding serialized it as `null`, the frontend shape
// guard degraded it to `[]`, and the chat input's @-autocomplete cached that
// "empty workspace" permanently — file completions then stopped appearing
// until the app was restarted.
func TestListDirRecursive_MissingRootErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	nodes, err := ListDirRecursive(missing, nil)
	if err == nil {
		t.Fatal("expected an error for a missing walk root, got nil")
	}
	if nodes != nil {
		t.Fatalf("expected nil nodes alongside the error, got %d", len(nodes))
	}
}

// TestListDirRecursive_EmptyDirReturnsEmptyNonNil ensures an existing but
// empty directory yields a non-nil empty slice. A nil slice would serialize
// as `null` across the Wails binding and be indistinguishable from a failed
// listing on the frontend side.
func TestListDirRecursive_EmptyDirReturnsEmptyNonNil(t *testing.T) {
	empty := t.TempDir()

	nodes, err := ListDirRecursive(empty, nil)
	if err != nil {
		t.Fatalf("ListDirRecursive on empty dir: unexpected error: %v", err)
	}
	if nodes == nil {
		t.Fatal("expected non-nil nodes slice for an existing (empty) directory")
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes for an empty directory, got %d", len(nodes))
	}
}

// TestListDirRecursive_ListsFiles verifies the happy path still walks nested
// entries after the root-error handling was added.
func TestListDirRecursive_ListsFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("write sub/b.txt: %v", err)
	}

	nodes, err := ListDirRecursive(root, nil)
	if err != nil {
		t.Fatalf("ListDirRecursive: unexpected error: %v", err)
	}

	byPath := make(map[string]FileNode, len(nodes))
	for _, n := range nodes {
		byPath[n.Path] = n
	}
	for _, want := range []string{
		filepath.Join(root, "a.txt"),
		filepath.Join(root, "sub"),
		filepath.Join(root, "sub", "b.txt"),
	} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("expected %s in recursive listing; got %v entries", want, len(nodes))
		}
	}
}
