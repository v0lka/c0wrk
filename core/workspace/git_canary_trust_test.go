package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/core/gittrust"
	"github.com/v0lka/c0wrk/internal/gittest"
)

// Trusted-repository canary tests ─────────────────────────────────────────
//
// The trust opt-out (security.trusted_git_repos → core/gittrust) flips the
// spawn-layer behavior for a repository: where the hardened path neutralizes
// every command-bearing git-config key (the existing canary suite proves a
// canary never fires), a trusted repository runs raw git and therefore
// EXECUTES its own hooks/filters/signing. The test here is the functional
// complement to TestGitCmdInRepoTrustedRepoSpawnsRawGit (which pins the raw
// argv structurally): it arms a real canary and asserts it FIRES through
// c0wrk's spawn path, proving the trust decision genuinely opts the repo
// back into its own git configuration.

// TestCanaryTrustedRepoSpawnsRawGitFiresCanary arms a pre-commit hook —
// the exact vector TestCanaryHooksPathNeutered proves the hardened path
// suppresses — and then trusts the repository. A commit through GitCmdInRepo
// must execute the hook (raw git), so the canary fires; the commit must also
// complete, proving raw git stays functional.
func TestCanaryTrustedRepoSpawnsRawGitFiresCanary(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "pre-commit", gittest.HookBody)
	hooksDir := filepath.Join(f.repo.GitDir(), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("creating hooks dir: %v", err)
	}
	installed, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("reading canary hook: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), installed, 0o755); err != nil {
		t.Fatalf("installing canary hook: %v", err)
	}
	f.repo.AppendConfig(t, "[core]\n\thooksPath = "+hooksDir)
	f.requireFinding(GitConfigFindingHooksPath) // armed: hardened path would neutralize it

	gittrust.Trust(f.repo.Root)

	f.repo.Write(t, "file.txt", "hello\nchanged\n")
	f.repo.Git(t, "add", ".")

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "commit", "-m", "trusted")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted): %v", err)
	}
	var commitOut bytes.Buffer
	cmd.Stdout = &commitOut
	cmd.Stderr = &commitOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit through trusted raw git failed: %v (output: %s)", err, commitOut.String())
	}
	if got := f.repo.Git(t, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("commit did not complete useably: rev-list count = %s", got)
	}
	if !f.canary.Fired(t) {
		t.Fatal("trusted repository must run raw git, so the armed pre-commit hook canary must fire")
	}
}
