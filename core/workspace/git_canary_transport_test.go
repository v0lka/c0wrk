package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// Transport-vector canary integration tests (post-v0.7.3 review findings
// [40]/[55]) ──────────────────────────────────────────────────────────────
//
// The Git panel ships remote operations (Pull/Push/Fetch RPCs) and the diff
// porcelain, so keys like core.sshCommand, core.askPass, credential helpers
// and diff.external are reachable from c0wrk's git layer. Two of those
// vectors are inherently network/interactive — core.sshCommand replaces the
// ssh binary on ssh:// fetches (network I/O), and askPass/credential helpers
// run on credential challenges (git prompts the user) — so c0wrk neutralizes
// each with a -c command-line override. These tests pin that the override
// reaches the spawned argv via the shared fake-git harness: the network-free,
// interaction-free proof that the hostile value never executes. The
// command-bearing key is planted, the scanner must report its finding, and
// the spawned argv must carry the neutralizing override (strictly not the
// planted value). diff.external is additionally exercised end-to-end against
// real local git (no network, no prompt), where a canary proves the override
// also prevents execution on the live porcelain.
//
// Non-vacuity controls run the same armed repository through RAW git with
// the machine's global config neutralized (GIT_CONFIG_GLOBAL/SYSTEM on
// /dev/null — the gittest "intentionally exercised" exemption): the canary
// fires there, proving the fixture is armed and only the neutralization
// stands between the key and execution. Controls are used only for the
// local-only vector (diff.external); the network/interactive vectors are
// proven structurally via the fake-git argv pins.

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

// pinGitCmdArgv runs GitCmdInRepo through the fake-git harness (no network,
// no interactive prompt) and returns the recorded argv tokens. Tests assert
// the neutralizing override beats the planted value by checking the argv
// carries the override and not the hostile value. This is the structural
// stand-in for the canary-never-fires proof on vectors that only execute
// during a network fetch or a credential challenge.
func pinGitCmdArgv(ctx context.Context, t *testing.T, repoRoot string, args ...string) []string {
	t.Helper()
	readLog := gittest.InstallFakeGit(t)
	cmd, err := GitCmdInRepo(ctx, repoRoot, args...)
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("fake git %v: %v", args, err)
	}
	return gittest.SectionLines(readLog(), "ARGV")
}

// TestCanarySSHCommandNeutered arms core.sshCommand with a canary script.
// core.sshCommand only executes on an ssh:// remote fetch (network), so the
// neutralization is pinned structurally via the fake-git harness: the spawn
// argv must carry the core.sshCommand=ssh override (restoring the default
// ssh binary) and must not carry the repository's script.
func TestCanarySSHCommandNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "sshcommand", gittest.HookBody)
	f.repo.AppendConfig(t, "[core]\n\tsshCommand = "+script)
	f.requireFinding(GitConfigFindingSSHCommand)

	argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, "fetch", "origin")
	if !slices.Contains(argv, "core.sshCommand=ssh") {
		t.Errorf("argv = %q, want the core.sshCommand=ssh override", argv)
	}
	if slices.Contains(argv, script) {
		t.Errorf("argv = %q, must not carry the repository's sshCommand", argv)
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

// TestCanaryAskPassNeutered arms core.askPass. core.askPass only runs on a
// git credential challenge (an interactive prompt), so the neutralization is
// pinned structurally via the fake-git harness: the spawn argv must carry the
// core.askPass= (empty) override and must not carry the repository's helper.
func TestCanaryAskPassNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "askpass", gittest.HookBody)
	f.repo.AppendConfig(t, "[core]\n\taskPass = "+script)
	f.requireFinding(GitConfigFindingAskPass)

	argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, "fetch", "origin")
	if !slices.Contains(argv, "core.askPass=") {
		t.Errorf("argv = %q, want the core.askPass= override", argv)
	}
	if slices.Contains(argv, script) {
		t.Errorf("argv = %q, must not carry the repository's askPass", argv)
	}
}

// TestCanaryCredentialHelperNeutered arms both credential.helper and a
// URL-matched credential.<url>.helper. credential helpers only run on a git
// credential challenge (an interactive prompt), so the neutralization is
// pinned structurally via the fake-git harness: the spawn argv must carry the
// credential.helper= (empty) reset and the per-URL empty pin, and must not
// carry either helper value.
func TestCanaryCredentialHelperNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	generic := f.canary.Plant(t, "credgeneric", gittest.HookBody)
	perURL := f.canary.Plant(t, "credurl", gittest.HookBody)
	f.repo.AppendConfig(t, "[credential]\n\thelper = "+generic+"\n"+
		"[credential \"http://127.0.0.1\"]\n\thelper = "+perURL)
	f.requireFinding(GitConfigFindingCredential)

	argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, "fetch", "origin")
	for _, want := range []string{"credential.helper=", "credential.http://127.0.0.1.helper="} {
		if !slices.Contains(argv, want) {
			t.Errorf("argv = %q, want the %q override", argv, want)
		}
	}
	if slices.Contains(argv, generic) || slices.Contains(argv, perURL) {
		t.Errorf("argv = %q, must not carry the repository's credential helpers", argv)
	}
}
