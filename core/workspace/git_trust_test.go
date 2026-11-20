package workspace

import (
	"bytes"
	"context"
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
// the full neutralization (the existing canary suite). Every raw-eligible
// spawn additionally re-checks the semantic git-config fingerprint the
// decision was bound to: a plain checkout scans its own config, a linked
// worktree re-derives the identity from the TRUST ROOT (the common config is
// shared; the worktree's own overlay stays on the separate overlay gate) —
// drift since the decision evicts the trust and hardens the spawn, and a
// fingerprint-less registry entry (legacy `gittrust.Trust`) never spawns
// raw. The registry is process-wide, so every test here clears it up
// front and on teardown.

// trustFingerprinted registers root in the gittrust registry the way the
// backend's TrustGitRepo pipeline does: bound to the semantic fingerprint of
// the repository's current config (scan + include resolution), so the spawn
// layer's identity check passes.
func trustFingerprinted(t *testing.T, root string) {
	t.Helper()
	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig for trust registration: %v", err)
	}
	info.ResolveIncludes()
	gittrust.TrustWithFingerprint(root, info.SemanticFingerprint())
}

// TestGitCmdInRepoTrustedRepoSpawnsRawGit plants a filter finding (which the
// hardened path would neutralize with -c filter.* overrides) and then trusts
// the repository: GitCmdInRepo must produce argv == [git, <args...>] with
// cmd.Env equal to the inherited parent environment plus only the
// GIT_OPTIONAL_LOCKS=0 watcher-loop pin, proving no baseline, no
// NeutralizingArgv and no GIT_EDITOR pin reach a trusted spawn.
func TestGitCmdInRepoTrustedRepoSpawnsRawGit(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	f := newCanaryFixture(t, "hello\n")
	f.repo.AppendConfig(t, "[filter \"lfs\"]\n\tprocess = /tmp/evil-filter.sh\n\tclean = /tmp/evil-clean.sh\n\tsmudge = /tmp/evil-smudge.sh\n")
	f.requireFinding(GitConfigFindingFilter) // armed: hardened path would neutralize it

	root := f.repo.Root
	trustFingerprinted(t, root)

	cmd, err := GitCmdInRepo(f.ctx, root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted): %v", err)
	}

	want := []string{"git", "status", "--porcelain"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("trusted argv = %q, want raw %q (no -c overrides)", cmd.Args, want)
	}
	if cmd.Env == nil {
		t.Fatal("trusted cmd.Env must carry the GIT_OPTIONAL_LOCKS pin, got nil")
	}
	if want := pinGitEnv(os.Environ(), "GIT_OPTIONAL_LOCKS", "0"); !slices.Equal(cmd.Env, want) {
		t.Errorf("trusted cmd.Env must be the parent environment plus the pin only:\n got %q\nwant %q", cmd.Env, want)
	}
	if slices.Contains(cmd.Env, "GIT_EDITOR=true") {
		t.Error("trusted cmd.Env must not carry the hardened GIT_EDITOR pin")
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
	trustFingerprinted(t, f.repo.Root)

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
	if !slices.Contains(cmd.Env, "GIT_OPTIONAL_LOCKS=0") {
		t.Errorf("trusted-subdir cmd.Env missing the GIT_OPTIONAL_LOCKS=0 pin: %q", cmd.Env)
	}
}

// TestGitCmdInRepoTrustedByLinkedWorktree pins the worktree unification: the
// trust decision keys on the repository trust root (ResolveGitTrustRoot —
// the MAIN work-tree root), so a repository trusted through its main
// checkout runs raw git from a linked worktree too. The hooks and common
// config a worktree commit executes are shared with the main checkout;
// keying on the worktree path instead would leave commits in a "trusted
// repository's" worktrees hardened, with the Commit-withheld dialog's Trust
// action looping on the already-stored root (the bug this fixes).
func TestGitCmdInRepoTrustedByLinkedWorktree(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	repo, wt := worktreeFixture(t)
	// Trust recorded for the MAIN root — the form backend's TrustGitRepo
	// stores once normalized through ResolveGitTrustRoot — bound to the
	// semantic fingerprint: the worktree path re-derives the identity from
	// the trust root, so a fingerprint-less entry would keep the hardened
	// path (the same fail-closed rule as a plain checkout).
	trustFingerprinted(t, repo.Root)

	cmd, err := GitCmdInRepo(context.Background(), wt, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted worktree): %v", err)
	}
	want := []string{"git", "rev-parse", "--is-inside-work-tree"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("trusted-worktree argv = %q, want raw %q (no -c overrides)", cmd.Args, want)
	}
	if !slices.Contains(cmd.Env, "GIT_OPTIONAL_LOCKS=0") {
		t.Errorf("trusted-worktree cmd.Env missing the GIT_OPTIONAL_LOCKS=0 pin: %q", cmd.Env)
	}
	if slices.Contains(cmd.Env, "GIT_EDITOR=true") {
		t.Error("trusted-worktree cmd.Env must not carry the hardened GIT_EDITOR pin")
	}
}

