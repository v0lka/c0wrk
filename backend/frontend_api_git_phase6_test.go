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
// parseGitGraph — deterministic, no git
// ---------------------------------------------------------------------------

func TestParseGitGraph(t *testing.T) {
	// %H%x1f%P%x1f%s%x1f%d%x1e  (record sep = \x1e, field sep = \x1f)
	tests := []struct {
		name  string
		input string
		want  []GraphCommit
	}{
		{
			name:  "empty",
			input: "",
			want:  []GraphCommit{},
		},
		{
			name:  "whitespace only",
			input: "  \n ",
			want:  []GraphCommit{},
		},
		{
			name:  "single root commit no refs",
			input: "abc123\x1f\x1fadd file\x1f",
			want: []GraphCommit{
				{SHA: "abc123", Parents: nil, Message: "add file", Refs: []string{}},
			},
		},
		{
			name:  "single commit with parents and refs",
			input: "def456\x1faaa bbb\x1ffeat: x\x1f (HEAD -> main, tag: v1.0)",
			want: []GraphCommit{
				{SHA: "def456", Parents: []string{"aaa", "bbb"}, Message: "feat: x", Refs: []string{"HEAD -> main", "tag: v1.0"}},
			},
		},
		{
			name:  "multiple commits",
			input: "s1\x1fp1\x1fm1\x1f\x1es2\x1f\x1fm2\x1f (HEAD -> main)",
			want: []GraphCommit{
				{SHA: "s1", Parents: []string{"p1"}, Message: "m1", Refs: []string{}},
				{SHA: "s2", Parents: nil, Message: "m2", Refs: []string{"HEAD -> main"}},
			},
		},
		{
			name:  "malformed record with too few fields is skipped",
			input: "s1\x1fp1\x1fm1\x1f\x1eshort",
			want: []GraphCommit{
				{SHA: "s1", Parents: []string{"p1"}, Message: "m1", Refs: []string{}},
			},
		},
		{
			name:  "trailing record separator ignored",
			input: "s1\x1f\x1fm1\x1f\x1e",
			want: []GraphCommit{
				{SHA: "s1", Parents: nil, Message: "m1", Refs: []string{}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitGraph(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseGitGraph(%q) mismatch (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}

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
// GetGitGraph — integration
// ---------------------------------------------------------------------------

func TestGetGitGraph_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.GetGitGraph(0, 0); err == nil {
		t.Fatal("GetGitGraph: expected error when no active project")
	}
}

func TestGetGitGraph_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// withGitRepo commits committed.txt (1 commit). Add a second.
		commitFile(t, dir, "b.txt", "b\n")

		graph, err := f.GetGitGraph(0, 0)
		if err != nil {
			t.Fatalf("GetGitGraph: %v", err)
		}
		if len(graph) != 2 {
			t.Fatalf("len: got %d, want 2", len(graph))
		}
		// Newest first.
		if graph[0].Message != "add b.txt" {
			t.Errorf("graph[0].Message: got %q, want %q", graph[0].Message, "add b.txt")
		}
		if graph[0].SHA == "" {
			t.Error("graph[0].SHA is empty")
		}
		// Second commit is the root: it has the first commit as parent, and
		// the root commit has no parents.
		if len(graph[0].Parents) != 1 {
			t.Errorf("graph[0].Parents: got %d, want 1", len(graph[0].Parents))
		} else if graph[0].Parents[0] != graph[1].SHA {
			t.Errorf("graph[0].Parents[0]: got %q, want %q", graph[0].Parents[0], graph[1].SHA)
		}
		if len(graph[1].Parents) != 0 {
			t.Errorf("graph[1].Parents: got %v, want empty (root)", graph[1].Parents)
		}
		// HEAD -> branch decoration sits on the newest commit (graph[0]);
		// the root commit (graph[1]) carries no decoration.
		if len(graph[0].Refs) == 0 {
			t.Errorf("graph[0].Refs: got %v, want at least one (HEAD -> branch)", graph[0].Refs)
		}
		if len(graph[1].Refs) != 0 {
			t.Errorf("graph[1].Refs: got %v, want empty (root has no decoration)", graph[1].Refs)
		}
	})
}

func TestGetGitGraph_Pagination(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// withGitRepo creates 1 commit (committed.txt). Add two more so the
		// repo has 3 commits: newest=oldest order is b2, b1, committed.
		commitFile(t, dir, "b1.txt", "b1\n")
		commitFile(t, dir, "b2.txt", "b2\n")

		// limit caps the page size.
		page, err := f.GetGitGraph(2, 0)
		if err != nil {
			t.Fatalf("GetGitGraph(2,0): %v", err)
		}
		if len(page) != 2 {
			t.Fatalf("GetGitGraph(2,0) len: got %d, want 2", len(page))
		}
		// Newest first: the first page starts at the most recent commit.
		if page[0].Message != "add b2.txt" {
			t.Errorf("GetGitGraph(2,0)[0].Message: got %q, want %q", page[0].Message, "add b2.txt")
		}

		// skip offsets into older history: skipping 2 leaves the root commit.
		older, err := f.GetGitGraph(2, 2)
		if err != nil {
			t.Fatalf("GetGitGraph(2,2): %v", err)
		}
		if len(older) != 1 {
			t.Fatalf("GetGitGraph(2,2) len: got %d, want 1 (only root remains)", len(older))
		}

		// Non-positive limit defaults to defaultGitGraphLimit (100): with
		// skip=1 the newest commit is skipped, returning the 2 older ones.
		rest, err := f.GetGitGraph(0, 1)
		if err != nil {
			t.Fatalf("GetGitGraph(0,1): %v", err)
		}
		if len(rest) != 2 {
			t.Fatalf("GetGitGraph(0,1) len: got %d, want 2", len(rest))
		}
		if rest[0].Message != "add b1.txt" {
			t.Errorf("GetGitGraph(0,1)[0].Message: got %q, want %q", rest[0].Message, "add b1.txt")
		}
	})
}

func TestGetGitGraph_DoesNotEmit(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, _ string) {
		emitted := false
		f.emitEvent = func(name string, _ ...any) {
			if name == EventGitStatusChanged {
				emitted = true
			}
		}
		if _, err := f.GetGitGraph(0, 0); err != nil {
			t.Fatalf("GetGitGraph: %v", err)
		}
		if emitted {
			t.Error("GetGitGraph emitted git:status_changed, want no emit")
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
	if _, err := f.GetGitGraph(0, 0); err == nil {
		t.Error("GetGitGraph: expected error")
	}
	if err := f.StageHunks("/f.txt", []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Error("StageHunks: expected error")
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
	if _, err := f.GetGitGraph(0, 0); err == nil {
		t.Error("GetGitGraph: expected error")
	}
	if err := f.StageHunks(filepath.Join(f.activeProjectPath, "f.txt"), []HunkRange{{StartLine: 1, EndLine: 1}}); err == nil {
		t.Error("StageHunks: expected error")
	}
}
