package backend

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Tag & reset RPC tests (Phase 4)
// ---------------------------------------------------------------------------
//
// These tests exercise CreateTag, DeleteTag, PushTag, DeleteRemoteTag and
// ResetToCommit. They reuse the shared git test helpers (withGitRepo,
// gitOut, commitFile, runGit, gitDefaultBranch, gitInit) defined in
// frontend_api_git_test.go and frontend_api_workspace_test.go.
//
// For PushTag/DeleteRemoteTag there is no real remote available, so the
// tests focus on the validation path (empty name) and the default-remote
// logic (asserting the command is formed against "origin" by inspecting
// the error/combined output).
//
// disableGpgSign disables tag.gpgsign (and commit.gpgsign) locally in the
// test repo. The development machine sets tag.gpgsign=true globally, which
// forces `git tag <name> <sha>` into annotated+signed mode and demands an
// editor. Production now neutralizes this inline with -c tag.gpgsign=false
// (see CreateTag), so CreateTag tests no longer strictly require this
// helper — but it remains harmless and is still used by other tag/commit
// tests for hermeticity. See TestCreateTag_WithGpgSignEnabled for the
// regression test that exercises the production override directly.
func disableGpgSign(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "tag.gpgsign", "false")
	runGit(t, dir, "config", "commit.gpgsign", "false")
}

// --- CreateTag tests ---

func TestCreateTag_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.CreateTag("v1.0", "HEAD"); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestCreateTag_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		sha := gitOut(t, dir, "rev-parse", "HEAD")
		if err := f.CreateTag("   ", sha); err == nil {
			t.Fatal("expected error for empty tag name")
		}
	})
}

func TestCreateTag_EmptySha(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.CreateTag("v1.0", "   "); err == nil {
			t.Fatal("expected error for empty sha")
		}
	})
}

func TestCreateTag_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		disableGpgSign(t, dir)
		sha := gitOut(t, dir, "rev-parse", "HEAD")

		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				if p, ok := args[0].(string); ok {
					emitted = p
				}
			}
		}

		if err := f.CreateTag("v1.0", sha); err != nil {
			t.Fatalf("CreateTag: %v", err)
		}

		// Verify the tag exists and points at the expected commit.
		tags := gitOut(t, dir, "tag", "--list")
		if !strings.Contains(tags, "v1.0") {
			t.Errorf("expected v1.0 in tag list, got: %q", tags)
		}
		tagSha := gitOut(t, dir, "rev-parse", "refs/tags/v1.0")
		if tagSha != sha {
			t.Errorf("tag points at %q, want %q", tagSha, sha)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

// TestCreateTag_WithGpgSignEnabled is the regression test for the bug where
// creating a tag failed on machines that set tag.gpgsign=true globally. With
// tag.gpgsign=true, a plain `git tag <name> <sha>` is forced into
// annotated+signed mode and demands an interactive editor for the tag
// message — which is unavailable in the GUI app, causing CreateTag to fail.
// Production neutralizes this with an inline -c tag.gpgsign=false override,
// so CreateTag must succeed even when gpgsign is enabled, and the resulting
// tag must be a lightweight tag (no annotated tag object).
func TestCreateTag_WithGpgSignEnabled(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Simulate the global tag.gpgsign=true found on this dev machine.
		// Do NOT call disableGpgSign — the point is to exercise the override.
		runGit(t, dir, "config", "tag.gpgsign", "true")
		sha := gitOut(t, dir, "rev-parse", "HEAD")

		if err := f.CreateTag("v0.4.0", sha); err != nil {
			t.Fatalf("CreateTag with tag.gpgsign=true: %v", err)
		}

		// The tag must exist and point at the requested commit.
		tagSha := strings.TrimSpace(gitOut(t, dir, "rev-parse", "refs/tags/v0.4.0"))
		if tagSha != strings.TrimSpace(sha) {
			t.Errorf("tag points at %q, want %q", tagSha, sha)
		}
		// It must be a lightweight tag (points at the commit object directly),
		// not an annotated tag object — the inline override guarantees this.
		objType := strings.TrimSpace(gitOut(t, dir, "cat-file", "-t", "v0.4.0"))
		if objType != "commit" {
			t.Errorf("expected lightweight tag (commit), got %q", objType)
		}
	})
}

func TestCreateTag_AlreadyExists(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		disableGpgSign(t, dir)
		sha := gitOut(t, dir, "rev-parse", "HEAD")
		// Create the tag once directly.
		gitOut(t, dir, "tag", "v1.0", sha)

		err := f.CreateTag("v1.0", sha)
		if err == nil {
			t.Fatal("expected error when tag already exists")
		}
	})
}

func TestCreateTag_NonexistentSha(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		disableGpgSign(t, dir)
		err := f.CreateTag("v1.0", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
		if err == nil {
			t.Fatal("expected error for nonexistent sha")
		}
	})
}

// --- DeleteTag tests ---

func TestDeleteTag_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.DeleteTag("   "); err == nil {
			t.Fatal("expected error for empty tag name")
		}
	})
}

func TestDeleteTag_Success(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		disableGpgSign(t, dir)
		// Create a tag directly to delete.
		sha := gitOut(t, dir, "rev-parse", "HEAD")
		gitOut(t, dir, "tag", "v2.0", sha)

		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				if p, ok := args[0].(string); ok {
					emitted = p
				}
			}
		}

		if err := f.DeleteTag("v2.0"); err != nil {
			t.Fatalf("DeleteTag: %v", err)
		}

		tags := gitOut(t, dir, "tag", "--list")
		if strings.Contains(tags, "v2.0") {
			t.Errorf("expected v2.0 to be deleted, tag list still: %q", tags)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

func TestDeleteTag_Nonexistent(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		err := f.DeleteTag("does-not-exist")
		if err == nil {
			t.Fatal("expected error for nonexistent tag")
		}
	})
}

