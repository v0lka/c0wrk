package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// Post-v0.7.4 hardening canary tests: the four vectors the prior review
// proved live under the then-current override set — core.gitProxy (executed
// by git:// fetches, not neutralizable via -c), core.worktree (writes
// tracked files outside the workspace, not neutralizable via -c), and
// include-hidden core.sshCommand / diff.external (name-independent pins
// close them) — plus the attr.tree version gate that fails closed on git
// older than 2.45 for include-bearing configs.
//
// Every scenario follows the established two-property pattern: the canary
// (or the hostile write) never happens through c0wrk's spawn path, and a
// raw-git non-vacuity control proves the fixture is genuinely armed.

// resetGitVersionCache clears the process-wide git version resolution so an
// injected probe result takes effect (and so a restored probe re-resolves).
func resetGitVersionCache() {
	gitVersionOnce = sync.Once{}
	resolvedGitVersionErr = nil
}

// injectGitVersion pins gitVersionOutputFn's result for the duration of
// the test and resets the version cache around the swap.
func injectGitVersion(t *testing.T, out string, outErr error) {
	t.Helper()
	orig := gitVersionOutputFn
	gitVersionOutputFn = func(context.Context) (string, error) { return out, outErr }
	resetGitVersionCache()
	t.Cleanup(func() {
		gitVersionOutputFn = orig
		resetGitVersionCache()
	})
}

// skipUnlessAttrTreeCapableGit skips a test whose fixture leans on the
// attr.tree neutralization when the machine's real git predates attr.tree
// support (2.45) — on such git the chokepoint fails closed by design, so
// the canary scenarios cannot run there at all.
func skipUnlessAttrTreeCapableGit(t *testing.T) {
	t.Helper()
	gittest.RequireGit(t)
	if err := requireAttrTreeCapableGit(); err != nil {
		t.Skipf("git lacks attr.tree support; include-bearing neutralization fails closed here: %v", err)
	}
}

// TestGitCmdInRepo_AttrTreeVersionGateFailsClosed pins the git-version
// gate: an include-bearing config relies on the attr.tree kill for hidden
// driver names, and attr.tree exists only since git 2.45 — older (or
// unresolvable) versions must make the chokepoint REFUSE to construct the
// command with a clear error, while 2.45.0 passes and include-free configs
// are never gated at all.
func TestGitCmdInRepo_AttrTreeVersionGateFailsClosed(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	f.repo.AppendConfig(t, "[include]\n\tpath = /nonexistent-extra.conf\n")

	injectGitVersion(t, "git version 2.44.9\n", nil)
	_, err := GitCmdInRepo(f.ctx, f.repo.Root, "status")
	if err == nil {
		t.Fatal("expected fail-closed refusal for an include-bearing config on git 2.44")
	}
	if !strings.Contains(err.Error(), "fail closed") {
		t.Errorf("gate error must state the fail-closed semantics: %v", err)
	}

	injectGitVersion(t, "2.45.0\n", nil)
	if _, err := GitCmdInRepo(f.ctx, f.repo.Root, "status"); err != nil {
		t.Fatalf("git 2.45.0 is the attr.tree floor and must pass the gate: %v", err)
	}

	injectGitVersion(t, "", errors.New("git: not found"))
	if _, err := GitCmdInRepo(f.ctx, f.repo.Root, "status"); err == nil {
		t.Fatal("expected fail-closed refusal when the git version is unresolvable")
	}

	injectGitVersion(t, "garbage\n", nil)
	if _, err := GitCmdInRepo(f.ctx, f.repo.Root, "status"); err == nil {
		t.Fatal("expected fail-closed refusal for unparsable git --version output")
	}

	// The gate is include-scoped: with a fully visible config an old git
	// must not be refused (the attr.tree pin never derives there).
	clean := newCanaryFixture(t, "hello\n")
	injectGitVersion(t, "git version 2.34.1\n", nil)
	if _, err := GitCmdInRepo(clean.ctx, clean.repo.Root, "status"); err != nil {
		t.Fatalf("include-free config on old git must not be gated: %v", err)
	}
}

// TestCanaryGitProxyNeutered arms core.gitProxy. core.gitProxy only executes
// on a git:// transport fetch (network), and no core.gitProxy value
// (empty included) neutralizes it — the protocol.git.allow=never override is
// the kill. The neutralization is pinned structurally via the fake-git
// harness: the spawn argv must carry the protocol.git.allow=never override
// and must not carry the repository's proxy command.
func TestCanaryGitProxyNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "gitproxy", gittest.HookBody)
	f.repo.AppendConfig(t, "[core]\n\tgitProxy = "+script)
	f.requireFinding(GitConfigFindingGitProxy)

	argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, "fetch", "origin")
	if !slices.Contains(argv, "protocol.git.allow=never") {
		t.Errorf("argv = %q, want the protocol.git.allow=never override", argv)
	}
	if slices.Contains(argv, script) {
		t.Errorf("argv = %q, must not carry the repository's gitProxy", argv)
	}
}

