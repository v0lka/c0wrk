package vectorindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// Session-managed worktrees (ADR-080) live INSIDE the repository at
// <repo>/.worktrees/<name>. The vector-index walks of a workspace must
// never descend into a nested .worktrees container: each managed tree is a
// separate session workspace with its own index rooted at the tree itself,
// and indexing the container from the main checkout would duplicate every
// managed tree's content into the main session's index. The hidden-dot
// guard is the exclusion mechanism (a leading-dot segment is never
// indexable); these tests pin it explicitly for the worktree container and
// pin the inverse — a managed session indexing its OWN tree works, because
// only segments RELATIVE to the walk root are inspected.

func TestWalkProjectFiles_ExcludesNestedWorktreesContainer(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"main.go":                           "package main",
		".worktrees/s1/session.go":          "package s1",
		".worktrees/s1/nested/deep.go":      "package nested",
		".worktrees/s2/another.go":          "package s2",
		"sub/regular.go":                    "package sub",
		"sub/.worktrees/nested-repo/x.go":   "package nestedrepo",
		"other/.hidden-deep/also-hidden.go": "package hidden",
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	got, err := walkProjectFiles(root, noopIgnoreChecker{}, 1<<20)
	if err != nil {
		t.Fatalf("walkProjectFiles: %v", err)
	}
	set := make(map[string]bool, len(got))
	for _, p := range got {
		set[p] = true
	}
	if !set[filepath.Join(root, "main.go")] || !set[filepath.Join(root, "sub", "regular.go")] {
		t.Errorf("regular files missing from walk: %v", got)
	}
	for _, forbidden := range []string{
		filepath.Join(root, ".worktrees", "s1", "session.go"),
		filepath.Join(root, ".worktrees", "s2", "another.go"),
		filepath.Join(root, "sub", ".worktrees", "nested-repo", "x.go"),
	} {
		if set[forbidden] {
			t.Errorf("walk included nested worktree path %q", forbidden)
		}
	}
}

func TestIsIndexablePath_WorktreesContainerExcluded(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join(root, "main.go"), true},
		{filepath.Join(root, ".worktrees", "s1", "session.go"), false},
		{filepath.Join(root, ".worktrees"), false},
		{filepath.Join(root, "sub", ".worktrees", "x", "f.go"), false},
		{filepath.Join(root, "sub", "f.go"), true},
	}
	for _, tc := range cases {
		if got := isIndexablePath(tc.path, root, noopIgnoreChecker{}); got != tc.want {
			t.Errorf("isIndexablePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsIndexablePath_ManagedSessionIndexesOwnTree(t *testing.T) {
	repo := t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "s1")
	if !isIndexablePath(filepath.Join(wt, "src", "main.go"), wt, noopIgnoreChecker{}) {
		t.Error("files under a managed session's own tree root are not indexable; the hidden container segment is above the walk root and must not block indexing")
	}
	if isIndexablePath(filepath.Join(wt, ".git", "config"), wt, noopIgnoreChecker{}) {
		t.Error(".git inside a managed tree must stay non-indexable")
	}
}

// Real-git variants (a genuine `git worktree add` fixture): the synthetic
// tests above pin the hidden-dot guard against planted paths; these pin the
// same contract against the on-disk shape a managed session workspace really
// has — a linked worktree whose `.git` is a pointer FILE, living inside the
// checkout's `.worktrees` container.

func TestWalkProjectFiles_RealLinkedWorktreeInsideContainer(t *testing.T) {
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	wt := addWorktree(t, repo, filepath.Join(root, ".worktrees", "s1"), "topic")
	repo.Write(t, "checkout.go", "package main\n")

	// Sanity: the worktree is a real linked worktree (pointer file, not dir).
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("fixture sanity: %s/.git must be a pointer file", wt)
	}

	// Walking the CHECKOUT excludes the whole managed container, exactly as
	// the synthetic test pins — with the real worktree's files on disk.
	got, err := walkProjectFiles(root, noopIgnoreChecker{}, 1<<20)
	if err != nil {
		t.Fatalf("walkProjectFiles(checkout): %v", err)
	}
	for _, p := range got {
		if strings.HasPrefix(p, filepath.Join(root, ".worktrees")) {
			t.Errorf("checkout walk included managed-tree path %q", p)
		}
	}
	found := false
	for _, p := range got {
		if p == filepath.Join(root, "checkout.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("checkout walk missing checkout.go: %v", got)
	}

	// Walking the WORKTREE ITSELF (the managed session's own index root)
	// indexes its regular files — including the initial-commit file.txt —
	// while the `.git` pointer FILE stays excluded by name.
	got, err = walkProjectFiles(wt, noopIgnoreChecker{}, 1<<20)
	if err != nil {
		t.Fatalf("walkProjectFiles(worktree): %v", err)
	}
	set := make(map[string]bool, len(got))
	for _, p := range got {
		set[p] = true
	}
	if !set[filepath.Join(wt, "file.txt")] {
		t.Errorf("worktree walk missing file.txt: %v", got)
	}
	if set[filepath.Join(wt, ".git")] {
		t.Error("worktree walk must exclude the .git pointer file")
	}
}