// TestGitCmdInRepoTrustedWorktreeLegacyEntry pins backward compatibility: an
// entry stored under the WORKTREE root itself (the pre-unification keying,
// written by older builds or by hand) still opts the worktree into raw git.
func TestGitCmdInRepoTrustedWorktreeLegacyEntry(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	_, wt := worktreeFixture(t)
	// Legacy worktree-keyed entry, bound to the semantic fingerprint the
	// same way: even the degraded keying verifies its identity before a raw
	// spawn — a fingerprint-less entry hardens.
	trustFingerprinted(t, wt)

	cmd, err := GitCmdInRepo(context.Background(), wt, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("GitCmdInRepo (legacy worktree trust): %v", err)
	}
	want := []string{"git", "rev-parse", "--is-inside-work-tree"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("legacy-trusted-worktree argv = %q, want raw %q", cmd.Args, want)
	}
}

// TestGitCmdInRepoTrustedRepoPinsOptionalLocks pins the one deliberate
// deviation trusted spawns keep from the raw parent environment:
// GIT_OPTIONAL_LOCKS=0, c0wrk's own watcher-loop prevention (fcb592bf) —
// read-only status refreshes must not opportunistically rewrite .git/index,
// or the fsnotify REMOVE+CREATE events re-trigger the very status refresh
// that rewrote it. The pin must override a user-exported GIT_OPTIONAL_LOCKS
// inherited from the parent environment, and the spawn must stay
// functional.
func TestGitCmdInRepoTrustedRepoPinsOptionalLocks(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)
	// A user-exported value must not re-enable the opportunistic index write.
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")

	f := newCanaryFixture(t, "hello\n")
	trustFingerprinted(t, f.repo.Root)

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted): %v", err)
	}
	if cmd.Env == nil {
		t.Fatal("trusted cmd.Env must carry the GIT_OPTIONAL_LOCKS pin, got nil")
	}
	if !slices.Contains(cmd.Env, "GIT_OPTIONAL_LOCKS=0") {
		t.Errorf("trusted cmd.Env missing the GIT_OPTIONAL_LOCKS=0 pin: %q", cmd.Env)
	}
	if want := pinGitEnv(os.Environ(), "GIT_OPTIONAL_LOCKS", "0"); !slices.Equal(cmd.Env, want) {
		t.Errorf("trusted cmd.Env must be the parent environment plus the pin only (inherited value overridden):\n got %q\nwant %q", cmd.Env, want)
	}

	// The pinned spawn stays functional.
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("trusted pinned git status: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("trusted pinned git status unexpected output: %q", out.String())
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

// writeWorktreeOverlay enables extensions.worktreeConfig in the common config
// and writes the given content to the linked worktree's own config.worktree
// overlay (the per-worktree config source git reads live for that worktree).
func writeWorktreeOverlay(t *testing.T, repo *gittest.Repo, wt, content string) {
	t.Helper()
	repo.AppendConfig(t, "[extensions]\n\tworktreeConfig = true\n")
	wtGitDir := filepath.Join(evalDir(t, repo.Root), ".git", "worktrees", filepath.Base(wt))
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree gitdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "config.worktree"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.worktree: %v", err)
	}
}

