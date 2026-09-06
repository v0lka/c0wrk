package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// Transport-vector canary integration tests (post-v0.7.3 review findings
// [40]/[55]) ──────────────────────────────────────────────────────────────
//
// The Git panel ships remote operations (Pull/Push/Fetch RPCs) and the diff
// porcelain, so the keys the old scanner texts declared unreachable are in
// fact executed by plain git: core.sshCommand wholly replaces the ssh binary
// on fetch/push against ssh:// remotes (before any network I/O), credential
// helpers and core.askPass run on credential challenges (git credential fill
// is the network-free stand-in for an authenticated fetch), and
// diff.external / diff.<n>.command run on plain `git diff` with no
// --ext-diff flag. Each test arms the key with a canary, runs c0wrk's
// repo-scoped git paths, and asserts the canary never executed.
//
// Non-vacuity controls run the same armed repository through RAW git with
// the machine's global config neutralized (GIT_CONFIG_GLOBAL/SYSTEM on
// /dev/null — the gittest "intentionally exercised" exemption): the canary
// fires there, proving the fixture is armed and only the neutralization
// stands between the key and execution.

// runRawGitControl runs git in the armed repository without any c0wrk
// hardening, with the machine's global/system config disabled so the probe
// is hermetic (no user keychain helpers or askpass programs run). Used only
// for non-vacuity controls that intentionally fire a dedicated canary.
func runRawGitControl(t *testing.T, dir, stdin string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run() // the control's exit status is irrelevant; only firing is
	t.Logf("raw git control %v: %s", args, out.String())
}

// TestCanarySSHCommandNeutered arms core.sshCommand with a canary script,
// points origin at an unreachable ssh:// remote, and fetches through
// GitCmdInRepo. The core.sshCommand=ssh override must restore the default
// ssh binary (verified on git 2.50.1): the fetch fails on the connection —
// not by executing the repository's command — and remote operations keep
// their fail-closed behavior.
func TestCanarySSHCommandNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	f.repo.Git(t, "remote", "add", "origin", "ssh://git@127.0.0.1:1/repo.git")
	script := f.canary.Plant(t, "sshcommand", gittest.HookBody)
	f.repo.AppendConfig(t, "[core]\n\tsshCommand = "+script)
	f.requireFinding(GitConfigFindingSSHCommand)

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "fetch", "origin")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err == nil {
		t.Fatalf("fetch from an unreachable ssh remote unexpectedly succeeded: %s", out.String())
	}
	t.Logf("neutralized fetch output: %s", out.String())
	f.canary.RequireNotFired(t)

	// Non-vacuity control: raw git against the same armed repository fires
	// the canary before any network I/O happens.
	control := gittest.NewCanary(t)
	controlScript := control.Plant(t, "sshcommand", gittest.HookBody)
	f.repo.AppendConfig(t, "[core]\n\tsshCommand = "+controlScript)
	runRawGitControl(t, f.repo.Root, "", "fetch", "origin")
	if !control.Fired(t) {
		t.Fatalf("the control canary must fire under raw git; fixture was not armed")
	}
}

// TestCanaryDiffExternalNeutered arms diff.external — which plain git diff
// executes BY DEFAULT (verified on git 2.50.1; --no-ext-diff is what
// disables it) — and exercises both c0wrk patch surfaces plus a raw
// GitCmdInRepo diff without the flag: the canary must never run, the diff
// output must stay usable through the --no-ext-diff call sites, and the
// unflagged invocation must fail closed rather than execute the armed
// value.
func TestCanaryDiffExternalNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	f.repo.Write(t, "file.txt", "hello\nchanged\n")
	script := f.canary.Plant(t, "diffexternal", gittest.HookBody)
	f.repo.AppendConfig(t, "[diff]\n\texternal = "+script)
	f.requireFinding(GitConfigFindingDiffCommand)

	// Patch surface 1 (runGitDiff + --no-ext-diff): usable output.
	diff, err := GetFileDiffInRepo(f.ctx, f.repo.Root, "file.txt")
	if err != nil {
		t.Fatalf("GetFileDiffInRepo: %v", err)
	}
	if !contains(diff, "+changed") {
		t.Fatalf("diff missing expected change:\n%s", diff)
	}
	f.canary.RequireNotFired(t)

	// Patch surface 2 (runGitDiffHead and the --no-index untracked-file leg
	// of BuildReviewDiff, both with --no-ext-diff): usable output. The
	// untracked leg matters because --no-index run inside a repository
	// still honors repo config (verified: an armed diff.external fires
	// there without the flag).
	f.repo.Write(t, "untracked.txt", "new file\n")
	review, err := BuildReviewDiff(f.ctx, f.repo.Root, 3)
	if err != nil {
		t.Fatalf("BuildReviewDiff: %v", err)
	}
	if !contains(review, "+changed") || !contains(review, "untracked.txt") {
		t.Fatalf("review diff missing expected content:\n%s", review)
	}
	f.canary.RequireNotFired(t)

	// Override-only path: a bare diff through GitCmdInRepo (no flag) must
	// fail closed instead of executing the armed external command.
	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "diff")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err == nil {
		t.Log("bare diff succeeded; the override still forbids execution either way")
	}
	f.canary.RequireNotFired(t)

	// Non-vacuity control: plain raw git diff executes the armed command.
	control := gittest.NewCanary(t)
	controlScript := control.Plant(t, "diffexternal", gittest.HookBody)
	f.repo.AppendConfig(t, "[diff]\n\texternal = "+controlScript)
	runRawGitControl(t, f.repo.Root, "", "diff")
	if !control.Fired(t) {
		t.Fatalf("the control canary must fire under raw git diff; fixture was not armed")
	}
}

