package backend

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Branch remote operations RPCs (Phase 5)
// ---------------------------------------------------------------------------
//
// These tests exercise PushBranch, CheckoutRemoteBranch and
// DeleteRemoteBranch against a local bare remote. They reuse the shared git
// test helpers (withGitRepo, gitOut, commitFile, runGit, gitDefaultBranch,
// gitInit) defined in frontend_api_git_test.go and
// frontend_api_workspace_test.go.

// --- PushBranch ---

func TestPushBranch_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.PushBranch("main"); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestPushBranch_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if _, err := f.PushBranch("   "); err == nil {
			t.Fatal("expected error for empty branch name")
		}
	})
}

func TestPushBranch_UntrackedBranch_PublishesAndSetsUpstream(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	mainBranch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", mainBranch)

	// Create an unpublished feature branch with its own commit.
	gitOut(t, localDir, "checkout", "-b", "feature/x")
	commitFile(t, localDir, "b.txt", "b\n")

	f := &FrontendAPI{activeProjectPath: localDir}
	var emitted string
	f.emitEvent = func(name string, args ...any) {
		if name == EventGitStatusChanged && len(args) >= 1 {
			emitted, _ = args[0].(string)
		}
	}

	if _, err := f.PushBranch("feature/x"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if emitted != localDir {
		t.Errorf("event payload: got %q, want %q", emitted, localDir)
	}

	// The remote now has the feature branch pointing at the local HEAD.
	remoteHead := gitOut(t, remoteDir, "rev-parse", "refs/heads/feature/x")
	localHead := gitOut(t, localDir, "rev-parse", "HEAD")
	if remoteHead != localHead {
		t.Errorf("after PushBranch: remote head %q != local head %q", remoteHead, localHead)
	}

	// The -u publish must have set the upstream.
	if got := gitOut(t, localDir, "config", "--get", "branch.feature/x.remote"); got != "origin" {
		t.Errorf("branch.feature/x.remote = %q, want origin", got)
	}
	if got := gitOut(t, localDir, "rev-parse", "--abbrev-ref", "feature/x@{upstream}"); got != "origin/feature/x" {
		t.Errorf("upstream = %q, want origin/feature/x", got)
	}
}

func TestPushBranch_TrackedBranch_PushesToConfiguredRemote(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	// A remote that is NOT named "origin" forces PushBranch to honour
	// branch.<name>.remote instead of hardcoding "origin".
	gitOut(t, localDir, "remote", "add", "upstream", remoteDir)
	mainBranch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "upstream", mainBranch)

	f := &FrontendAPI{activeProjectPath: localDir}

	// New local commit to push via the RPC.
	commitFile(t, localDir, "b.txt", "b\n")
	if _, err := f.PushBranch(mainBranch); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}

	remoteHead := gitOut(t, remoteDir, "rev-parse", "refs/heads/"+mainBranch)
	localHead := gitOut(t, localDir, "rev-parse", "HEAD")
	if remoteHead != localHead {
		t.Errorf("after PushBranch: remote head %q != local head %q", remoteHead, localHead)
	}
}

// --- CheckoutRemoteBranch ---

func TestCheckoutRemoteBranch_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if err := f.CheckoutRemoteBranch("origin/main"); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestCheckoutRemoteBranch_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.CheckoutRemoteBranch("   "); err == nil {
			t.Fatal("expected error for empty remote branch")
		}
	})
}

func TestCheckoutRemoteBranch_NoSlash(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if err := f.CheckoutRemoteBranch("feature"); err == nil {
			t.Fatal("expected error for remote branch without a '/'")
		}
	})
}

func TestCheckoutRemoteBranch_CreatesTrackingBranchAndSwitches(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	mainBranch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", mainBranch)

	// Publish a remote branch from a separate clone.
	cloneParent := t.TempDir()
	cloneDir := filepath.Join(cloneParent, "clone")
	gitOut(t, cloneParent, "clone", remoteDir, cloneDir)
	runGit(t, cloneDir, "config", "user.email", "test@test.com")
	runGit(t, cloneDir, "config", "user.name", "Test")
	runGit(t, cloneDir, "checkout", "-b", "feature/x")
	commitFile(t, cloneDir, "b.txt", "b\n")
	runGit(t, cloneDir, "push", "-u", "origin", "feature/x")

	// Make the remote-tracking ref visible locally.
	gitOut(t, localDir, "fetch", "origin")

	f := &FrontendAPI{activeProjectPath: localDir}
	if err := f.CheckoutRemoteBranch("origin/feature/x"); err != nil {
		t.Fatalf("CheckoutRemoteBranch: %v", err)
	}

	current, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if current.Name != "feature/x" {
		t.Errorf("current branch: got %q, want %q", current.Name, "feature/x")
	}
	if current.Upstream != "origin/feature/x" {
		t.Errorf("upstream: got %q, want %q", current.Upstream, "origin/feature/x")
	}
}

