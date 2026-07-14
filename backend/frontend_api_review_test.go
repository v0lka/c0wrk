package backend

import (
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
