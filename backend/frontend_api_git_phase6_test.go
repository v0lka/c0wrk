package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/workspace"
)

// ---------------------------------------------------------------------------
// Phase 6 tests: discard, gitignore, merge/rebase, commit graph, hunk staging
// ---------------------------------------------------------------------------
//
// Deterministic parser/helper tests (no git) use table-driven subtests and
// cmp.Diff for struct/slice comparisons. Integration tests exercise the real
// git binary through the FrontendAPI RPCs using the shared withGitRepo /
// commitFile / gitOut / gitDefaultBranch helpers defined in this package.

// ---------------------------------------------------------------------------
// parseGitRefs — deterministic, no git
// ---------------------------------------------------------------------------

func TestParseGitRefs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty", input: "", want: []string{}},
		{name: "whitespace", input: "   ", want: []string{}},
		{name: "empty parens", input: "()", want: []string{}},
		{name: "single ref", input: "(HEAD -> main)", want: []string{"HEAD -> main"}},
		{name: "multiple refs", input: "(HEAD -> main, tag: v1.0, origin/main)", want: []string{"HEAD -> main", "tag: v1.0", "origin/main"}},
		{name: "no parens passthrough", input: "HEAD -> main", want: []string{"HEAD -> main"}},
		{name: "trims surrounding spaces", input: "  (  tag: v1.0  )  ", want: []string{"tag: v1.0"}},
		{name: "skips empty comma entries", input: "(HEAD -> main, , tag: v1.0)", want: []string{"HEAD -> main", "tag: v1.0"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitRefs(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseGitRefs(%q) mismatch (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// hunkInRange — deterministic, no git
// ---------------------------------------------------------------------------

func TestHunkInRange(t *testing.T) {
	tests := []struct {
		name      string
		oldStart  int
		oldCount  int
		ranges    []HunkRange
		wantMatch bool
	}{
		{name: "exact match", oldStart: 1, oldCount: 3, ranges: []HunkRange{{StartLine: 1, EndLine: 3}}, wantMatch: true},
		{name: "end mismatch", oldStart: 1, oldCount: 3, ranges: []HunkRange{{StartLine: 1, EndLine: 2}}, wantMatch: false},
		{name: "start mismatch", oldStart: 2, oldCount: 3, ranges: []HunkRange{{StartLine: 1, EndLine: 4}}, wantMatch: false},
		{name: "one of several matches", oldStart: 10, oldCount: 5, ranges: []HunkRange{{StartLine: 1, EndLine: 1}, {StartLine: 10, EndLine: 14}}, wantMatch: true},
		{name: "none match", oldStart: 10, oldCount: 5, ranges: []HunkRange{{StartLine: 1, EndLine: 1}, {StartLine: 20, EndLine: 24}}, wantMatch: false},
		{name: "default count 1", oldStart: 7, oldCount: 1, ranges: []HunkRange{{StartLine: 7, EndLine: 7}}, wantMatch: true},
		{name: "pure addition zero old lines", oldStart: 5, oldCount: 0, ranges: []HunkRange{{StartLine: 5, EndLine: 4}}, wantMatch: true},
		{name: "empty ranges", oldStart: 1, oldCount: 1, ranges: nil, wantMatch: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hunkInRange(tc.oldStart, tc.oldCount, tc.ranges)
			if got != tc.wantMatch {
				t.Errorf("hunkInRange(start=%d, count=%d, ranges=%v) = %v, want %v",
					tc.oldStart, tc.oldCount, tc.ranges, got, tc.wantMatch)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildHunkPatch — deterministic, no git
// ---------------------------------------------------------------------------

const twoHunkDiff = `diff --git a/file.txt b/file.txt
index 1111111..2222222 100644
--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,3 @@
 l1
-l2
+L2
 l3
@@ -10,3 +10,3 @@
 l10
-l11
+L11
 l12
`

func TestBuildHunkPatch(t *testing.T) {
	tests := []struct {
		name     string
		diff     string
		ranges   []HunkRange
		wantSel  int
		wantCont []string // substrings that must appear in the patch
		wantNot  []string // substrings that must NOT appear in the patch
	}{
		{
			name:     "select first hunk only",
			diff:     twoHunkDiff,
			ranges:   []HunkRange{{StartLine: 1, EndLine: 3}},
			wantSel:  1,
			wantCont: []string{"--- a/file.txt", "+++ b/file.txt", "@@ -1,3 +1,3 @@", "+L2"},
			wantNot:  []string{"+L11", "@@ -10,3 +10,3 @@"},
		},
		{
			name:     "select second hunk only",
			diff:     twoHunkDiff,
			ranges:   []HunkRange{{StartLine: 10, EndLine: 12}},
			wantSel:  1,
			wantCont: []string{"@@ -10,3 +10,3 @@", "+L11"},
			wantNot:  []string{"+L2", "@@ -1,3 +1,3 @@"}, // file header is shared by all hunks
		},
		{
			name:     "select both hunks",
			diff:     twoHunkDiff,
			ranges:   []HunkRange{{StartLine: 1, EndLine: 3}, {StartLine: 10, EndLine: 12}},
			wantSel:  2,
			wantCont: []string{"+L2", "+L11"},
			wantNot:  nil,
		},
		{
			name:     "no matching ranges",
			diff:     twoHunkDiff,
			ranges:   []HunkRange{{StartLine: 50, EndLine: 60}},
			wantSel:  0,
			wantCont: nil,
			wantNot:  []string{"+L2", "+L11", "@@ -1,3", "@@ -10,3"},
		},
		{
			name:     "empty diff",
			diff:     "",
			ranges:   []HunkRange{{StartLine: 1, EndLine: 1}},
			wantSel:  0,
			wantCont: nil,
			wantNot:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			patch, sel, err := buildHunkPatch(tc.diff, tc.ranges)
			if err != nil {
				t.Fatalf("buildHunkPatch: unexpected error: %v", err)
			}
			if sel != tc.wantSel {
				t.Errorf("selected: got %d, want %d", sel, tc.wantSel)
			}
			for _, s := range tc.wantCont {
				if !strings.Contains(patch, s) {
					t.Errorf("patch missing %q\npatch:\n%s", s, patch)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(patch, s) {
					t.Errorf("patch unexpectedly contains %q\npatch:\n%s", s, patch)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// patternAlreadyIgnored — deterministic, no git
// ---------------------------------------------------------------------------

func TestPatternAlreadyIgnored(t *testing.T) {
	tests := []struct {
		name    string
		content string
		pattern string
		want    bool
	}{
		{name: "empty content", content: "", pattern: "build/", want: false},
		{name: "exact match", content: "node_modules/\nbuild/\n", pattern: "build/", want: true},
		{name: "match with trailing whitespace", content: "build/   \n", pattern: "build/", want: true},
		{name: "match with leading whitespace", content: "  build/\n", pattern: "build/", want: true},
		{name: "no match", content: "node_modules/\n", pattern: "build/", want: false},
		{name: "substring is not a match", content: "build-cache/\n", pattern: "build/", want: false},
		{name: "single line no newline", content: "*.log", pattern: "*.log", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := patternAlreadyIgnored(tc.content, tc.pattern)
			if got != tc.want {
				t.Errorf("patternAlreadyIgnored(%q, %q) = %v, want %v", tc.content, tc.pattern, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isRebaseActive — deterministic filesystem, no git
// ---------------------------------------------------------------------------

func TestIsRebaseActive(t *testing.T) {
	t.Run("no rebase dirs", func(t *testing.T) {
		dir := t.TempDir()
		if isRebaseActive(dir) {
			t.Errorf("isRebaseActive(empty dir) = true, want false")
		}
	})

	t.Run("rebase-apply dir present", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "rebase-apply"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if !isRebaseActive(dir) {
			t.Errorf("isRebaseActive(rebase-apply) = false, want true")
		}
	})

	t.Run("rebase-merge dir present", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "rebase-merge"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if !isRebaseActive(dir) {
			t.Errorf("isRebaseActive(rebase-merge) = false, want true")
		}
	})

	t.Run("file named rebase-apply is not a rebase", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "rebase-apply"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if isRebaseActive(dir) {
			t.Errorf("isRebaseActive(file) = true, want false")
		}
	})
}

// ---------------------------------------------------------------------------
// DiscardChanges — integration
// ---------------------------------------------------------------------------

func TestDiscardChanges_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.DiscardChanges("/some/file.txt"); err == nil {
		t.Fatal("DiscardChanges: expected error when no active project")
	}
}

func TestDiscardChanges_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: project.NoProjectID, activeProjectPath: t.TempDir()}
	if err := f.DiscardChanges(filepath.Join(f.activeProjectPath, "file.txt")); err == nil {
		t.Fatal("DiscardChanges: expected error for No Project mode")
	}
}

func TestDiscardChanges_TrackedFileRestored(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Stage it so DiscardChanges must reset+checkout.
		runGit(t, dir, "add", "committed.txt")

		if err := f.DiscardChanges(path); err != nil {
			t.Fatalf("DiscardChanges: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != "v1\n" {
			t.Errorf("after discard: committed.txt = %q, want %q", string(got), "v1\n")
		}
		// No staged or unstaged changes remain.
		status, err := workspace.GitStatus(context.Background(), dir)
		if err != nil {
			t.Fatalf("GitStatus: %v", err)
		}
		if len(status) != 0 {
			t.Errorf("after discard: status = %v, want empty", status)
		}
	})
}

func TestDiscardChanges_UntrackedFileRemoved(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "untracked.txt")
		if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.DiscardChanges(path); err != nil {
			t.Fatalf("DiscardChanges: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("after discard: untracked.txt should be removed, stat err: %v", err)
		}
	})
}

func TestDiscardChanges_NoChangesIsNoop(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		if err := f.DiscardChanges(path); err != nil {
			t.Fatalf("DiscardChanges (clean): %v", err)
		}
	})
}

func TestDiscardChanges_PathOutsideWorkspace(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.DiscardChanges("/__not__in__workspace__/file.txt"); err == nil {
			t.Fatal("DiscardChanges: expected error for path outside workspace")
		}
	})
}

func TestDiscardChanges_EmitsStatusChanged(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted, _ = args[0].(string)
			}
		}
		path := filepath.Join(dir, "untracked.txt")
		if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.DiscardChanges(path); err != nil {
			t.Fatalf("DiscardChanges: %v", err)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

// ---------------------------------------------------------------------------
// AppendToGitignore — integration
// ---------------------------------------------------------------------------

func TestAppendToGitignore_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.AppendToGitignore("build/"); err == nil {
		t.Fatal("AppendToGitignore: expected error when no active project")
	}
}

func TestAppendToGitignore_EmptyPattern(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.AppendToGitignore("   "); err == nil {
			t.Fatal("AppendToGitignore: expected error for empty pattern")
		}
	})
}

func TestAppendToGitignore_CreatesFileAndAppends(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.AppendToGitignore("build/"); err != nil {
			t.Fatalf("AppendToGitignore: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if string(got) != "build/\n" {
			t.Errorf(".gitignore = %q, want %q", string(got), "build/\n")
		}
	})
}

func TestAppendToGitignore_AppendsSecondPattern(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.AppendToGitignore("build/"); err != nil {
			t.Fatalf("first: %v", err)
		}
		if err := f.AppendToGitignore("*.log"); err != nil {
			t.Fatalf("second: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if string(got) != "build/\n*.log\n" {
			t.Errorf(".gitignore = %q, want %q", string(got), "build/\n*.log\n")
		}
	})
}

func TestAppendToGitignore_DedupIsNoop(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.AppendToGitignore("build/"); err != nil {
			t.Fatalf("AppendToGitignore (dedup): %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if string(got) != "build/\n" {
			t.Errorf("dedup .gitignore = %q, want %q", string(got), "build/\n")
		}
	})
}