// TestGitCmdInRepoTrustedWorktreeDirtyOverlayHardens pins the per-worktree
// overlay gate: the repository trust decision (keyed on the MAIN root, the
// form backend's TrustGitRepo stores) never bound a linked worktree's own
// config.worktree overlay — the one per-worktree config source git reads
// live — so a worktree whose overlay arms a dangerous key keeps the hardened
// baseline, which neutralizes the overlay's keys from the same scan, instead
// of executing configuration the user never reviewed. This is the hole the
// pre-unification model covered by keying trust on the worktree itself and
// the unification re-opened: without the gate the overlay runs raw on every
// worktree operation, plantable at any time after the trust decision with no
// detection path. Covers both the worktree root and a subdirectory inside it
// (both resolve to the same worktree gitdir and trust root).
func TestGitCmdInRepoTrustedWorktreeDirtyOverlayHardens(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	repo, wt := worktreeFixture(t)
	writeWorktreeOverlay(t, repo, wt, "[core]\n\tfsmonitor = /tmp/evil-wt-fsmonitor.sh\n")

	// Fingerprint-bound trust for the MAIN root (the raw-spawn registration
	// convention): the snapshot scanned the main checkout and never saw the
	// overlay written above, so only the overlay gate can refuse this spawn —
	// a fingerprint-less entry would harden via the identity check and mask
	// an overlay-gate regression.
	trustFingerprinted(t, repo.Root)

	sub := filepath.Join(wt, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir worktree subdir: %v", err)
	}
	for _, path := range []string{wt, sub} {
		cmd, err := GitCmdInRepo(context.Background(), path, "status", "--porcelain")
		if err != nil {
			t.Fatalf("GitCmdInRepo (trusted worktree, dirty overlay, %s): %v", path, err)
		}
		joined := strings.Join(cmd.Args, " ")
		if !strings.Contains(joined, "-c core.fsmonitor=false") {
			t.Errorf("argv (%s) = %q, want the hardened baseline with the overlay's core.fsmonitor neutralized", path, cmd.Args)
		}
		if !slices.Contains(cmd.Env, "GIT_EDITOR=true") {
			t.Errorf("cmd.Env (%s) missing the hardened GIT_EDITOR pin: %q", path, cmd.Env)
		}
	}
}

// TestGitCmdInRepoTrustedWorktreeCleanOverlaySpawnsRaw pins the other side of
// the overlay gate: a worktree whose overlay carries nothing dangerous — the
// common case, git users legitimately keep per-worktree settings there —
// still runs raw git under the repository's trust decision. The gate costs
// one scan per operation, not the trust itself.
func TestGitCmdInRepoTrustedWorktreeCleanOverlaySpawnsRaw(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	repo, wt := worktreeFixture(t)
	writeWorktreeOverlay(t, repo, wt, "[alias]\n\tst = status\n")
	// Fingerprint-bound trust (the raw-spawn registration convention): the
	// worktree spawn re-derives the trust root's identity below.
	trustFingerprinted(t, repo.Root)

	cmd, err := GitCmdInRepo(context.Background(), wt, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted worktree, clean overlay): %v", err)
	}
	want := []string{"git", "status", "--porcelain"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("trusted-worktree argv = %q, want raw %q (no -c overrides)", cmd.Args, want)
	}
	if !slices.Contains(cmd.Env, "GIT_OPTIONAL_LOCKS=0") {
		t.Errorf("trusted-worktree cmd.Env missing the GIT_OPTIONAL_LOCKS=0 pin: %q", cmd.Env)
	}
	if slices.Contains(cmd.Env, "GIT_EDITOR=true") {
		t.Error("trusted-worktree cmd.Env must not carry the hardened GIT_EDITOR pin")
	}
}

// TestGitCmdInRepoTrustedWorktreeOverlayIncludeHardens pins the include
// direction of the overlay gate: an include directive in the overlay points
// at a source the scanner deliberately does not follow (ADR-033), so its
// content can never be proven safe — the gate refuses the raw spawn (the
// ignored-include warning fires exactly once, from the scan that retained
// the overlay parse) and the hardened baseline runs instead.
func TestGitCmdInRepoTrustedWorktreeOverlayIncludeHardens(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	repo, wt := worktreeFixture(t)
	writeWorktreeOverlay(t, repo, wt, "[include]\n\tpath = ./evil\n")
	// Fingerprint-bound trust (the raw-spawn registration convention): the
	// spawn must harden via the overlay include, not via a fingerprint-less
	// identity refusal — that keeps this test pinning the overlay gate.
	trustFingerprinted(t, repo.Root)

	logger := expectedDiagnostics(t, ignoredIncludeDiagnostic(2, "./evil"))
	ctx := gitDiagnosticContext(context.Background(), logger)

	cmd, err := GitCmdInRepo(ctx, wt, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted worktree, overlay include): %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-c core.fsmonitor=false") {
		t.Errorf("argv = %q, want the hardened baseline for an include-bearing overlay", cmd.Args)
	}
}

