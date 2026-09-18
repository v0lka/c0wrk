package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
)

// genReviewLines returns n numbered lines ("l1\nl2\n…\n").
func genReviewLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "l%d\n", i)
	}
	return b.String()
}

// replaceLine swaps the 1-based lineNo of s (newline-delimited) with repl.
func replaceLine(t *testing.T, s string, lineNo int, repl string) string {
	t.Helper()
	parts := strings.Split(s, "\n")
	if lineNo < 1 || lineNo > len(parts) {
		t.Fatalf("replaceLine: lineNo %d out of range (%d lines)", lineNo, len(parts))
	}
	parts[lineNo-1] = repl
	return strings.Join(parts, "\n")
}

// hunkContextLines counts the context (leading-space) body lines in a raw
// hunk block, skipping the "@@" header and "\ No newline" markers.
func hunkContextLines(raw string) int {
	body := strings.Split(raw, "\n")
	count := 0
	for i, l := range body {
		if i == 0 { // hunk header "@@ …"
			continue
		}
		if l != "" && l[0] == ' ' {
			count++
		}
	}
	return count
}

// TestGetReviewDiff_TwoFiles asserts the RPC groups uncommitted changes per
// file with the expected hunk counts, includes staged and unstaged changes
// together (vs HEAD), and yields at least 5 context lines per hunk (-U5).
func TestGetReviewDiff_TwoFiles(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		base := genReviewLines(30)
		commitFile(t, dir, "file1.txt", base)
		commitFile(t, dir, "file2.txt", base)

		// file1: two change regions 18 lines apart => 2 distinct hunks
		// (gap > 2*5 context lines, so git does not merge them).
		f1 := replaceLine(t, base, 6, "CHG6")
		f1 = replaceLine(t, f1, 24, "CHG24")
		if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(f1), 0o644); err != nil {
			t.Fatalf("write file1: %v", err)
		}

		// file2: one change region => 1 hunk. Stage it to prove the diff
		// covers staged + unstaged changes combined against HEAD.
		f2 := replaceLine(t, base, 15, "CHG15")
		if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte(f2), 0o644); err != nil {
			t.Fatalf("write file2: %v", err)
		}
		runGit(t, dir, "add", "file2.txt")

		files, err := f.GetReviewDiff()
		if err != nil {
			t.Fatalf("GetReviewDiff: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected 2 changed files, got %d", len(files))
		}

		// Assert per-file hunk counts via a path-keyed map.
		counts := map[string]int{}
		for _, file := range files {
			counts[file.Path] = len(file.Hunks)
		}
		if counts["file1.txt"] != 2 {
			t.Errorf("file1.txt: expected 2 hunks, got %d", counts["file1.txt"])
		}
		if counts["file2.txt"] != 1 {
			t.Errorf("file2.txt: expected 1 hunk, got %d", counts["file2.txt"])
		}

		// Every hunk must carry at least 5 context lines (the -U5 effect).
		for _, file := range files {
			for _, h := range file.Hunks {
				if c := hunkContextLines(h.Raw); c < 5 {
					t.Errorf("%s hunk @ +%d: %d context lines, want >=5", file.Path, h.NewStart, c)
				}
			}
		}
	})
}

// TestGetReviewDiff_NoProject verifies the read-only RPC returns an empty
// slice (not an error) for No Project mode.
func TestGetReviewDiff_NoProject(t *testing.T) {
	f := &FrontendAPI{activeProjectID: project.NoProjectID, activeProjectPath: t.TempDir()}
	files, err := f.GetReviewDiff()
	if err != nil {
		t.Fatalf("unexpected error for No Project: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty slice for No Project, got %d files", len(files))
	}
}

// TestGetReviewDiff_NoProjectEmpty verifies that an unconfigured
// FrontendAPI (no active project) also returns an empty slice.
func TestGetReviewDiff_NoActiveProject(t *testing.T) {
	f := &FrontendAPI{}
	files, err := f.GetReviewDiff()
	if err != nil {
		t.Fatalf("unexpected error for no active project: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty slice with no active project, got %d files", len(files))
	}
}

// TestGetReviewDiff_NonGit verifies the RPC returns an empty slice for a
// workspace that is not a git repository.
func TestGetReviewDiff_NonGit(t *testing.T) {
	f := &FrontendAPI{activeProjectPath: t.TempDir()}
	files, err := f.GetReviewDiff()
	if err != nil {
		t.Fatalf("unexpected error for non-git workspace: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty slice for non-git workspace, got %d files", len(files))
	}
}

// TestGetReviewDiff_CleanTree verifies the RPC returns an empty slice when
// the working tree has no uncommitted changes relative to HEAD.
func TestGetReviewDiff_CleanTree(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, _ string) {
		files, err := f.GetReviewDiff()
		if err != nil {
			t.Fatalf("unexpected error for clean tree: %v", err)
		}
		if len(files) != 0 {
			t.Errorf("expected empty slice for clean tree, got %d files", len(files))
		}
	})
}