func TestAppendToGitignore_EmitsStatusChanged(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted, _ = args[0].(string)
			}
		}
		if err := f.AppendToGitignore("build/"); err != nil {
			t.Fatalf("AppendToGitignore: %v", err)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

// ---------------------------------------------------------------------------
// Merge / Rebase — integration
// ---------------------------------------------------------------------------

func TestMerge_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.Merge("topic"); err == nil {
		t.Fatal("Merge: expected error when no active project")
	}
}

func TestMerge_EmptyBranch(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, _ string) {
		if err := f.Merge("   "); err == nil {
			t.Fatal("Merge: expected error for empty branch name")
		}
	})
}

func TestMerge_FastForwardSuccess(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		branch := gitDefaultBranch(t, dir)
		gitOut(t, dir, "branch", "topic")
		gitOut(t, dir, "checkout", "topic")
		commitFile(t, dir, "topic.txt", "t\n")
		gitOut(t, dir, "checkout", branch)

		if err := f.Merge("topic"); err != nil {
			t.Fatalf("Merge: %v", err)
		}
		// Fast-forward: topic.txt now present on the current branch.
		if _, err := os.Stat(filepath.Join(dir, "topic.txt")); err != nil {
			t.Errorf("after merge: topic.txt should exist, stat err: %v", err)
		}
	})
}