// TestGitCmdInRepoTrustedWorktreeCommonConfigDriftHardens pins the
// common-config identity check on the worktree spawn path (the gap a linked
// worktree's raw spawn had: gated only on its own overlay, drift of the
// SHARED common config stayed invisible until the next main-checkout spawn —
// so a config edited after the trust decision rode a worktree session's
// auto-fetch or panel refresh raw). The worktree spawn re-derives the trust
// root's semantic fingerprint: drift evicts the trust and the spawn hardens,
// exactly like the plain path.
func TestGitCmdInRepoTrustedWorktreeCommonConfigDriftHardens(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	repo, wt := worktreeFixture(t)
	trustFingerprinted(t, repo.Root)

	// Drift the COMMON config — the shared file every checkout reads — after
	// the decision: a command-bearing key the trust never reviewed.
	repo.AppendConfig(t, "[core]\n\tfsmonitor = /tmp/evil-wt-fsmonitor.sh\n")

	cmd, err := GitCmdInRepo(context.Background(), wt, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted worktree, common-config drift): %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-c core.fsmonitor=false") {
		t.Errorf("argv = %q, want the hardened baseline with the drifted common-config key neutralized", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "GIT_EDITOR=true") {
		t.Error("the drifted spawn must carry the hardened GIT_EDITOR pin")
	}

	// The drift evicted the trust: the registry entry is gone, so the next
	// spawn (worktree or main checkout) stays hardened until re-trust.
	if gittrust.IsTrusted(repo.Root) {
		t.Error("the common-config drift must evict the trust from the registry")
	}
}

// TestWorktreeOverlayClean pins the overlay predicate the trusted-worktree
// spawn gate consults, per shape: no overlay in play is trivially clean; a
// benign overlay is clean; a dangerous overlay key, an overlay re-arming a
// common-config key with a new value, an include directive, a parse error,
// and a named-but-unretained overlay are not. Signing siblings follow the
// merged model's inert classification: dead without an armed commit.gpgsign
// anywhere, live (and gate-tripping) when the trusted common config arms it.
func TestWorktreeOverlayClean(t *testing.T) {
	t.Run("nil info", func(t *testing.T) {
		var info *GitConfigInfo
		if !info.WorktreeOverlayClean() {
			t.Error("nil info must be trivially clean")
		}
	})

	t.Run("no overlay in play", func(t *testing.T) {
		_, wt := worktreeFixture(t)
		info, err := ScanGitConfig(wt)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if info.WorktreeConfigPath != "" {
			t.Fatalf("WorktreeConfigPath = %q, want empty with the extension disabled", info.WorktreeConfigPath)
		}
		if !info.WorktreeOverlayClean() {
			t.Error("a scan without an overlay in play must be trivially clean")
		}
	})

	t.Run("benign overlay", func(t *testing.T) {
		repo, wt := worktreeFixture(t)
		writeWorktreeOverlay(t, repo, wt, "[alias]\n\tst = status\n")
		info, err := ScanGitConfig(wt)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if !info.WorktreeOverlayClean() {
			t.Error("a benign overlay must be clean")
		}
	})

	t.Run("dangerous overlay key", func(t *testing.T) {
		repo, wt := worktreeFixture(t)
		writeWorktreeOverlay(t, repo, wt, "[core]\n\tfsmonitor = /tmp/evil.sh\n")
		info, err := ScanGitConfig(wt)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if info.WorktreeOverlayClean() {
			t.Error("an overlay arming core.fsmonitor must not be clean")
		}
	})

	t.Run("overlay re-arms a common-config key", func(t *testing.T) {
		repo, wt := worktreeFixture(t)
		writeWorktreeOverlay(t, repo, wt, "[core]\n\tfsmonitor = /tmp/evil.sh\n")
		// The COMMON config also sets the key (a value the trust snapshot
		// may have bound); the overlay's value wins git's layering and must
		// gate the spawn even though the key itself was reviewed.
		cfg := filepath.Join(evalDir(t, repo.Root), ".git", "config")
		data, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg, append([]byte("[core]\n\tfsmonitor = /tmp/safe.sh\n"), data...), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := ScanGitConfig(wt)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if info.WorktreeOverlayClean() {
			t.Error("an overlay re-arming a reviewed key with a new value must not be clean")
		}
	})

	t.Run("signing sibling without arming stays clean", func(t *testing.T) {
		repo, wt := worktreeFixture(t)
		writeWorktreeOverlay(t, repo, wt, "[user]\n\tsigningkey = ABCDEF\n")
		info, err := ScanGitConfig(wt)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if !info.WorktreeOverlayClean() {
			t.Error("a dead signing sibling (nothing arms commit.gpgsign) must stay clean")
		}
	})

	t.Run("signing sibling with common-config arming gates", func(t *testing.T) {
		repo, wt := worktreeFixture(t)
		writeWorktreeOverlay(t, repo, wt, "[user]\n\tsigningkey = ABCDEF\n")
		// The trusted common config arms signing: the overlay's gpg.program
		// would EXECUTE as the signing program, so the sibling is live.
		repo.AppendConfig(t, "[commit]\n\tgpgsign = true\n")
		info, err := ScanGitConfig(wt)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if info.WorktreeOverlayClean() {
			t.Error("an overlay signing sibling under an armed commit.gpgsign must not be clean")
		}
	})

	t.Run("overlay include directive", func(t *testing.T) {
		repo, wt := worktreeFixture(t)
		writeWorktreeOverlay(t, repo, wt, "[include]\n\tpath = ./evil\n")
		logger := expectedDiagnostics(t, ignoredIncludeDiagnostic(2, "./evil"))
		info, err := ScanGitConfig(wt, logger)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if info.WorktreeOverlayClean() {
			t.Error("an include-bearing overlay must not be clean (the target is never read)")
		}
	})

	t.Run("overlay parse error", func(t *testing.T) {
		repo, wt := worktreeFixture(t)
		writeWorktreeOverlay(t, repo, wt, "[core\n\tfsmonitor = x\n")
		info, err := ScanGitConfig(wt)
		if err != nil {
			t.Fatalf("ScanGitConfig: %v", err)
		}
		if info.WorktreeOverlayClean() {
			t.Error("a malformed overlay must not be clean (fail closed)")
		}
	})

	t.Run("named overlay not retained", func(t *testing.T) {
		info := &GitConfigInfo{WorktreeConfigPath: "/some/config.worktree"}
		if info.WorktreeOverlayClean() {
			t.Error("an overlay the scan did not retain cannot be proven safe (fail closed)")
		}
	})
}

