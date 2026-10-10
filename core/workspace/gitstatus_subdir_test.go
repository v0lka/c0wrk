package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// TestGitStatus_KeysPathsOnWorkTreeRoot pins review [99]: porcelain v1
// paths are relative to the REPOSITORY ROOT, not to the directory the
// command runs in. A workspace rooted at a repository subdirectory must
// key every entry onto the work-tree root — keying onto the workspace path
// produced non-existent paths (and a wrong-file DiscardChanges for
// colliding names).
func TestGitStatus_KeysPathsOnWorkTreeRoot(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittest.RequireGit(t)

	root := filepath.Join(gittest.TempDir(t), "repo")
	repo := gittest.InitRepo(t, root, "root\n")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir subdirectory: %v", err)
	}
	repo.Write(t, filepath.Join("sub", "nested.txt"), "nested\n")
	repo.Git(t, "add", ".")
	repo.Git(t, "commit", "-m", "nested")

	// Modify one root-level and one nested tracked file; add one untracked
	// file under the subdirectory.
	repo.Write(t, "file.txt", "root changed\n")
	repo.Write(t, filepath.Join("sub", "nested.txt"), "nested changed\n")
	repo.Write(t, filepath.Join("sub", "fresh.txt"), "untracked\n")

	status, err := GitStatus(context.Background(), sub)
	if err != nil {
		t.Fatalf("GitStatus(subdirectory workspace): %v", err)
	}

	// Every key must be rooted at the work-tree root.
	want := map[string]bool{
		filepath.Join(root, "file.txt"):          false,
		filepath.Join(root, "sub", "nested.txt"): false,
		filepath.Join(root, "sub", "fresh.txt"):  false,
	}
	for path := range status {
		if _, ok := want[path]; !ok {
			t.Errorf("unexpected status key %q; want keys rooted at the work-tree root %q", path, root)
		} else {
			want[path] = true
		}
	}
	for path, seen := range want {
		if !seen {
			t.Errorf("missing status key %q", path)
		}
	}
	if len(status) != len(want) {
		t.Errorf("status has %d entries, want %d", len(status), len(want))
	}
}