func TestMerge_ConflictReturnsError(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		branch := gitDefaultBranch(t, dir)
		gitOut(t, dir, "branch", "topic")
		gitOut(t, dir, "checkout", "topic")
		commitFile(t, dir, "committed.txt", "topic-change\n")
		gitOut(t, dir, "checkout", branch)
		commitFile(t, dir, "committed.txt", "main-change\n")

		if err := f.Merge("topic"); err == nil {
			t.Fatal("Merge: expected error on conflict")
		}
	})
}

func TestRebase_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.Rebase("main"); err == nil {
		t.Fatal("Rebase: expected error when no active project")
	}
}

func TestRebase_EmptyBranch(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, _ string) {
		if err := f.Rebase("   "); err == nil {
			t.Fatal("Rebase: expected error for empty branch name")
		}
	})
}

func TestRebase_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		branch := gitDefaultBranch(t, dir)
		gitOut(t, dir, "branch", "topic")
		gitOut(t, dir, "checkout", "topic")
		commitFile(t, dir, "topic.txt", "t\n")
		gitOut(t, dir, "checkout", branch)
		commitFile(t, dir, "main2.txt", "m\n") // divergent, different file → no conflict
		gitOut(t, dir, "checkout", "topic")

		if err := f.Rebase(branch); err != nil {
			t.Fatalf("Rebase: %v", err)
		}
		// After rebase onto main, topic includes main's commit (main2.txt).
		if _, err := os.Stat(filepath.Join(dir, "main2.txt")); err != nil {
			t.Errorf("after rebase: main2.txt should exist, stat err: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "topic.txt")); err != nil {
			t.Errorf("after rebase: topic.txt should still exist, stat err: %v", err)
		}
	})
}

