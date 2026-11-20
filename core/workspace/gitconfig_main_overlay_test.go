package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// Main-work-tree config.worktree overlay tests ────────────────────────────
//
// With extensions.worktreeConfig enabled, git reads <git-dir>/config.worktree
// for the MAIN work tree exactly as it does for linked worktrees (verified
// empirically). The scan skipping it left main-checkout filter/driver
// configurations invisible to both the hardened spawn's neutralization and
// the trust snapshot, so a hostile overlay could arm command-bearing config
// in a repository that reads as clean.

// TestScanGitConfigMainWorktreeOverlayScanned plants an enabled overlay in
// the main work tree's .git and requires the scan to report it: the filter
// finding must be visible and the overlay retained (WorktreeConfigPath), so
// the hardened spawn derives the neutralizing pins and the trust snapshot
// covers the overlay's bytes.
func TestScanGitConfigMainWorktreeOverlayScanned(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(gittest.TempDir(t), "repo"), "hello\n")
	repo.AppendConfig(t, "[extensions]\n\tworktreeConfig = true\n")
	overlay := repo.GitDirFile("config.worktree")
	if err := os.WriteFile(overlay, []byte("[filter \"evil\"]\n\tclean = /tmp/evil-clean.sh\n\tprocess = /tmp/evil-process.sh\n"), 0o644); err != nil {
		t.Fatalf("writing main-worktree overlay: %v", err)
	}

	info, err := ScanGitConfig(repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if info.WorktreeConfigPath != overlay {
		t.Fatalf("WorktreeConfigPath = %q, want the main work tree overlay %q", info.WorktreeConfigPath, overlay)
	}
	f := finding(t, info, "filter.evil.clean")
	if f.Value != "/tmp/evil-clean.sh" {
		t.Errorf("filter.evil.clean = %q, want /tmp/evil-clean.sh", f.Value)
	}
	wantOverride(t, info,
		"filter.evil.clean=cat",
		"filter.evil.process=",
		"filter.evil.smudge=cat")
	if info.Clean() {
		t.Error("a main work tree with a hostile enabled overlay must not be Clean")
	}
}

// TestScanGitConfigMainWorktreeOverlayInertWhenDisabled pins the other side:
// a planted-but-DISABLED overlay (extensions.worktreeConfig unset) is inert —
// git never reads it — so the scan must not report it.
func TestScanGitConfigMainWorktreeOverlayInertWhenDisabled(t *testing.T) {
	repo := gittest.InitRepo(t, filepath.Join(gittest.TempDir(t), "repo"), "hello\n")
	if err := os.WriteFile(repo.GitDirFile("config.worktree"), []byte("[filter \"evil\"]\n\tclean = /tmp/evil-clean.sh\n"), 0o644); err != nil {
		t.Fatalf("writing overlay: %v", err)
	}

	info, err := ScanGitConfig(repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if info.WorktreeConfigPath != "" {
		t.Errorf("WorktreeConfigPath = %q, want empty: a disabled overlay is inert", info.WorktreeConfigPath)
	}
	for i := range info.Findings {
		if info.Findings[i].Kind == GitConfigFindingFilter {
			t.Fatalf("findings = %+v, want none from the disabled overlay", info.Findings)
		}
	}
}

// TestCanaryMainWorktreeOverlayFilterNeutered arms a clean-filter canary in
// the MAIN work tree's own config.worktree overlay (extensions.worktreeConfig
// on) and stages a change through GitCmdInRepo on an untrusted repository:
// the scan must see the overlay — the pre-fix hole — and derive the per-name
// pins, so the canary never executes while the add still succeeds.
func TestCanaryMainWorktreeOverlayFilterNeutered(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	canary := gittest.NewCanary(t)
	script := canary.Plant(t, "overlayfilter", gittest.FilterBody)
	canary.RequireArmed(t)

	repo := gittest.InitRepo(t, filepath.Join(gittest.TempDir(t), "repo"), "hello\n")
	repo.AppendConfig(t, "[extensions]\n\tworktreeConfig = true\n")
	overlay := "[filter \"evil\"]\n" +
		"\tprocess = " + script + "\n" +
		"\tclean = " + script + "\n" +
		"\tsmudge = " + script + "\n"
	if err := os.WriteFile(repo.GitDirFile("config.worktree"), []byte(overlay), 0o644); err != nil {
		t.Fatalf("writing main-worktree overlay: %v", err)
	}
	repo.Write(t, ".gitattributes", "*.txt filter=evil\n")
	repo.Write(t, "file.txt", "hello\nchanged\n")

	cmd, err := GitCmdInRepo(context.Background(), repo.Root, "add", ".")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add through the hardened path failed: %v (output: %s)", err, out.String())
	}
	if canary.Fired(t) {
		t.Fatal("the main-worktree overlay's clean filter must be neutralized on every c0wrk git invocation")
	}
}
