package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/workspace"
)

// ---------------------------------------------------------------------------
// Phase 6 tests: discard, gitignore, merge/rebase, commit graph, per-hunk diff info
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
	if _, err := f.GetFileDiffHunks(filepath.Join(f.activeProjectPath, "f.txt")); err == nil {
		t.Error("GetFileDiffHunks: expected error")
	}
}

// ---------------------------------------------------------------------------
// parseHunkInfos — deterministic, no git
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