func TestRebase_ConflictReturnsError(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		branch := gitDefaultBranch(t, dir)
		gitOut(t, dir, "branch", "topic")
		gitOut(t, dir, "checkout", "topic")
		commitFile(t, dir, "committed.txt", "topic-change\n")
		gitOut(t, dir, "checkout", branch)
		commitFile(t, dir, "committed.txt", "main-change\n")
		gitOut(t, dir, "checkout", "topic")

		if err := f.Rebase(branch); err == nil {
			t.Fatal("Rebase: expected error on conflict")
		}
	})
}

// ---------------------------------------------------------------------------
// AbortMerge / AbortRebase — integration
// ---------------------------------------------------------------------------

func TestAbortMerge_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.AbortMerge(); err == nil {
		t.Fatal("AbortMerge: expected error when no active project")
	}
}

func TestAbortMerge_CleansUpInProgressMerge(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		branch := gitDefaultBranch(t, dir)
		gitOut(t, dir, "branch", "topic")
		gitOut(t, dir, "checkout", "topic")
		commitFile(t, dir, "committed.txt", "topic-change\n")
		gitOut(t, dir, "checkout", branch)
		commitFile(t, dir, "committed.txt", "main-change\n")
		// Trigger a conflicting merge (error expected) to start a merge.
		if err := f.Merge("topic"); err == nil {
			t.Fatal("setup merge: expected conflict error")
		}

		// Merge is in progress.
		state, err := f.GetRebaseMergeState()
		if err != nil {
			t.Fatalf("GetRebaseMergeState: %v", err)
		}
		if !state.IsMerging {
			t.Errorf("before abort: IsMerging = false, want true")
		}

		if err := f.AbortMerge(); err != nil {
			t.Fatalf("AbortMerge: %v", err)
		}

		// committed.txt restored to the pre-merge (main) state.
		got, err := os.ReadFile(filepath.Join(dir, "committed.txt"))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != "main-change\n" {
			t.Errorf("after abort: committed.txt = %q, want %q", string(got), "main-change\n")
		}

		// No merge in progress anymore.
		state, err = f.GetRebaseMergeState()
		if err != nil {
			t.Fatalf("GetRebaseMergeState (after): %v", err)
		}
		if state.IsMerging {
			t.Errorf("after abort: IsMerging = true, want false")
		}
	})
}

func TestAbortRebase_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.AbortRebase(); err == nil {
		t.Fatal("AbortRebase: expected error when no active project")
	}
}

func TestAbortRebase_CleansUpInProgressRebase(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		branch := gitDefaultBranch(t, dir)
		gitOut(t, dir, "branch", "topic")
		gitOut(t, dir, "checkout", "topic")
		commitFile(t, dir, "committed.txt", "topic-change\n")
		gitOut(t, dir, "checkout", branch)
		commitFile(t, dir, "committed.txt", "main-change\n")
		gitOut(t, dir, "checkout", "topic")
		// Trigger a conflicting rebase (error expected) to start a rebase.
		if err := f.Rebase(branch); err == nil {
			t.Fatal("setup rebase: expected conflict error")
		}

		state, err := f.GetRebaseMergeState()
		if err != nil {
			t.Fatalf("GetRebaseMergeState: %v", err)
		}
		if !state.IsRebasing {
			t.Errorf("before abort: IsRebasing = false, want true")
		}

		if err := f.AbortRebase(); err != nil {
			t.Fatalf("AbortRebase: %v", err)
		}

		state, err = f.GetRebaseMergeState()
		if err != nil {
			t.Fatalf("GetRebaseMergeState (after): %v", err)
		}
		if state.IsRebasing {
			t.Errorf("after abort: IsRebasing = true, want false")
		}
	})
}

// ---------------------------------------------------------------------------
// GetRebaseMergeState — integration
// ---------------------------------------------------------------------------

func TestGetRebaseMergeState_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.GetRebaseMergeState(); err == nil {
		t.Fatal("GetRebaseMergeState: expected error when no active project")
	}
}

func TestGetRebaseMergeState_CleanState(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, _ string) {
		state, err := f.GetRebaseMergeState()
		if err != nil {
			t.Fatalf("GetRebaseMergeState: %v", err)
		}
		if diff := cmp.Diff(MergeRebaseState{}, state); diff != "" {
			t.Errorf("GetRebaseMergeState mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestGetRebaseMergeState_DoesNotEmit(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, _ string) {
		emitted := false
		f.emitEvent = func(name string, _ ...any) {
			if name == EventGitStatusChanged {
				emitted = true
			}
		}
		if _, err := f.GetRebaseMergeState(); err != nil {
			t.Fatalf("GetRebaseMergeState: %v", err)
		}
		if emitted {
			t.Error("GetRebaseMergeState emitted git:status_changed, want no emit")
		}
	})
}

// ---------------------------------------------------------------------------
// StageHunks — integration
// ---------------------------------------------------------------------------

