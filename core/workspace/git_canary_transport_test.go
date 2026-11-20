package workspace

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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

// TestCanaryUploadPackNeutered arms remote.origin.uploadpack with a canary
// script and a local-path remote — the one remote shape where the value
// executes locally on the first fetch, with no network and no user
// interaction (verified on git 2.56.0). The spawn argv must carry BOTH the
// command-line --upload-pack=git-upload-pack pin (which beats file config
// regardless of remote name) and the per-name -c pin derived from the scan,
// and must never carry the planted value; the real hardened fetch must
// succeed through the default program without firing the canary, and the
// raw-git control must fire it — proving the fixture is armed.
func TestCanaryUploadPackNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	origin := gittest.InitRepo(t, filepath.Join(t.TempDir(), "origin"), "seed\n")
	script := f.canary.Plant(t, "uploadpack", gittest.HookBody)
	f.repo.AppendConfig(t, "[remote \"origin\"]\n\turl = "+origin.Root+"\n\tuploadpack = "+script+"\n")
	f.requireFinding(GitConfigFindingUploadPack)

	// End-to-end: the real local-path fetch runs the DEFAULT remote-side
	// program and completes; the planted value never executes. (Real git
	// before the fake-git harness below — fake git replaces PATH.)
	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "fetch", "origin")
	if err != nil {
		t.Fatalf("GitCmdInRepo fetch: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("hardened fetch: %v (%s)", err, out.String())
	}
	f.canary.RequireNotFired(t)

	// Non-vacuity control: raw git executes the armed uploadpack.
	runRawGitControl(t, f.repo.Root, "", "fetch", "origin")
	if !f.canary.Fired(t) {
		t.Fatal("raw git control must execute the armed remote.origin.uploadpack; fixture was not armed")
	}

	// Structural: the spawn argv carries the command-line pin and the
	// scan-derived per-name -c pin, never the planted script.
	argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, "fetch", "origin")
	if !slices.Contains(argv, "--upload-pack=git-upload-pack") {
		t.Errorf("argv = %q, want the --upload-pack=git-upload-pack spawn pin", argv)
	}
	if !slices.Contains(argv, "remote.origin.uploadpack=git-upload-pack") {
		t.Errorf("argv = %q, want the remote.origin.uploadpack=git-upload-pack -c pin", argv)
	}
	if slices.Contains(argv, script) {
		t.Errorf("argv = %q, must not carry the repository's uploadpack", argv)
	}
}

// TestCanaryReceivePackNeutered arms remote.origin.receivepack against a
// bare local-path remote — the push-side twin of the uploadpack vector: the
// value executes locally on the first push (verified on git 2.56.0). The
// spawn argv must carry the --receive-pack=git-receive-pack pin and the
// per-name -c pin, the real hardened push must land the ref without firing
// the canary, and the raw-git control must fire it.
func TestCanaryReceivePackNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	bare := filepath.Join(t.TempDir(), "remote.git")
	f.repo.Git(t, "init", "--bare", bare)
	script := f.canary.Plant(t, "receivepack", gittest.HookBody)
	f.repo.AppendConfig(t, "[remote \"origin\"]\n\turl = "+bare+"\n\treceivepack = "+script+"\n")
	f.requireFinding(GitConfigFindingReceivePack)

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "push", "origin", "HEAD")
	if err != nil {
		t.Fatalf("GitCmdInRepo push: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("hardened push: %v (%s)", err, out.String())
	}
	f.canary.RequireNotFired(t)
	// Neutralization must not break the operation: the ref landed on the
	// bare remote.
	if _, err := os.Stat(filepath.Join(bare, "refs", "heads")); err != nil {
		t.Fatalf("pushed ref missing on the bare remote: %v", err)
	}

	runRawGitControl(t, f.repo.Root, "", "push", "origin", "HEAD")
	if !f.canary.Fired(t) {
		t.Fatal("raw git control must execute the armed remote.origin.receivepack; fixture was not armed")
	}

	argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, "push", "origin", "HEAD")
	if !slices.Contains(argv, "--receive-pack=git-receive-pack") {
		t.Errorf("argv = %q, want the --receive-pack=git-receive-pack spawn pin", argv)
	}
	if !slices.Contains(argv, "remote.origin.receivepack=git-receive-pack") {
		t.Errorf("argv = %q, want the remote.origin.receivepack=git-receive-pack -c pin", argv)
	}
	if slices.Contains(argv, script) {
		t.Errorf("argv = %q, must not carry the repository's receivepack", argv)
	}
}

