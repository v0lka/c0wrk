package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// liteStatusFixture builds a repo with a modified tracked file and an
// untracked directory carrying multiple files — the shape that separates
// -uall (per-file untracked enumeration) from -unormal (collapsed dir/).
func liteStatusFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", ".")
	run("add", "tracked.txt")
	run("-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-qm", "init")
	// Post-commit work-tree state: one tracked modification, one fresh
	// untracked directory with two files.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "newmod"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, "newmod", name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGitStatusLite_CollapsesUntrackedDirs pins the contract that separates
// the tree's badge source from the Git panel's staging source: under
// GitStatusLite the untracked directory arrives as ONE entry (worktree '?'),
// the files beneath it are NOT enumerated, and tracked modifications are
// identical to the full status. The collapsed key is the directory path
// without a trailing slash, matching the tree node's path.
func TestGitStatusLite_CollapsesUntrackedDirs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	liteStatusFixture(t, dir)

	full, err := GitStatus(t.Context(), dir)
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}
	lite, err := GitStatusLite(t.Context(), dir)
	if err != nil {
		t.Fatalf("GitStatusLite: %v", err)
	}

	dirKey := filepath.Join(dir, "newmod")
	entry, ok := lite[dirKey]
	if !ok {
		t.Fatalf("lite status must key the collapsed untracked dir at %s, got %v", dirKey, lite)
	}
	if entry.WorkTreeStatus != "?" {
		t.Errorf("collapsed dir worktree status = %q, want ?", entry.WorkTreeStatus)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, enumerated := lite[filepath.Join(dir, "newmod", name)]; enumerated {
			t.Errorf("lite status must not enumerate %s beneath the collapsed dir", name)
		}
		if _, enumerated := full[filepath.Join(dir, "newmod", name)]; !enumerated {
			t.Errorf("full status fixture precondition: %s must be listed", name)
		}
	}

	// Tracked modification: identical in both variants.
	modKey := filepath.Join(dir, "tracked.txt")
	if lite[modKey] != full[modKey] {
		t.Errorf("tracked-file entry diverges between lite and full: %+v vs %+v", lite[modKey], full[modKey])
	}
	if full[modKey].WorkTreeStatus != "M" {
		t.Errorf("fixture precondition: tracked.txt must be M, got %+v", full[modKey])
	}
}