// firstHunkRange derives the HunkRange (old-file coordinates) for the first
// hunk in `git diff -- <relPath>` output, so the test adapts to git's actual
// context-line counts rather than guessing them.
func firstHunkRange(t *testing.T, dir, relPath string) HunkRange {
	t.Helper()
	out := gitOut(t, dir, "diff", "--", relPath)
	m := regexp.MustCompile(`@@ -(\d+),(\d+) `).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no hunk header in diff:\n%s", out)
	}
	start, count := atoi(t, m[1]), atoi(t, m[2])
	return HunkRange{StartLine: start, EndLine: start + count - 1}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("atoi(%q): not a digit", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func TestStageHunks_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.StageHunks("/some/file.txt", []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Fatal("StageHunks: expected error when no active project")
	}
}

func TestStageHunks_EmptyHunksIsNoop(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Empty hunk slice short-circuits before touching git.
		if err := f.StageHunks(path, nil); err != nil {
			t.Fatalf("StageHunks (empty): %v", err)
		}
	})
}

func TestStageHunks_NoUnstagedChanges(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		if err := f.StageHunks(path, []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
			t.Fatal("StageHunks: expected error when there are no unstaged changes")
		}
	})
}

func TestStageHunks_NoMatchingRanges(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "multi.txt")
		if err := os.WriteFile(path, []byte("l1\nl2\nl3\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "multi.txt")
		runGit(t, dir, "commit", "-m", "add multi")
		if err := os.WriteFile(path, []byte("L1\nl2\nl3\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// A range that matches no hunk.
		if err := f.StageHunks(path, []HunkRange{{StartLine: 500, EndLine: 600}}); err == nil {
			t.Fatal("StageHunks: expected error when no hunks match")
		}
	})
}

func TestStageHunks_StagesSingleHunkOfTwo(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "multi.txt")
		// 20 lines; modify line 1 and line 20 — far enough apart to produce
		// two separate hunks with default (3-line) context.
		var orig strings.Builder
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(&orig, "l%d\n", i)
		}
		if err := os.WriteFile(path, []byte(orig.String()), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "multi.txt")
		runGit(t, dir, "commit", "-m", "add multi")

		var modified strings.Builder
		for i := 1; i <= 20; i++ {
			switch i {
			case 1:
				modified.WriteString("L1\n")
			case 20:
				modified.WriteString("L20\n")
			default:
				fmt.Fprintf(&modified, "l%d\n", i)
			}
		}
		if err := os.WriteFile(path, []byte(modified.String()), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		// Sanity: two hunks present.
		diff := gitOut(t, dir, "diff", "--", "multi.txt")
		if got := strings.Count(diff, "\n@@ -"); got != 2 && strings.Count(diff, "@@ -") != 2 {
			t.Fatalf("expected 2 hunks, got diff:\n%s", diff)
		}

		first := firstHunkRange(t, dir, "multi.txt")
		if err := f.StageHunks(path, []HunkRange{first}); err != nil {
			t.Fatalf("StageHunks: %v", err)
		}

		// Staged diff contains the first change (L1) but not the second (L20).
		staged := gitOut(t, dir, "diff", "--cached", "--", "multi.txt")
		if !strings.Contains(staged, "+L1") {
			t.Errorf("staged diff missing +L1\n%s", staged)
		}
		if strings.Contains(staged, "+L20") {
			t.Errorf("staged diff should not contain +L20\n%s", staged)
		}
		// Unstaged diff still contains the second change (L20).
		unstaged := gitOut(t, dir, "diff", "--", "multi.txt")
		if !strings.Contains(unstaged, "+L20") {
			t.Errorf("unstaged diff missing +L20\n%s", unstaged)
		}
	})
}