// --- PushTag tests ---

func TestPushTag_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		_, err := f.PushTag("   ", "")
		if err == nil {
			t.Fatal("expected error for empty tag name")
		}
	})
}

// TestPushTag_DefaultRemoteOrigin verifies that an empty remote defaults
// to "origin". With no origin configured, the push fails — runGitCmdCombined
// returns git's stderr (referencing "origin") in the combined output, so we
// assert it appears there.
func TestPushTag_DefaultRemoteOrigin(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		disableGpgSign(t, dir)
		sha := gitOut(t, dir, "rev-parse", "HEAD")
		gitOut(t, dir, "tag", "v3.0", sha)

		out, err := f.PushTag("v3.0", "")
		if err == nil {
			t.Skipf("push succeeded (a remote named origin exists); cannot assert default-remote error path. output: %q", out)
		}
		// git's stderr is returned in the combined output, not in the
		// wrapped error string; check both.
		combined := err.Error() + " " + out
		if !strings.Contains(combined, "origin") {
			t.Errorf("expected default remote 'origin' in output, got err=%v out=%q", err, out)
		}
	})
}

func TestPushTag_NonexistentRemote(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		disableGpgSign(t, dir)
		sha := gitOut(t, dir, "rev-parse", "HEAD")
		gitOut(t, dir, "tag", "v3.0", sha)

		out, err := f.PushTag("v3.0", "no-such-remote")
		if err == nil {
			t.Fatal("expected error when pushing to a nonexistent remote")
		}
		// git's "does not appear to be a git repository" message carries the
		// remote name in the combined output.
		combined := err.Error() + " " + out
		if !strings.Contains(combined, "no-such-remote") {
			t.Errorf("expected output to reference the named remote, got err=%v out=%q", err, out)
		}
	})
}

// --- DeleteRemoteTag tests ---

func TestDeleteRemoteTag_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		_, err := f.DeleteRemoteTag("   ", "")
		if err == nil {
			t.Fatal("expected error for empty tag name")
		}
	})
}

// TestDeleteRemoteTag_DefaultRemoteOrigin verifies that an empty remote
// defaults to "origin". With no origin configured, the push fails — git's
// stderr (referencing "origin") is in the combined output.
func TestDeleteRemoteTag_DefaultRemoteOrigin(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		disableGpgSign(t, dir)
		sha := gitOut(t, dir, "rev-parse", "HEAD")
		gitOut(t, dir, "tag", "v4.0", sha)

		out, err := f.DeleteRemoteTag("v4.0", "")
		if err == nil {
			t.Skipf("push succeeded (a remote named origin exists); cannot assert default-remote error path. output: %q", out)
		}
		combined := err.Error() + " " + out
		if !strings.Contains(combined, "origin") {
			t.Errorf("expected default remote 'origin' in output, got err=%v out=%q", err, out)
		}
	})
}

func TestDeleteRemoteTag_NonexistentRemote(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		out, err := f.DeleteRemoteTag("v4.0", "no-such-remote")
		if err == nil {
			t.Fatal("expected error when deleting remote tag on a nonexistent remote")
		}
		combined := err.Error() + " " + out
		if !strings.Contains(combined, "no-such-remote") {
			t.Errorf("expected output to reference the named remote, got err=%v out=%q", err, out)
		}
	})
}

// --- ResetToCommit tests ---

func TestResetToCommit_EmptySha(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.ResetToCommit("   ", "soft"); err == nil {
			t.Fatal("expected error for empty sha")
		}
	})
}

func TestResetToCommit_InvalidMode(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		sha := gitOut(t, dir, "rev-parse", "HEAD")
		err := f.ResetToCommit(sha, "bogus")
		if err == nil {
			t.Fatal("expected error for invalid reset mode")
		}
		if !strings.Contains(err.Error(), "invalid reset mode") {
			t.Errorf("expected 'invalid reset mode' message, got: %v", err)
		}
	})
}

func TestResetToCommit_Soft(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		// Make a second commit so HEAD can be moved back to the first.
		commitFile(t, dir, "second.txt", "second\n")
		// HEAD~1 is the first commit (committed.txt).
		firstSha := gitOut(t, dir, "rev-parse", "HEAD~1")

		var emitted string
		f.emitEvent = func(name string, args ...any) {
			if name == EventGitStatusChanged && len(args) >= 1 {
				if p, ok := args[0].(string); ok {
					emitted = p
				}
			}
		}

		if err := f.ResetToCommit(firstSha, "soft"); err != nil {
			t.Fatalf("ResetToCommit soft: %v", err)
		}

		// HEAD should now point at the first commit; the soft reset keeps
		// the second commit's changes staged.
		headSha := gitOut(t, dir, "rev-parse", "HEAD")
		if headSha != firstSha {
			t.Errorf("HEAD after reset: got %q, want %q", headSha, firstSha)
		}
		if emitted != dir {
			t.Errorf("event payload: got %q, want %q", emitted, dir)
		}
	})
}

func TestResetToCommit_NonexistentSha(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		err := f.ResetToCommit("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "hard")
		if err == nil {
			t.Fatal("expected error for nonexistent sha")
		}
	})
}
