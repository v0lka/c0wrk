package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/gittrust"
	"github.com/v0lka/c0wrk/internal/gittest"
)

// Trusted-repository spawn carve-out tests ─────────────────────────────────
//
// security.trusted_git_repos now opts a repository back into its own git
// configuration: core/workspace consults the process-wide core/gittrust
// registry (written by backend) and, for a trusted work-tree root, spawns
// raw git via sysproc.GitCmdRaw — no baseline overrides, no per-repo
// NeutralizingArgv, no GIT_EDITOR pin. Untrusted / hardened repositories keep
// the full neutralization (the existing canary suite). The registry is
// process-wide, so every test here clears it up front and on teardown.

// TestGitCmdInRepoTrustedRepoSpawnsRawGit plants a filter finding (which the
// hardened path would neutralize with -c filter.* overrides) and then trusts
// the repository: GitCmdInRepo must produce argv == [git, <args...>] with
// cmd.Env nil (inherit parent), proving no baseline, no NeutralizingArgv and
// no GIT_EDITOR pin reach a trusted spawn.
func TestGitCmdInRepoTrustedRepoSpawnsRawGit(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	f := newCanaryFixture(t, "hello\n")
	f.repo.AppendConfig(t, "[filter \"lfs\"]\n\tprocess = /tmp/evil-filter.sh\n\tclean = /tmp/evil-clean.sh\n\tsmudge = /tmp/evil-smudge.sh\n")
	f.requireFinding(GitConfigFindingFilter) // armed: hardened path would neutralize it

	root := f.repo.Root
	gittrust.Trust(root)

	cmd, err := GitCmdInRepo(f.ctx, root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted): %v", err)
	}

	want := []string{"git", "status", "--porcelain"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("trusted argv = %q, want raw %q (no -c overrides)", cmd.Args, want)
	}
	if cmd.Env != nil {
		t.Errorf("trusted cmd.Env must be nil (inherit parent, no GIT_EDITOR pin), got %d entries", len(cmd.Env))
	}
	if cmd.Dir != root {
		t.Errorf("trusted cmd.Dir = %q, want %q", cmd.Dir, root)
	}

	// The raw path must still be functional.
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("trusted raw git status: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("trusted raw git status unexpected output: %q", out.String())
	}
}

// TestGitCmdInRepoTrustedBySubdirectory verifies the trust decision keys on
// the work-tree root git discovers, not the exact path: a workspace opened at
// a subdirectory of a trusted repository still spawns raw git, matching the
// backend's "trust any path inside a repo trusts the whole repo" semantics.
func TestGitCmdInRepoTrustedBySubdirectory(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	f := newCanaryFixture(t, "hello\n")
	gittrust.Trust(f.repo.Root)

	sub := filepath.Join(f.repo.Root, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	cmd, err := GitCmdInRepo(f.ctx, sub, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted subdir): %v", err)
	}
	want := []string{"git", "rev-parse", "--is-inside-work-tree"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("trusted-subdir argv = %q, want raw %q", cmd.Args, want)
	}
	if cmd.Env != nil {
		t.Error("trusted-subdir cmd.Env must be nil")
	}
}

// TestGitCmdInRepoUntrustedRepoStaysHardened is the inverse pin: without a
// trust decision the same repository goes through the full neutralization —
// baseline -c overrides plus the NeutralizingArgv derived from a planted
// filter, with a hardened (GIT_EDITOR-pinned) environment.
func TestGitCmdInRepoUntrustedRepoStaysHardened(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	f := newCanaryFixture(t, "hello\n")
	f.repo.AppendConfig(t, "[filter \"lfs\"]\n\tprocess = /tmp/evil-filter.sh\n\tclean = /tmp/evil-clean.sh\n\tsmudge = /tmp/evil-smudge.sh\n")
	f.requireFinding(GitConfigFindingFilter)

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (untrusted): %v", err)
	}

	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"-c core.fsmonitor=false",
		"-c commit.gpgsign=false",
		"-c filter.lfs.process=",
		"-c filter.lfs.clean=cat",
		"-c filter.lfs.smudge=cat",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("untrusted argv missing %q: %q", want, cmd.Args)
		}
	}
	if cmd.Env == nil {
		t.Fatal("untrusted cmd.Env must be hardened (non-nil)")
	}
	if !slices.Contains(cmd.Env, "GIT_EDITOR=true") {
		t.Errorf("untrusted cmd.Env missing GIT_EDITOR pin: %q", cmd.Env)
	}
}