func TestStageHunks_EmitsStatusChanged(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted, _ = args[0].(string)
			}
		}
		path := filepath.Join(dir, "multi.txt")
		if err := os.WriteFile(path, []byte("l1\nl2\nl3\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "multi.txt")
		runGit(t, dir, "commit", "-m", "add multi")
		if err := os.WriteFile(path, []byte("L1\nl2\nl3\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		r := firstHunkRange(t, dir, "multi.txt")
		if err := f.StageHunks(path, []HunkRange{r}); err != nil {
			t.Fatalf("StageHunks: %v", err)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

// ---------------------------------------------------------------------------
// Phase 6 error paths — no project / No Project mode
// ---------------------------------------------------------------------------

func TestPhase6Git_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.DiscardChanges("/f.txt"); err == nil {
		t.Error("DiscardChanges: expected error")
	}
	if err := f.AppendToGitignore("x"); err == nil {
		t.Error("AppendToGitignore: expected error")
	}
	if err := f.Merge("b"); err == nil {
		t.Error("Merge: expected error")
	}
	if err := f.Rebase("b"); err == nil {
		t.Error("Rebase: expected error")
	}
	if err := f.AbortMerge(); err == nil {
		t.Error("AbortMerge: expected error")
	}
	if err := f.AbortRebase(); err == nil {
		t.Error("AbortRebase: expected error")
	}
	if _, err := f.GetRebaseMergeState(); err == nil {
		t.Error("GetRebaseMergeState: expected error")
	}
	if err := f.StageHunks("/f.txt", []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Error("StageHunks: expected error")
	}
	if err := f.UnstageHunks("/f.txt", []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Error("UnstageHunks: expected error")
	}
	if err := f.DiscardHunks("/f.txt", []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Error("DiscardHunks: expected error")
	}
	if _, err := f.GetFileDiffHunks("/f.txt"); err == nil {
		t.Error("GetFileDiffHunks: expected error")
	}
}

func TestPhase6Git_NoProjectMode(t *testing.T) {
	f := &FrontendAPI{activeProjectID: project.NoProjectID, activeProjectPath: t.TempDir()}
	if err := f.DiscardChanges(filepath.Join(f.activeProjectPath, "f.txt")); err == nil {
		t.Error("DiscardChanges: expected error")
	}
	if err := f.AppendToGitignore("x"); err == nil {
		t.Error("AppendToGitignore: expected error")
	}
	if err := f.Merge("b"); err == nil {
		t.Error("Merge: expected error")
	}
	if err := f.Rebase("b"); err == nil {
		t.Error("Rebase: expected error")
	}
	if err := f.AbortMerge(); err == nil {
		t.Error("AbortMerge: expected error")
	}
	if err := f.AbortRebase(); err == nil {
		t.Error("AbortRebase: expected error")
	}
	if _, err := f.GetRebaseMergeState(); err == nil {
		t.Error("GetRebaseMergeState: expected error")
	}
	if err := f.StageHunks(filepath.Join(f.activeProjectPath, "f.txt"), []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Error("StageHunks: expected error")
	}
	if err := f.UnstageHunks(filepath.Join(f.activeProjectPath, "f.txt"), []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Error("UnstageHunks: expected error")
	}
	if err := f.DiscardHunks(filepath.Join(f.activeProjectPath, "f.txt"), []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Error("DiscardHunks: expected error")
	}
	if _, err := f.GetFileDiffHunks(filepath.Join(f.activeProjectPath, "f.txt")); err == nil {
		t.Error("GetFileDiffHunks: expected error")
	}
}

// ---------------------------------------------------------------------------
// parseHunkInfos — deterministic, no git
// ---------------------------------------------------------------------------

func TestParseHunkInfos(t *testing.T) {
	t.Run("empty diff returns nil", func(t *testing.T) {
		if got := parseHunkInfos("", true); got != nil {
			t.Errorf("parseHunkInfos('') = %v, want nil", got)
		}
	})

	t.Run("parses two hunks with staged flag", func(t *testing.T) {
		hunks := parseHunkInfos(twoHunkDiff, true)
		if len(hunks) != 2 {
			t.Fatalf("expected 2 hunks, got %d", len(hunks))
		}
		if !hunks[0].Staged {
			t.Error("hunk[0].Staged = false, want true")
		}
		if hunks[0].OldStart != 1 || hunks[0].OldCount != 3 {
			t.Errorf("hunk[0] old: start=%d count=%d, want 1/3", hunks[0].OldStart, hunks[0].OldCount)
		}
		if hunks[0].NewStart != 1 || hunks[0].NewCount != 3 {
			t.Errorf("hunk[0] new: start=%d count=%d, want 1/3", hunks[0].NewStart, hunks[0].NewCount)
		}
		// Change-start excludes the 1 leading context line: header says
		// -1 but the first changed line (-l2) is at old line 2.
		if hunks[0].OldChangeStart != 2 || hunks[0].NewChangeStart != 2 {
			t.Errorf("hunk[0] change-start: old=%d new=%d, want 2/2",
				hunks[0].OldChangeStart, hunks[0].NewChangeStart)
		}
		if !strings.Contains(hunks[0].Diff, "@@ -1,3 +1,3 @@") {
			t.Errorf("hunk[0].Diff missing header:\n%s", hunks[0].Diff)
		}
		if !strings.Contains(hunks[0].Diff, "+L2") {
			t.Errorf("hunk[0].Diff missing +L2:\n%s", hunks[0].Diff)
		}
		if hunks[1].OldStart != 10 || hunks[1].OldCount != 3 {
			t.Errorf("hunk[1] old: start=%d count=%d, want 10/3", hunks[1].OldStart, hunks[1].OldCount)
		}
		if hunks[1].OldChangeStart != 11 || hunks[1].NewChangeStart != 11 {
			t.Errorf("hunk[1] change-start: old=%d new=%d, want 11/11",
				hunks[1].OldChangeStart, hunks[1].NewChangeStart)
		}
	})

	t.Run("change-start skips 3 context lines", func(t *testing.T) {
		// A hunk with the default 3 context lines before the change.
		// Header says -10 but the first '-' line is at old line 13.
		const diff = `diff --git a/f b/f
--- a/f
+++ b/f
@@ -10,7 +10,7 @@
 l10
 l11
 l12
-l13
+L13
 l14
 l15
 l16
`
		hunks := parseHunkInfos(diff, false)
		if len(hunks) != 1 {
			t.Fatalf("expected 1 hunk, got %d", len(hunks))
		}
		if hunks[0].OldStart != 10 {
			t.Errorf("OldStart=%d, want 10", hunks[0].OldStart)
		}
		if hunks[0].OldChangeStart != 13 || hunks[0].NewChangeStart != 13 {
			t.Errorf("change-start: old=%d new=%d, want 13/13",
				hunks[0].OldChangeStart, hunks[0].NewChangeStart)
		}
	})

	t.Run("change-start for pure addition", func(t *testing.T) {
		// @@ -5,0 +6,2 @@ — insertion after old line 5.
		const diff = `diff --git a/f b/f
--- a/f
+++ b/f
@@ -5,0 +6,2 @@
+new1
+new2
`
		hunks := parseHunkInfos(diff, false)
		if len(hunks) != 1 {
			t.Fatalf("expected 1 hunk, got %d", len(hunks))
		}
		// oldChangeStart = 5 (line before insertion), newChangeStart = 6.
		if hunks[0].OldChangeStart != 5 || hunks[0].NewChangeStart != 6 {
			t.Errorf("change-start: old=%d new=%d, want 5/6",
				hunks[0].OldChangeStart, hunks[0].NewChangeStart)
		}
	})

	t.Run("unstaged flag is false", func(t *testing.T) {
		hunks := parseHunkInfos(twoHunkDiff, false)
		if len(hunks) == 0 {
			t.Fatal("expected hunks, got none")
		}
		if hunks[0].Staged {
			t.Error("hunk[0].Staged = true, want false")
		}
	})
}

// ---------------------------------------------------------------------------
// GetFileDiffHunks — integration
// ---------------------------------------------------------------------------

func TestGetFileDiffHunks_StagedAndUnstaged(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "multi.txt")
		commitFile(t, dir, "multi.txt", "l1\nl2\nl3\nl4\nl5\n")

		// Stage a change to line 1.
		if err := os.WriteFile(path, []byte("L1\nl2\nl3\nl4\nl5\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "multi.txt")

		// Make an additional unstaged change to line 5.
		if err := os.WriteFile(path, []byte("L1\nl2\nl3\nl4\nL5\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		hunks, err := f.GetFileDiffHunks(path)
		if err != nil {
			t.Fatalf("GetFileDiffHunks: %v", err)
		}
		if len(hunks) != 2 {
			t.Fatalf("expected 2 hunks (1 staged + 1 unstaged), got %d", len(hunks))
		}

		var staged, unstaged int
		for _, h := range hunks {
			if h.Staged {
				staged++
				if !strings.Contains(h.Diff, "+L1") {
					t.Errorf("staged hunk diff missing +L1:\n%s", h.Diff)
				}
			} else {
				unstaged++
				if !strings.Contains(h.Diff, "+L5") {
					t.Errorf("unstaged hunk diff missing +L5:\n%s", h.Diff)
				}
			}
		}
		if staged != 1 {
			t.Errorf("expected 1 staged hunk, got %d", staged)
		}
		if unstaged != 1 {
			t.Errorf("expected 1 unstaged hunk, got %d", unstaged)
		}
	})
}

func TestGetFileDiffHunks_NoChanges(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		hunks, err := f.GetFileDiffHunks(path)
		if err != nil {
			t.Fatalf("GetFileDiffHunks: %v", err)
		}
		if len(hunks) != 0 {
			t.Errorf("expected 0 hunks for clean file, got %d", len(hunks))
		}
	})
}

// ---------------------------------------------------------------------------
// UnstageHunks — integration
// ---------------------------------------------------------------------------

func TestUnstageHunks_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.UnstageHunks("/some/file.txt", []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Fatal("UnstageHunks: expected error when no active project")
	}
}

func TestUnstageHunks_EmptyHunksIsNoop(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		if err := f.UnstageHunks(path, nil); err != nil {
			t.Fatalf("UnstageHunks (empty): %v", err)
		}
	})
}