// TestCanaryWorkTreeEnvPinKeepsWritesInsideRepo arms core.worktree with an
// absolute path outside the repository and drives checkout plus reset
// --hard through GitCmdInRepo. Verified on git 2.50.1: no -c form beats the
// config key (an empty value is ignored too) — only the GIT_WORK_TREE env
// pin outranks it, so tracked files must materialize INSIDE the repository
// root and nothing may appear at the hostile path.
func TestCanaryWorkTreeEnvPinKeepsWritesInsideRepo(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	f.repo.Git(t, "checkout", "-b", "feature")
	f.repo.Write(t, "file.txt", "feature side\n")
	f.repo.Git(t, "add", ".")
	f.repo.Git(t, "commit", "-m", "feature side")
	f.repo.Git(t, "checkout", "main")

	external := filepath.Join(t.TempDir(), "outside")
	f.repo.AppendConfig(t, "[core]\n\tworktree = "+external)
	f.requireFinding(GitConfigFindingWorkTree)

	requireNothingAt := func() {
		t.Helper()
		if _, err := os.Stat(external); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("hostile worktree path %s must stay untouched, got stat error %v", external, err)
		}
	}

	// checkout through the hardened spawn: tracked files materialize
	// inside the repository root.
	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "checkout", "feature")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("checkout failed: %v (%s)", err, out.String())
	}
	if got, err := os.ReadFile(filepath.Join(f.repo.Root, "file.txt")); err != nil || string(got) != "feature side\n" {
		t.Fatalf("file.txt inside the repo root = %q (%v), want the feature content", got, err)
	}
	requireNothingAt()

	// reset --hard through the hardened spawn restores the index INSIDE
	// the repository root, never at the hostile path.
	f.repo.Write(t, "file.txt", "tampered\n")
	cmd, err = GitCmdInRepo(f.ctx, f.repo.Root, "reset", "--hard")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	out.Reset()
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("reset --hard failed: %v (%s)", err, out.String())
	}
	if got, err := os.ReadFile(filepath.Join(f.repo.Root, "file.txt")); err != nil || string(got) != "feature side\n" {
		t.Fatalf("file.txt inside the repo root = %q (%v), want the restored content", got, err)
	}
	requireNothingAt()

	// Non-vacuity control: raw git honors the hostile core.worktree — with
	// the target directory present (the attacker controls its creation),
	// a checkout writes the tracked file to the external path. The
	// hardened runs above created nothing there, so the directory is made
	// only now, for the control alone.
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatalf("creating the control's hostile dir: %v", err)
	}
	runRawGitControl(t, f.repo.Root, "", "checkout", "main")
	if _, err := os.Stat(filepath.Join(external, "file.txt")); err != nil {
		t.Fatalf("control must prove the fixture armed: raw git checkout should write to the hostile worktree: %v", err)
	}
}

// TestCanaryIncludeHiddenSSHCommandNeutered hides core.sshCommand in an
// included file the scanner deliberately never reads. The include record
// derives the name-independent core.sshCommand=ssh pin. The hidden key only
// executes on an ssh:// remote fetch (network), so the neutralization is
// pinned structurally via the fake-git harness: the include-derived pin must
// reach the spawn argv and the hidden script must not.
func TestCanaryIncludeHiddenSSHCommandNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "hiddenssh", gittest.HookBody)
	extra := filepath.Join(f.repo.Root, "hidden.conf")
	if err := os.WriteFile(extra, []byte("[core]\n\tsshCommand = "+script+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.AppendConfig(t, "[include]\n\tpath = "+extra+"\n")

	// The hidden key itself is invisible to the scan (the include is not
	// followed): the include record plus the name-independent pins are the
	// whole defense.
	info, err := ScanGitConfig(f.repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if len(info.Includes) != 1 {
		t.Fatalf("includes = %+v, want the one include", info.Includes)
	}
	argvs := overrideArgvs(info)
	for _, want := range []string{"core.sshCommand=ssh", "core.askPass=", "credential.helper=", "diff.external="} {
		if !slices.Contains(argvs, want) {
			t.Fatalf("include-derived overrides = %v, want %q among them", argvs, want)
		}
	}

	// Pin the spawn argv without network: the include-bearing path gates on
	// git >= 2.45 (attr.tree), so inject a capable version to avoid probing
	// real git under the fake-git PATH, then assert the pin reaches argv.
	injectGitVersion(t, "git version 2.50.1\n", nil)
	argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, "fetch", "origin")
	if !slices.Contains(argv, "core.sshCommand=ssh") {
		t.Errorf("argv = %q, want the include-derived core.sshCommand=ssh pin", argv)
	}
	if slices.Contains(argv, script) {
		t.Errorf("argv = %q, must not carry the include-hidden sshCommand", argv)
	}
}

// TestCanaryIncludeHiddenDiffExternalNeutered hides diff.external in an
// included file. The include record derives the name-independent
// diff.external= (empty) kill — which beats file config no matter where the
// key is defined. Patch-producing callers must also pass --no-ext-diff: Git
// otherwise tries to execute the deliberately empty override. The hardened
// diff must render the staged change without executing the hidden command.
func TestCanaryIncludeHiddenDiffExternalNeutered(t *testing.T) {
	skipUnlessAttrTreeCapableGit(t)
	f := newCanaryFixture(t, "hello\n")
	f.repo.Write(t, "file.txt", "hello\nchanged\n")
	f.repo.Git(t, "add", ".")
	script := f.canary.Plant(t, "hiddendiff", gittest.HookBody)
	extra := filepath.Join(f.repo.Root, "hidden.conf")
	if err := os.WriteFile(extra, []byte("[diff]\n\texternal = "+script+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.AppendConfig(t, "[include]\n\tpath = "+extra+"\n")

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "diff", "--no-ext-diff", "--cached")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("hardened diff --no-ext-diff --cached: %v (%s)", err, out.String())
	}
	if !contains(out.String(), "+changed") {
		t.Fatalf("hardened diff missing staged change:\n%s", out.String())
	}
	f.canary.RequireNotFired(t)

	// Non-vacuity control: without the hardened argv, raw git executes the
	// include-hidden external command. This proves the protected run above
	// was not merely a benign fixture.
	runRawGitControl(t, f.repo.Root, "", "diff", "--cached")
	if !f.canary.Fired(t) {
		t.Fatal("raw git control must execute the include-hidden external diff command")
	}
}