// TestGetReviewDiff_IncludesUntracked verifies the RPC surfaces untracked
// files (never added to the index) alongside tracked changes: `git diff
// HEAD` omits them, so BuildReviewDiff emits each as a new-file diff against
// /dev/null. Git-ignored files must still be excluded.
func TestGetReviewDiff_IncludesUntracked(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// An untracked file that must appear as a full-file addition.
		if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
			t.Fatalf("write newfile: %v", err)
		}
		// An untracked file in a subdirectory (rel path must round-trip).
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatalf("mkdir sub: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested\n"), 0o644); err != nil {
			t.Fatalf("write nested: %v", err)
		}
		// A git-ignored untracked file that must NOT appear.
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
			t.Fatalf("write gitignore: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("nope\n"), 0o644); err != nil {
			t.Fatalf("write ignored: %v", err)
		}

		files, err := f.GetReviewDiff()
		if err != nil {
			t.Fatalf("GetReviewDiff: %v", err)
		}

		// Note: .gitignore is itself untracked and therefore appears too.
		paths := map[string]bool{}
		for _, file := range files {
			paths[file.Path] = true
		}
		if !paths["newfile.txt"] {
			t.Errorf("expected untracked newfile.txt in review diff, got paths: %v", paths)
		}
		if !paths["sub/nested.txt"] {
			t.Errorf("expected untracked sub/nested.txt in review diff, got paths: %v", paths)
		}
		if paths["ignored.txt"] {
			t.Errorf("git-ignored ignored.txt must not appear in review diff, got paths: %v", paths)
		}

		// An untracked file is a pure addition: a single hunk at new line 1
		// with the whole file added, marked as new (old side empty).
		var newFile *ReviewFileDiff
		for i := range files {
			if files[i].Path == "newfile.txt" {
				newFile = &files[i]
				break
			}
		}
		if newFile == nil {
			t.Fatalf("newfile.txt missing from review diff")
		}
		if len(newFile.Hunks) != 1 {
			t.Fatalf("newfile.txt: expected 1 hunk, got %d", len(newFile.Hunks))
		}
		h := newFile.Hunks[0]
		if h.OldCount != 0 || h.NewStart != 1 {
			t.Errorf("newfile.txt hunk = old %+d new @%d count %d, want pure addition at new line 1",
				h.OldCount, h.NewStart, h.NewCount)
		}
	})
}

// TestGetReviewDiff_EmptyUntrackedFile reproduces the regression where a single
// content-less file (here an empty untracked file — git emits a "diff --git"
// header with no @@ hunk) caused parseReviewFileBlock to leave Hunks as a nil
// slice. Go serialises a nil slice as JSON null, the frontend's isArrayOf guard
// (Array.isArray) rejects that element, and because the guard checks every
// element the ENTIRE diff array was discarded — so the review page showed "No
// uncommitted changes to review" even with real changes present. The fix is
// that Hunks is always a non-nil slice (JSON []); this test pins that contract.
func TestGetReviewDiff_EmptyUntrackedFile(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// A real, content-bearing change so there is something to review.
		if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("changed\n"), 0o644); err != nil {
			t.Fatalf("write committed.txt: %v", err)
		}
		// An empty (0-byte) untracked file — git diff --no-index emits a
		// header with no @@ hunk for it.
		if err := os.WriteFile(filepath.Join(dir, "empty.txt"), []byte{}, 0o644); err != nil {
			t.Fatalf("write empty.txt: %v", err)
		}

		files, err := f.GetReviewDiff()
		if err != nil {
			t.Fatalf("GetReviewDiff: %v", err)
		}

		byPath := map[string]ReviewFileDiff{}
		for _, file := range files {
			byPath[file.Path] = file
		}

		// The empty file must NOT be silently dropped.
		empty, ok := byPath["empty.txt"]
		if !ok {
			t.Fatalf("empty.txt missing from review diff; got paths: %v", keysOf(byPath))
		}
		// Hunks must be a non-nil, zero-length slice so JSON serialises it as
		// [] (not null) and the frontend Array.isArray guard accepts it.
		if empty.Hunks == nil {
			t.Fatal("empty.txt Hunks is nil — would serialise to JSON null and poison the frontend guard")
		}
		if len(empty.Hunks) != 0 {
			t.Errorf("empty.txt: expected 0 hunks, got %d", len(empty.Hunks))
		}
		raw, err := json.Marshal(empty)
		if err != nil {
			t.Fatalf("marshal empty.txt diff: %v", err)
		}
		if !strings.Contains(string(raw), `"hunks":[]`) {
			t.Errorf("empty.txt JSON must contain \"hunks\":[], got: %s", raw)
		}

		// The regression: the content-bearing file must STILL be present even
		// though a content-less file is in the same result set.
		if _, ok := byPath["committed.txt"]; !ok {
			t.Errorf("committed.txt missing — a content-less file poisoned the result; got paths: %v", keysOf(byPath))
		}
	})
}