func TestUnstageHunks_NoStagedChanges(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Not staged — only unstaged changes exist.
		if err := f.UnstageHunks(path, []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
			t.Fatal("UnstageHunks: expected error when there are no staged changes")
		}
	})
}

func TestUnstageHunks_UnstagesSingleHunk(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "multi.txt")
		var orig strings.Builder
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(&orig, "l%d\n", i)
		}
		if err := os.WriteFile(path, []byte(orig.String()), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "multi.txt")
		runGit(t, dir, "commit", "-m", "add multi")

		var modified strings.Builder
		for i := 1; i <= 20; i++ {
			switch i {
			case 1:
				modified.WriteString("L1\n")
			case 20:
				modified.WriteString("L20\n")
			default:
				fmt.Fprintf(&modified, "l%d\n", i)
			}
		}
		if err := os.WriteFile(path, []byte(modified.String()), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Stage both hunks first.
		runGit(t, dir, "add", "multi.txt")

		// Derive the first hunk's range from the staged diff.
		stagedDiff := gitOut(t, dir, "diff", "--cached", "--", "multi.txt")
		m := regexp.MustCompile(`@@ -(\d+),(\d+) `).FindStringSubmatch(stagedDiff)
		if m == nil {
			t.Fatalf("no hunk header in staged diff:\n%s", stagedDiff)
		}
		start, count := atoi(t, m[1]), atoi(t, m[2])
		firstRange := HunkRange{StartLine: start, EndLine: start + count - 1}

		if err := f.UnstageHunks(path, []HunkRange{firstRange}); err != nil {
			t.Fatalf("UnstageHunks: %v", err)
		}

		// Staged diff should now only contain L20, not L1.
		stagedDiff = gitOut(t, dir, "diff", "--cached", "--", "multi.txt")
		if strings.Contains(stagedDiff, "+L1") {
			t.Errorf("staged diff should not contain +L1 after unstage\n%s", stagedDiff)
		}
		if !strings.Contains(stagedDiff, "+L20") {
			t.Errorf("staged diff should still contain +L20\n%s", stagedDiff)
		}
		// Unstaged diff should now contain L1.
		unstaged := gitOut(t, dir, "diff", "--", "multi.txt")
		if !strings.Contains(unstaged, "+L1") {
			t.Errorf("unstaged diff should contain +L1 after unstage\n%s", unstaged)
		}
	})
}