// TestCanaryAskPassNeutered arms core.askPass and drives the credential
// machinery with `git credential fill` (the network-free equivalent of an
// authenticated fetch's credential challenge). The core.askPass= (empty)
// override must disable the helper: the challenge fails closed instead of
// prompting through repository-defined code.
func TestCanaryAskPassNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "askpass", gittest.HookBody)
	f.repo.AppendConfig(t, "[core]\n\taskPass = "+script)
	f.requireFinding(GitConfigFindingAskPass)

	if err := credentialFillThroughGitCmdInRepo(f.ctx, t, f.repo.Root); err == nil {
		t.Log("credential fill unexpectedly completed without prompting")
	}
	f.canary.RequireNotFired(t)

	control := gittest.NewCanary(t)
	controlScript := control.Plant(t, "askpass", gittest.HookBody)
	f.repo.AppendConfig(t, "[core]\n\taskPass = "+controlScript)
	runRawGitControl(t, f.repo.Root, credentialFillQuery, "credential", "fill")
	if !control.Fired(t) {
		t.Fatalf("the control canary must fire under raw git; fixture was not armed")
	}
}

// TestCanaryCredentialHelperNeutered arms both credential.helper and a
// URL-matched credential.<url>.helper with canaries and drives `git
// credential fill` through GitCmdInRepo. The credential.helper= (empty)
// reset must cover both forms (verified on git 2.50.1: an empty value in
// the -c layer — read after every file — resets the accumulated list, and
// the per-URL empty pin re-asserts it for the matching URL).
func TestCanaryCredentialHelperNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	generic := f.canary.Plant(t, "credgeneric", gittest.HookBody)
	perURL := f.canary.Plant(t, "credurl", gittest.HookBody)
	f.repo.AppendConfig(t, "[credential]\n\thelper = "+generic+"\n"+
		"[credential \"http://127.0.0.1\"]\n\thelper = "+perURL)
	f.requireFinding(GitConfigFindingCredential)

	if err := credentialFillThroughGitCmdInRepo(f.ctx, t, f.repo.Root); err == nil {
		t.Log("credential fill unexpectedly completed without prompting")
	}
	f.canary.RequireNotFired(t)

	control := gittest.NewCanary(t)
	controlScript := control.Plant(t, "credgeneric", gittest.HookBody)
	f.repo.AppendConfig(t, "[credential \"http://127.0.0.1\"]\n\thelper = "+controlScript)
	runRawGitControl(t, f.repo.Root, credentialFillQuery, "credential", "fill")
	if !control.Fired(t) {
		t.Fatalf("the control canary must fire under raw git; fixture was not armed")
	}
}

// credentialFillQuery is the stdin git credential fill expects: a challenge
// for an HTTP host, terminated by a blank line.
const credentialFillQuery = "protocol=http\nhost=127.0.0.1\n\n"

// credentialFillThroughGitCmdInRepo runs `git credential fill` through the
// hardened repo-scoped spawn path. It returns the command's error — with
// every helper reset and no terminal, git refuses the challenge, which is
// the expected fail-closed outcome.
func credentialFillThroughGitCmdInRepo(ctx context.Context, t *testing.T, repoRoot string) error {
	t.Helper()
	cmd, err := GitCmdInRepo(ctx, repoRoot, "credential", "fill")
	if err != nil {
		return err
	}
	cmd.Dir = repoRoot
	cmd.Stdin = strings.NewReader(credentialFillQuery)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	if runErr != nil {
		t.Logf("credential fill (fail-closed expected): %s", out.String())
	}
	return runErr
}