// TestGitCmdInRepoTrustedWithoutFingerprintHardens pins the spawn-side
// fail-closed for entries registered without a semantic identity — the form
// gittrust.Trust produces and a v1 trust record mirrors before its next-open
// migration: the decision cannot be checked against the live config, so the
// repository keeps the full hardened baseline instead of raw git.
func TestGitCmdInRepoTrustedWithoutFingerprintHardens(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	f := newCanaryFixture(t, "hello\n")
	f.repo.AppendConfig(t, "[filter \"lfs\"]\n\tprocess = /tmp/evil-filter.sh\n\tclean = /tmp/evil-clean.sh\n\tsmudge = /tmp/evil-smudge.sh\n")
	f.requireFinding(GitConfigFindingFilter)

	gittrust.Trust(f.repo.Root) // no fingerprint: an unverifiable decision

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted, no fingerprint): %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-c filter.lfs.process=") {
		t.Errorf("argv = %q, want the hardened baseline for a fingerprint-less trust entry", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "GIT_EDITOR=true") {
		t.Errorf("cmd.Env missing the hardened GIT_EDITOR pin: %q", cmd.Env)
	}
}

// TestGitCmdInRepoTrustedFingerprintDriftEvicts pins the spawn-path semantic
// recheck: a trusted repository whose command-bearing config changes after
// the trust decision stops spawning raw on the very next operation — the
// spawn hardens, the registry entry is evicted, and the backend-registered
// drift handler learns the root so it can remove the config record and
// re-warn. Without this gate every non-intake GitCmdInRepo caller (auto-fetch,
// panel refreshes) ran whatever config stood in the repo at spawn time.
func TestGitCmdInRepoTrustedFingerprintDriftEvicts(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(func() {
		gittrust.SetDriftHandler(nil)
		gittrust.Clear()
	})

	f := newCanaryFixture(t, "hello\n")
	root := f.repo.Root
	trustFingerprinted(t, root)

	// Trust bound to the current config: raw spawn works.
	cmd, err := GitCmdInRepo(f.ctx, root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted): %v", err)
	}
	if want := []string{"git", "status", "--porcelain"}; !slices.Equal(cmd.Args, want) {
		t.Fatalf("trusted argv = %q, want raw %q", cmd.Args, want)
	}

	// Drift AFTER trust: an fsmonitor armed where the decision saw none.
	f.repo.AppendConfig(t, "[core]\n\tfsmonitor = /tmp/evil-fsmonitor.sh\n")

	var drifted []string
	gittrust.SetDriftHandler(func(r string) { drifted = append(drifted, r) })

	cmd, err = GitCmdInRepo(f.ctx, root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (drifted): %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-c core.fsmonitor=false") {
		t.Errorf("argv = %q, want the hardened baseline after semantic drift", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "GIT_EDITOR=true") {
		t.Errorf("cmd.Env missing the hardened GIT_EDITOR pin after drift: %q", cmd.Env)
	}
	if gittrust.IsTrusted(root) {
		t.Error("a drifted trusted repository must be evicted from the registry")
	}
	if len(drifted) != 1 || drifted[0] != filepath.Clean(root) {
		t.Fatalf("drift handler invocations = %q, want exactly [%s]", drifted, filepath.Clean(root))
	}

	// The eviction stands: with the registry entry gone the next operation
	// takes the ordinary untrusted path without re-firing the handler.
	cmd, err = GitCmdInRepo(f.ctx, root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (after eviction): %v", err)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "-c core.fsmonitor=false") {
		t.Errorf("argv = %q, want the hardened path to stand after eviction", cmd.Args)
	}
	if len(drifted) != 1 {
		t.Errorf("drift handler fired %d times, want 1 (the eviction must stand)", len(drifted))
	}
}