// keysOf returns the map keys of m (test helper, order not guaranteed).
func keysOf(m map[string]ReviewFileDiff) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- GetCommitDiff ---

// TestGetCommitDiff_TwoFiles asserts the RPC returns the per-file hunk diff
// introduced by a single commit (relative to its parent), grouped per file
// with at least 5 context lines per hunk (-U5).
func TestGetCommitDiff_TwoFiles(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		base := genReviewLines(30)
		commitFile(t, dir, "file1.txt", base)
		commitFile(t, dir, "file2.txt", base)

		// file1: two change regions 18 lines apart => 2 distinct hunks.
		f1 := replaceLine(t, base, 6, "CHG6")
		f1 = replaceLine(t, f1, 24, "CHG24")
		if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(f1), 0o644); err != nil {
			t.Fatalf("write file1: %v", err)
		}

		// file2: one change region => 1 hunk.
		f2 := replaceLine(t, base, 15, "CHG15")
		if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte(f2), 0o644); err != nil {
			t.Fatalf("write file2: %v", err)
		}
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-m", "two-file changes")

		sha := gitOut(t, dir, "rev-parse", "HEAD")

		files, err := f.GetCommitDiff(sha)
		if err != nil {
			t.Fatalf("GetCommitDiff: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected 2 changed files, got %d", len(files))
		}

		counts := map[string]int{}
		for _, file := range files {
			counts[file.Path] = len(file.Hunks)
		}
		if counts["file1.txt"] != 2 {
			t.Errorf("file1.txt: expected 2 hunks, got %d", counts["file1.txt"])
		}
		if counts["file2.txt"] != 1 {
			t.Errorf("file2.txt: expected 1 hunk, got %d", counts["file2.txt"])
		}

		// Every hunk must carry at least 5 context lines (the -U5 effect).
		for _, file := range files {
			for _, h := range file.Hunks {
				if c := hunkContextLines(h.Raw); c < 5 {
					t.Errorf("%s hunk @ +%d: %d context lines, want >=5", file.Path, h.NewStart, c)
				}
			}
		}
	})
}

// TestGetCommitDiff_RootCommit verifies the RPC returns all files as added
// for a root commit (no parent), via the --root flag.
func TestGetCommitDiff_RootCommit(t *testing.T) {
	tmpDir := t.TempDir()
	gitInit(t, tmpDir)
	if err := os.WriteFile(filepath.Join(tmpDir, "init.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	runGit(t, tmpDir, "add", "init.txt")
	runGit(t, tmpDir, "commit", "-m", "initial")

	f := &FrontendAPI{activeProjectPath: tmpDir}
	sha := gitOut(t, tmpDir, "rev-parse", "HEAD")

	files, err := f.GetCommitDiff(sha)
	if err != nil {
		t.Fatalf("GetCommitDiff: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file for root commit, got %d", len(files))
	}
	if files[0].Path != "init.txt" {
		t.Errorf("expected init.txt, got %s", files[0].Path)
	}
	if len(files[0].Hunks) != 1 {
		t.Errorf("expected 1 hunk, got %d", len(files[0].Hunks))
	}
}

// TestGetCommitDiff_EmptySHA verifies the RPC rejects an empty/whitespace SHA.
func TestGetCommitDiff_EmptySHA(t *testing.T) {
	f := &FrontendAPI{activeProjectPath: t.TempDir()}
	if _, err := f.GetCommitDiff(""); err == nil {
		t.Fatal("expected error for empty sha")
	}
	if _, err := f.GetCommitDiff("   "); err == nil {
		t.Fatal("expected error for whitespace-only sha")
	}
}

// TestGetCommitDiff_InvalidSha verifies the RPC rejects a non-hex SHA.
func TestGetCommitDiff_InvalidSha(t *testing.T) {
	f := &FrontendAPI{activeProjectPath: t.TempDir()}
	if _, err := f.GetCommitDiff("not-a-sha"); err == nil {
		t.Fatal("expected error for non-hex SHA")
	}
}

// TestGetCommitDiff_NoProject verifies the RPC returns an error for No
// Project mode (a commit diff requires a git repository).
func TestGetCommitDiff_NoProject(t *testing.T) {
	f := &FrontendAPI{activeProjectID: project.NoProjectID, activeProjectPath: t.TempDir()}
	if _, err := f.GetCommitDiff("abcdef1234"); err == nil {
		t.Fatal("expected error for No Project")
	}
}