func TestCheckoutRemoteBranch_NestedBranchName(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	mainBranch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", mainBranch)

	cloneParent := t.TempDir()
	cloneDir := filepath.Join(cloneParent, "clone")
	gitOut(t, cloneParent, "clone", remoteDir, cloneDir)
	runGit(t, cloneDir, "config", "user.email", "test@test.com")
	runGit(t, cloneDir, "config", "user.name", "Test")
	runGit(t, cloneDir, "checkout", "-b", "feature/deep/x")
	commitFile(t, cloneDir, "b.txt", "b\n")
	runGit(t, cloneDir, "push", "-u", "origin", "feature/deep/x")

	gitOut(t, localDir, "fetch", "origin")

	f := &FrontendAPI{activeProjectPath: localDir}
	// Local name is everything after the first '/'.
	if err := f.CheckoutRemoteBranch("origin/feature/deep/x"); err != nil {
		t.Fatalf("CheckoutRemoteBranch: %v", err)
	}

	current, err := f.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if current.Name != "feature/deep/x" {
		t.Errorf("current branch: got %q, want %q", current.Name, "feature/deep/x")
	}
}

// --- DeleteRemoteBranch ---

func TestDeleteRemoteBranch_NoProject(t *testing.T) {
	f := &FrontendAPI{}
	if _, err := f.DeleteRemoteBranch("main", "origin"); err == nil {
		t.Fatal("expected error when no active project")
	}
}

func TestDeleteRemoteBranch_EmptyName(t *testing.T) {
	withGitRepo(t, func(f *FrontendAPI, dir string) {
		if _, err := f.DeleteRemoteBranch("   ", "origin"); err == nil {
			t.Fatal("expected error for empty branch name")
		}
	})
}

func TestDeleteRemoteBranch_DeletesOnRemote(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	mainBranch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", mainBranch)

	// Publish a feature branch to delete.
	gitOut(t, localDir, "checkout", "-b", "feature/x")
	commitFile(t, localDir, "b.txt", "b\n")
	gitOut(t, localDir, "push", "-u", "origin", "feature/x")

	f := &FrontendAPI{activeProjectPath: localDir}
	var emitted string
	f.emitEvent = func(name string, args ...any) {
		if name == EventGitStatusChanged && len(args) >= 1 {
			emitted, _ = args[0].(string)
		}
	}

	if _, err := f.DeleteRemoteBranch("feature/x", "origin"); err != nil {
		t.Fatalf("DeleteRemoteBranch: %v", err)
	}
	if emitted != localDir {
		t.Errorf("event payload: got %q, want %q", emitted, localDir)
	}

	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", "--verify", "--quiet", "refs/heads/feature/x")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err == nil {
		t.Error("expected remote branch feature/x to be deleted, but ref still exists")
	}
}

func TestDeleteRemoteBranch_DefaultRemoteOrigin(t *testing.T) {
	remoteDir := t.TempDir()
	gitOut(t, remoteDir, "init", "--bare")

	localDir := t.TempDir()
	gitInit(t, localDir)
	commitFile(t, localDir, "a.txt", "a\n")
	gitOut(t, localDir, "remote", "add", "origin", remoteDir)
	mainBranch := gitDefaultBranch(t, localDir)
	gitOut(t, localDir, "push", "-u", "origin", mainBranch)

	gitOut(t, localDir, "checkout", "-b", "feature/x")
	commitFile(t, localDir, "b.txt", "b\n")
	gitOut(t, localDir, "push", "-u", "origin", "feature/x")

	f := &FrontendAPI{activeProjectPath: localDir}
	// Empty remote falls back to "origin".
	if _, err := f.DeleteRemoteBranch("feature/x", ""); err != nil {
		t.Fatalf("DeleteRemoteBranch (default remote): %v", err)
	}

	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", "--verify", "--quiet", "refs/heads/feature/x")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err == nil {
		t.Error("expected remote branch feature/x to be deleted, but ref still exists")
	}
}