// TestCanaryIncludeHiddenUploadPackNeutered hides remote.<n>.uploadpack in
// an included file. The scan never follows includes, so no finding and no
// per-name -c pin exist — there is NO name-independent -c key for remote
// transports, which is exactly why the spawn-level --upload-pack pin is the
// whole defense on this vector. The include record must not derive any
// remote.* override, the hardened real fetch must not execute the hidden
// value, and the raw-git control must fire it.
func TestCanaryIncludeHiddenUploadPackNeutered(t *testing.T) {
	skipUnlessAttrTreeCapableGit(t)
	f := newCanaryFixture(t, "hello\n")
	origin := gittest.InitRepo(t, filepath.Join(t.TempDir(), "origin"), "seed\n")
	script := f.canary.Plant(t, "hiddenuploadpack", gittest.HookBody)
	extra := filepath.Join(f.repo.Root, "hidden.conf")
	if err := os.WriteFile(extra, []byte("[remote \"evil\"]\n\turl = "+origin.Root+"\n\tuploadpack = "+script+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.AppendConfig(t, "[include]\n\tpath = "+extra+"\n")

	// Three scans run below (the explicit ScanGitConfig, the hardened real
	// fetch, the fake-git argv pin) — each fires the include warning.
	logger := expectedDiagnostics(t,
		plantedIncludeDiagnostic(t, f.repo.Root, extra),
		plantedIncludeDiagnostic(t, f.repo.Root, extra),
		plantedIncludeDiagnostic(t, f.repo.Root, extra))
	f.ctx = gitDiagnosticContext(f.ctx, logger)

	// The hidden key is invisible to the scan: no finding, and the
	// include-derived overrides must not attempt any remote.* pin (none can
	// be name-independent).
	info, err := ScanGitConfig(f.repo.Root, logger)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	for i := range info.Findings {
		if info.Findings[i].Kind == GitConfigFindingUploadPack || info.Findings[i].Kind == GitConfigFindingReceivePack {
			t.Fatalf("scan must not report include-hidden remote transport keys as findings: %+v", info.Findings[i])
		}
	}
	argvs := overrideArgvs(info)
	if slices.ContainsFunc(argvs, func(a string) bool { return strings.HasPrefix(a, "remote.") }) {
		t.Fatalf("include-derived overrides = %v, must not contain remote.* pins", argvs)
	}

	// End-to-end: the real hardened fetch of the include-hidden remote runs
	// the default program; the hidden value never executes.
	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "fetch", "evil")
	if err != nil {
		t.Fatalf("GitCmdInRepo fetch: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("hardened include-hidden fetch: %v (%s)", err, out.String())
	}
	f.canary.RequireNotFired(t)

	runRawGitControl(t, f.repo.Root, "", "fetch", "evil")
	if !f.canary.Fired(t) {
		t.Fatal("raw git control must execute the include-hidden uploadpack; fixture was not armed")
	}

	// Structural: the spawn pin reaches the argv (include-bearing configs
	// gate on git >= 2.45 for attr.tree, so inject a capable version under
	// the fake-git harness).
	injectGitVersion(t, "git version 2.50.1\n", nil)
	argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, "fetch", "evil")
	if !slices.Contains(argv, "--upload-pack=git-upload-pack") {
		t.Errorf("argv = %q, want the --upload-pack=git-upload-pack spawn pin", argv)
	}
	if slices.Contains(argv, script) {
		t.Errorf("argv = %q, must not carry the include-hidden uploadpack", argv)
	}
}

// TestScanGitConfigRemoteTransportKeysDeriveDefaultProgramPins pins the
// scan-level mapping for the transport keys: both keys produce their finding
// kind and the derived per-name -c overrides restore git's default
// remote-side programs.
func TestScanGitConfigRemoteTransportKeysDeriveDefaultProgramPins(t *testing.T) {
	root := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "hello\n").Root
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte(
		"[core]\n\trepositoryformatversion = 0\n"+
			"[remote \"origin\"]\n\turl = /tmp/unused\n\tuploadpack = /bin/evil-upload\n\treceivepack = /bin/evil-receive\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	kinds := map[string]int{}
	for i := range info.Findings {
		kinds[info.Findings[i].Kind]++
	}
	if kinds[GitConfigFindingUploadPack] != 1 || kinds[GitConfigFindingReceivePack] != 1 {
		t.Fatalf("finding kinds = %v, want exactly one upload_pack and one receive_pack", kinds)
	}
	argv, err := info.NeutralizingArgvErr()
	if err != nil {
		t.Fatalf("NeutralizingArgvErr: %v", err)
	}
	for _, want := range []string{
		"remote.origin.uploadpack=git-upload-pack",
		"remote.origin.receivepack=git-receive-pack",
	} {
		if !slices.Contains(argv, want) {
			t.Errorf("neutralizing argv = %q, want %q", argv, want)
		}
	}
}

// TestRemoteTransportPinSubcommandFirstConvention pins the argv position of
// the remote-side transport pin for ALL FOUR transport subcommands (the pin
// must sit right after the subcommand), and the convention tripwire that
// keeps the pin's subcommand-first keying from failing silently open: a call
// that puts a global option before the subcommand gets NO pin — git's
// last-option-wins would otherwise let a caller's own transport flag be
// overridden — and must WARN instead (the load-bearing convention made
// visible; review Issue: the pin keyed on args[0] with nothing pinning the
// shape).
func TestRemoteTransportPinSubcommandFirstConvention(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")

	cases := []struct {
		name string
		args []string
		pin  string
	}{
		{"fetch", []string{"fetch", "origin"}, "--upload-pack=git-upload-pack"},
		{"pull", []string{"pull", "origin"}, "--upload-pack=git-upload-pack"},
		{"ls-remote", []string{"ls-remote", "origin"}, "--upload-pack=git-upload-pack"},
		{"push", []string{"push", "origin", "HEAD"}, "--receive-pack=git-receive-pack"},
	}
	for _, tc := range cases {
		argv := pinGitCmdArgv(f.ctx, t, f.repo.Root, tc.args...)
		// The hardened baseline prepends -c overrides, so the subcommand is
		// not at a fixed index; the pin must sit IMMEDIATELY after it, with
		// the caller's operands following unchanged.
		sub := slices.Index(argv, tc.args[0])
		if sub < 0 {
			t.Fatalf("%s: subcommand %q missing from argv %q", tc.name, tc.args[0], argv)
		}
		if sub+1 >= len(argv) || argv[sub+1] != tc.pin {
			t.Errorf("%s: argv = %q, want the transport pin %q immediately after the subcommand", tc.name, argv, tc.pin)
		}
		if !slices.Equal(argv[sub+2:], tc.args[1:]) {
			t.Errorf("%s: operands after the pin = %q, want %q (args must flow through unchanged)", tc.name, argv[sub+2:], tc.args[1:])
		}
	}

	// Flag-before-subcommand: no pin (by design), and the tripwire WARNs
	// with the exact subcommand and argv. Intercepted and asserted here so
	// the expected diagnostic never reaches the default handler.
	logger := expectedDiagnostics(t, diagnosticExpectation{
		level:   slog.LevelWarn,
		message: "git transport subcommand is not in the first argv position; the remote-side transport pin was not applied (remote.<n>.uploadpack/receivepack are unneutralized for this spawn — keep the subcommand first)",
		attrs:   map[string]string{"subcommand": "fetch", "position": "2"},
	})
	ctx := gitDiagnosticContext(f.ctx, logger)
	argv := pinGitCmdArgv(ctx, t, f.repo.Root, "-c", "test.convention.probe=value", "fetch", "origin")
	if slices.Contains(argv, "--upload-pack=git-upload-pack") {
		t.Errorf("argv = %q, must not carry the transport pin when a global flag precedes the subcommand", argv)
	}
	if !slices.Contains(argv, "fetch") {
		t.Errorf("argv = %q, the subcommand and operands must flow through unchanged", argv)
	}

	// A non-transport call with a value that merely LOOKS like a transport
	// subcommand must not trip the wire (an option value is never a
	// subcommand): "-c key=pull status" stays silent.
	logger = expectedDiagnostics(t)
	ctx = gitDiagnosticContext(f.ctx, logger)
	argv = pinGitCmdArgv(ctx, t, f.repo.Root, "-c", "test.convention.probe=pull", "status", "--porcelain")
	if slices.Contains(argv, "--receive-pack=git-receive-pack") || slices.Contains(argv, "--upload-pack=git-upload-pack") {
		t.Errorf("argv = %q, a status call must never carry a transport pin", argv)
	}
}
