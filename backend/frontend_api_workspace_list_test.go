package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/workspace"
)

// gitRepoFixture creates a minimal committed git repository at dir.
// Requires git on PATH; the caller skips the test when absent.
func gitRepoFixture(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", ".")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	run("add", "tracked.txt")
	run("-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-qm", "init")
}

// TestListDirectory_GitIgnoreFailureDegrades pins the degradation contract of
// ListDirectory: when the workspace IS a git repository but computing the
// git-ignored paths fails (here: a corrupted .git/index, which git rev-parse
// tolerates but git ls-files refuses), the RPC must still return the
// directory listing — without ignore flags — instead of failing. A rejected
// root listing is cached as an EMPTY tree by the frontend's FileTreePanel
// with no retry, so a fatal error here blanked the whole file explorer.
func TestListDirectory_GitIgnoreFailureDegrades(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	gitRepoFixture(t, ws)

	// Fixture precondition: the repo must be detected as a repository...
	if !workspace.IsGitRepo(t.Context(), ws) {
		t.Fatal("fixture precondition failed: workspace not detected as git repo")
	}
	// ...while the ignored-paths computation fails (corrupted index:
	// rev-parse tolerates it, ls-files does not).
	if err := os.WriteFile(filepath.Join(ws, ".git", "index"), []byte("GARBAGE-NOT-AN-INDEX"), 0o644); err != nil {
		t.Fatalf("corrupt .git/index: %v", err)
	}
	if _, ignoredErr := workspace.GitIgnoredPaths(t.Context(), ws); ignoredErr == nil {
		t.Fatal("fixture precondition failed: expected GitIgnoredPaths to fail on corrupted index")
	}

	f := &FrontendAPI{agentDir: base}
	f.activeProjectMu.Lock()
	f.activeProjectID = "test-project"
	f.activeProjectPath = ws
	f.activeProjectMu.Unlock()

	nodes, err := f.ListDirectory(ws, false)
	if err != nil {
		t.Fatalf("ListDirectory must degrade, not fail, when the ignore set cannot be computed: %v", err)
	}
	var names = make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	found := false
	for _, n := range names {
		if n == "tracked.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected tracked.txt in the degraded listing, got %v", names)
	}
}

// TestListDirectory_NonRepoUnaffected guards the companion path: a plain
// non-git directory (the extracted-firmware case from the original report)
// lists normally and never enters the git-ignore computation at all.
func TestListDirectory_NonRepoUnaffected(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(filepath.Join(ws, "rootfs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	f := &FrontendAPI{agentDir: base}
	f.activeProjectMu.Lock()
	f.activeProjectID = "test-project"
	f.activeProjectPath = ws
	f.activeProjectMu.Unlock()

	nodes, err := f.ListDirectory(ws, false)
	if err != nil {
		t.Fatalf("ListDirectory on non-git workspace: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "rootfs" || !nodes[0].IsDir {
		t.Fatalf("expected exactly the rootfs dir entry, got %+v", nodes)
	}
}
