package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// checkIgnoreFixture builds a repo whose .gitignore carries the three rule
// shapes the flags batch must honour: a directory pattern (build/), a glob
// (*.log) and a negation (!keep.log).
func checkIgnoreFixture(t *testing.T, dir string) {
	t.Helper()
	write := func(name, content string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(".gitignore", "build/\n*.log\n!keep.log\n")
	write("normal.txt", "n")
	write("build/out.bin", "b")
	write("build/deep/nested.txt", "d")
	write("sub/a.log", "a")
	write("sub/keep.log", "k")
	run := func(args ...string) {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", ".")
	run("add", "normal.txt")
	run("-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-qm", "init")
}

// TestFilterGitIgnoredPaths_BatchVerdicts pins the batched check-ignore
// contract behind ListDirectory's ignore flags: the returned set keys on the
// input strings verbatim, covers directory patterns, globs and children of
// ignored directories, and honours negation.
func TestFilterGitIgnoredPaths_BatchVerdicts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	checkIgnoreFixture(t, dir)

	paths := []string{
		filepath.Join(dir, "build"),
		filepath.Join(dir, "build", "deep"),
		filepath.Join(dir, "normal.txt"),
		filepath.Join(dir, "sub", "a.log"),
		filepath.Join(dir, "sub", "keep.log"),
	}
	flags, err := FilterGitIgnoredPaths(t.Context(), dir, paths)
	if err != nil {
		t.Fatalf("FilterGitIgnoredPaths: %v", err)
	}

	wantIgnored := map[string]bool{
		filepath.Join(dir, "build"):         true,
		filepath.Join(dir, "build", "deep"): true,
		filepath.Join(dir, "sub", "a.log"):  true,
	}
	for _, p := range paths {
		if got := flags[p]; got != wantIgnored[p] {
			t.Errorf("flag(%s) = %v, want %v", p, got, wantIgnored[p])
		}
	}
	if len(flags) != len(wantIgnored) {
		t.Errorf("flags map holds %d entries, want %d: %v", len(flags), len(wantIgnored), flags)
	}
}

// TestFilterGitIgnoredPaths_NoneIgnoredAndEmpty pins the two no-work shapes:
// an empty input never spawns git, and a "nothing is ignored" run (exit 1) is
// a successful empty answer, not an error.
func TestFilterGitIgnoredPaths_NoneIgnoredAndEmpty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	checkIgnoreFixture(t, dir)

	flags, err := FilterGitIgnoredPaths(t.Context(), dir, nil)
	if err != nil {
		t.Fatalf("empty paths: %v", err)
	}
	if len(flags) != 0 {
		t.Fatalf("empty paths must return an empty set, got %v", flags)
	}

	flags, err = FilterGitIgnoredPaths(t.Context(), dir, []string{filepath.Join(dir, "normal.txt")})
	if err != nil {
		t.Fatalf("none-ignored run: %v", err)
	}
	if len(flags) != 0 {
		t.Fatalf("none-ignored run must return an empty set, got %v", flags)
	}
}

// TestFilterGitIgnoredPaths_NonRepoDegrades pins the non-repo answer: no
// filtering, no error — ListDirectory relies on both.
func TestFilterGitIgnoredPaths_NonRepoDegrades(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "whatever.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	flags, err := FilterGitIgnoredPaths(t.Context(), dir, []string{p})
	if err != nil {
		t.Fatalf("non-repo must degrade to an empty set, got error: %v", err)
	}
	if len(flags) != 0 {
		t.Fatalf("non-repo must return an empty set, got %v", flags)
	}
}