// TestGitCmdInRepoTrustedIncludeTargetDriftEvicts pins the TOCTOU closure on
// the exact vector the spawn-path recheck exists for: the trust snapshot
// covers include TARGETS' bytes, so a target file edited after the trust
// decision — which the pre-fix spawn path executed raw, with no recheck
// between the user's decision and the spawn — reads as semantic drift and
// evicts before git can run the smuggled configuration.
func TestGitCmdInRepoTrustedIncludeTargetDriftEvicts(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(func() {
		gittrust.SetDriftHandler(nil)
		gittrust.Clear()
	})

	f := newCanaryFixture(t, "hello\n")
	root := f.repo.Root
	// An include directive in .git/config whose target resolves against the
	// .git directory — the shape a narrowed agent can edit inside the
	// workspace.
	target := f.repo.GitDirFile("trusted-git.conf")
	if err := os.WriteFile(target, []byte("[alias]\n\tst = status\n"), 0o644); err != nil {
		t.Fatalf("writing include target: %v", err)
	}
	f.repo.AppendConfig(t, "[include]\n\tpath = trusted-git.conf")
	trustFingerprinted(t, root)

	// Trust bound to the resolved include content: raw spawn works.
	cmd, err := GitCmdInRepo(f.ctx, root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (trusted): %v", err)
	}
	if want := []string{"git", "status", "--porcelain"}; !slices.Equal(cmd.Args, want) {
		t.Fatalf("trusted argv = %q, want raw %q", cmd.Args, want)
	}

	// The smuggled edit: the include target arms a transport command the
	// decision never saw.
	if err := os.WriteFile(target, []byte("[core]\n\tsshCommand = /tmp/evil-ssh.sh\n"), 0o644); err != nil {
		t.Fatalf("smuggling into the include target: %v", err)
	}

	var drifted []string
	gittrust.SetDriftHandler(func(r string) { drifted = append(drifted, r) })

	cmd, err = GitCmdInRepo(f.ctx, root, "status", "--porcelain")
	if err != nil {
		t.Fatalf("GitCmdInRepo (drifted include target): %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "-c core.sshCommand=ssh") {
		t.Errorf("argv = %q, want the hardened baseline with core.sshCommand pinned after include-target drift", cmd.Args)
	}
	if gittrust.IsTrusted(root) {
		t.Error("a repository whose include target drifted must be evicted from the registry")
	}
	if len(drifted) != 1 || drifted[0] != filepath.Clean(root) {
		t.Fatalf("drift handler invocations = %q, want exactly [%s]", drifted, filepath.Clean(root))
	}
}

// TestGitCmdInRepoTrustedUnscannableConfigRefuses pins the fail-closed scan
// error on the trusted plain-checkout path: a config that stops being
// readable never upgrades to raw git — the spawn refuses with the scan error,
// exactly as for an untrusted repository.
func TestGitCmdInRepoTrustedUnscannableConfigRefuses(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	f := newCanaryFixture(t, "hello\n")
	root := f.repo.Root
	trustFingerprinted(t, root)

	configPath := f.repo.GitDirFile("config")
	if err := os.Chmod(configPath, 0o000); err != nil {
		t.Fatalf("chmod config: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(configPath, 0o644) })

	if _, err := GitCmdInRepo(f.ctx, root, "status", "--porcelain"); err == nil {
		t.Fatal("GitCmdInRepo must refuse to spawn git when a trusted repository's config cannot be scanned (fail closed)")
	}
}