func TestUnstageHunks_EmitsStatusChanged(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted, _ = args[0].(string)
			}
		}
		path := filepath.Join(dir, "multi.txt")
		commitFile(t, dir, "multi.txt", "l1\nl2\nl3\n")
		if err := os.WriteFile(path, []byte("L1\nl2\nl3\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "multi.txt")

		stagedDiff := gitOut(t, dir, "diff", "--cached", "--", "multi.txt")
		m := regexp.MustCompile(`@@ -(\d+),(\d+) `).FindStringSubmatch(stagedDiff)
		if m == nil {
			t.Fatalf("no hunk header in staged diff:\n%s", stagedDiff)
		}
		start, count := atoi(t, m[1]), atoi(t, m[2])
		r := HunkRange{StartLine: start, EndLine: start + count - 1}

		if err := f.UnstageHunks(path, []HunkRange{r}); err != nil {
			t.Fatalf("UnstageHunks: %v", err)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

// ---------------------------------------------------------------------------
// DiscardHunks — integration
// ---------------------------------------------------------------------------

func TestDiscardHunks_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.DiscardHunks("/some/file.txt", []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Fatal("DiscardHunks: expected error when no active project")
	}
}

func TestDiscardHunks_EmptyHunksIsNoop(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		if err := f.DiscardHunks(path, nil); err != nil {
			t.Fatalf("DiscardHunks (empty): %v", err)
		}
	})
}

func TestDiscardHunks_NoUnstagedChanges(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "committed.txt")
		if err := f.DiscardHunks(path, []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
			t.Fatal("DiscardHunks: expected error when there are no unstaged changes")
		}
	})
}

func TestDiscardHunks_DiscardsSingleHunk(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "multi.txt")
		var orig strings.Builder
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(&orig, "l%d\n", i)
		}
		if err := os.WriteFile(path, []byte(orig.String()), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runGit(t, dir, "add", "multi.txt")
		runGit(t, dir, "commit", "-m", "add multi")

		var modified strings.Builder
		for i := 1; i <= 20; i++ {
			switch i {
			case 1:
				modified.WriteString("L1\n")
			case 20:
				modified.WriteString("L20\n")
			default:
				fmt.Fprintf(&modified, "l%d\n", i)
			}
		}
		if err := os.WriteFile(path, []byte(modified.String()), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		// Discard the first hunk only.
		first := firstHunkRange(t, dir, "multi.txt")
		if err := f.DiscardHunks(path, []HunkRange{first}); err != nil {
			t.Fatalf("DiscardHunks: %v", err)
		}

		// File should now have l1 restored but L20 still changed.
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		content := string(got)
		if !strings.Contains(content, "l1\n") {
			t.Errorf("after discard: file should contain l1 (restored)\n%s", content)
		}
		if !strings.Contains(content, "L20") {
			t.Errorf("after discard: file should still contain L20 (not discarded)\n%s", content)
		}
		// Unstaged diff should only contain L20, not L1.
		unstaged := gitOut(t, dir, "diff", "--", "multi.txt")
		if strings.Contains(unstaged, "+L1") {
			t.Errorf("unstaged diff should not contain +L1 after discard\n%s", unstaged)
		}
		if !strings.Contains(unstaged, "+L20") {
			t.Errorf("unstaged diff should still contain +L20\n%s", unstaged)
		}
	})
}

func TestDiscardHunks_EmitsStatusChanged(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				emitted, _ = args[0].(string)
			}
		}
		path := filepath.Join(dir, "multi.txt")
		commitFile(t, dir, "multi.txt", "l1\nl2\nl3\n")
		if err := os.WriteFile(path, []byte("L1\nl2\nl3\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		r := firstHunkRange(t, dir, "multi.txt")
		if err := f.DiscardHunks(path, []HunkRange{r}); err != nil {
			t.Fatalf("DiscardHunks: %v", err)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}
