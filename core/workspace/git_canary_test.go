package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// GitSpawn canary integration tests ────────────────────────────────────────
//
// Each test builds a repository whose .git/config (planted AFTER repository
// setup, so setup itself stays clean) arms a command-bearing key that fires
// a canary — a script appending a line to a marker file — and then runs
// c0wrk's repo-scoped git paths (GitCmdInRepo and the workspace API on top
// of it). Two properties are asserted for every scenario:
//
//  1. the canary never executed (marker file absent), and
//  2. the command still returned a usable result (neutralization must not
//     break legitimate operation).
//
// The per-repo neutralization is applied by GitCmdInRepo, which re-scans
// .git/config on every invocation; the mid-session planting test proves no
// caching defeats it.

// canaryFixture bundles the per-test scaffolding.
type canaryFixture struct {
	t      *testing.T
	ctx    context.Context
	repo   *gittest.Repo
	canary *gittest.Canary
}

func newCanaryFixture(t *testing.T, initialContent string) *canaryFixture {
	t.Helper()
	gittest.RequirePOSIXShell(t)
	f := &canaryFixture{
		t:      t,
		ctx:    context.Background(),
		repo:   gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), initialContent),
		canary: gittest.NewCanary(t),
	}
	f.canary.RequireArmed(t)
	return f
}

// requireFinding asserts the scanner saw a finding of the given kind —
// fixture sanity: the hostile key is really armed and visible to the
// component that derives the neutralizing overrides.
func (f *canaryFixture) requireFinding(kind string) {
	f.t.Helper()
	info, err := ScanGitConfig(f.repo.Root)
	if err != nil {
		f.t.Fatalf("ScanGitConfig: %v", err)
	}
	for i := range info.Findings {
		if info.Findings[i].Kind == kind {
			return
		}
	}
	f.t.Fatalf("fixture not armed as expected: no %q finding in scanned config", kind)
}

// TestCanaryFSMonitorNeutered arms core.fsmonitor to a canary script and
// runs git status through the workspace API. The baseline
// (-c core.fsmonitor=false) plus the per-repo scan must keep the canary
// daemon from ever starting while status still reports the work tree.
func TestCanaryFSMonitorNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "fsmonitor", gittest.FSMonitorBody)
	f.repo.AppendConfig(t, "[core]\n\tfsmonitor = "+script)
	f.requireFinding(GitConfigFindingFSMonitor)

	f.repo.Write(t, "new.txt", "untracked\n")
	status, err := GitStatus(f.ctx, f.repo.Root)
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}
	entry, ok := status[filepath.Join(f.repo.Root, "new.txt")]
	if !ok || entry.WorkTreeStatus != "?" {
		t.Fatalf("status did not report untracked file useably: %+v", status)
	}
	f.canary.RequireNotFired(t)
}

// TestCanaryHooksPathNeutered points core.hooksPath at a directory holding
// a canary pre-commit hook and runs a commit through GitCmdInRepo. The
// baseline safe hooksPath must win, the hook must never run, and the commit
// must complete.
func TestCanaryHooksPathNeutered(t *testing.T) {
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
	f.requireFinding(GitConfigFindingHooksPath)

	f.repo.Write(t, "file.txt", "hello\nchanged\n")
	f.repo.Git(t, "add", ".")

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "commit", "-m", "second")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var commitStderr bytes.Buffer
	cmd.Stderr = &commitStderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit with armed hook failed: %v (stderr: %s)", err, commitStderr.String())
	}
	if got := f.repo.Git(t, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("commit did not complete useably: rev-list count = %s", got)
	}
	f.canary.RequireNotFired(t)
}

// TestCanaryFilterCleanNeuteredWithMidSessionPlanting arms a canary
// clean/smudge/process filter with a .gitattributes routing rule and runs
// c0wrk's diff paths. The filter must never execute — before planting
// (clean config), after planting, and after planting mid-session on a
// long-lived caller, proving the per-call re-scan has no cache to go stale.
func TestCanaryFilterCleanNeuteredWithMidSessionPlanting(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")

	// Round 1: pristine config — the same long-lived caller works fine.
	f.repo.Write(t, "untracked.txt", "new file\n")
	if _, err := GitStatus(f.ctx, f.repo.Root); err != nil {
		t.Fatalf("GitStatus before planting: %v", err)
	}
	f.canary.RequireNotFired(t)

	// Mid-session planting: config, attributes and work-tree change all
	// appear AFTER the repository has already been used.
	script := f.canary.Plant(t, "filter", gittest.FilterBody)
	f.repo.AppendConfig(t, "[filter \"canary\"]\n"+
		"\tprocess = "+script+"\n"+
		"\tclean = "+script+"\n"+
		"\tsmudge = "+script+"\n")
	f.repo.Write(t, ".gitattributes", "*.txt filter=canary\n")
	f.repo.Write(t, "file.txt", "hello\nworld\n")
	f.requireFinding(GitConfigFindingFilter)

	// Round 2: tracked diff (runs the armed clean filter on the modified
	// work-tree file) and the review diff (adds ls-files and --no-index
	// paths on the same armed repository).
	diff, err := GetFileDiffInRepo(f.ctx, f.repo.Root, "file.txt")
	if err != nil {
		t.Fatalf("GetFileDiffInRepo: %v", err)
	}
	if !contains(diff, "+world") {
		t.Fatalf("diff missing expected change:\n%s", diff)
	}

	review, err := BuildReviewDiff(f.ctx, f.repo.Root, 3)
	if err != nil {
		t.Fatalf("BuildReviewDiff: %v", err)
	}
	if !contains(review, "+world") || !contains(review, "untracked.txt") {
		t.Fatalf("review diff missing expected content:\n%s", review)
	}
	f.canary.RequireNotFired(t)
}

// TestCanaryMergeDriverNeutered arms a canary merge driver with an
// attribute routing rule and merges two genuinely conflicting branches
// through GitCmdInRepo. The driver must never execute; the merge must still
// surface a usable conflict (UU status via the workspace API).
func TestCanaryMergeDriverNeutered(t *testing.T) {
	f := newCanaryFixture(t, "base\n")
	f.repo.Git(t, "checkout", "-b", "other")
	f.repo.Write(t, "conflict.txt", "other side\n")
	f.repo.Git(t, "add", ".")
	f.repo.Git(t, "commit", "-m", "other side")
	f.repo.Git(t, "checkout", "main")
	f.repo.Write(t, "conflict.txt", "our side\n")
	f.repo.Git(t, "add", ".")
	f.repo.Git(t, "commit", "-m", "our side")

	script := f.canary.Plant(t, "mergedriver", gittest.MergeDriverBody)
	f.repo.AppendConfig(t, "[merge \"canary\"]\n\tdriver = "+script+" %O %A %B")
	f.repo.Write(t, ".gitattributes", "*.txt merge=canary\n")
	f.requireFinding(GitConfigFindingMergeDriver)

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "merge", "other")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var mergeOut bytes.Buffer
	cmd.Stdout = &mergeOut
	cmd.Stderr = &mergeOut
	mergeErr := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(mergeErr, &exitErr) {
		t.Fatalf("expected conflicting merge to exit non-zero, got %v (output: %s)", mergeErr, mergeOut.String())
	}
	t.Logf("merge output: %s", mergeOut.String())

	status, err := GitStatus(f.ctx, f.repo.Root)
	if err != nil {
		t.Fatalf("GitStatus after merge: %v", err)
	}
	// conflict.txt is added on both branches, so git reports the unmerged
	// state as AA (both added); a both-modified conflict would be UU.
	entry, ok := status[filepath.Join(f.repo.Root, "conflict.txt")]
	unmerged := entry.WorkTreeStatus == "U" ||
		(entry.IndexStatus == "A" && entry.WorkTreeStatus == "A")
	if !ok || !unmerged {
		t.Fatalf("merge conflict not surfaced useably: %+v", status)
	}
	f.canary.RequireNotFired(t)
}

// TestCanaryTextconvNeutered arms a canary diff.textconv with an attribute
// routing rule and runs the review diff over a modified tracked binary-ish
// file. The textconv converter must never execute and the diff must show
// the real content change.
func TestCanaryTextconvNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	f.repo.Write(t, "blob.bin", "old-bytes\n")
	f.repo.Git(t, "add", ".")
	f.repo.Git(t, "commit", "-m", "add blob")

	script := f.canary.Plant(t, "textconv", gittest.TextconvBody)
	f.repo.AppendConfig(t, "[diff \"canary\"]\n\ttextconv = "+script)
	f.repo.Write(t, ".gitattributes", "*.bin diff=canary\n")
	f.repo.Write(t, "blob.bin", "new-bytes\n")
	f.requireFinding(GitConfigFindingTextConv)

	review, err := BuildReviewDiff(f.ctx, f.repo.Root, 3)
	if err != nil {
		t.Fatalf("BuildReviewDiff: %v", err)
	}
	if !contains(review, "+new-bytes") {
		t.Fatalf("diff missing real content change:\n%s", review)
	}
	f.canary.RequireNotFired(t)
}

// TestCanaryLegitFilterStillCompletes arms a LEGITIMATE LFS-like passthrough
// filter and verifies c0wrk's commands still complete with usable output.
// c0wrk treats every config-declared filter as untrusted and disarms it, so
// the passthrough's own log must also stay empty while the raw-content diff
// remains correct.
func TestCanaryLegitFilterStillCompletes(t *testing.T) {
	f := newCanaryFixture(t, "data v1\n")
	filterPath, filterLog := f.canary.PlantPassthrough(t, "lfs-clean")
	f.repo.AppendConfig(t, "[filter \"lfs\"]\n"+
		"\tclean = "+filterPath+"\n"+
		"\tsmudge = "+filterPath+"\n")
	f.repo.Write(t, ".gitattributes", "*.txt filter=lfs\n")
	f.repo.Write(t, "file.txt", "data v2\n")

	diff, err := GetFileDiffInRepo(f.ctx, f.repo.Root, "file.txt")
	if err != nil {
		t.Fatalf("GetFileDiffInRepo with legit filter armed: %v", err)
	}
	if !contains(diff, "+data v2") {
		t.Fatalf("diff missing raw content change:\n%s", diff)
	}
	if _, err := os.Stat(filterLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config-declared legit filter was executed (log: %v)", err)
	}
	f.canary.RequireNotFired(t)
}

// TestCanarySubdirWorkspaceNeutralizesParentRepoConfig proves discovery
// parity: when the workspace root is a subdirectory of an armed repository
// (no .git of its own), git -C <subdir> still discovers the parent's config
// and would execute its filters — so the scanner must walk up the chain and
// neutralize the parent config. Without the walk-up the scan of a .git-less
// root silently yields no overrides and the canary fires.
func TestCanarySubdirWorkspaceNeutralizesParentRepoConfig(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	sub := filepath.Join("nested", "deep")
	f.repo.Write(t, filepath.Join(sub, "file.txt"), "v1\n")
	f.repo.Git(t, "add", ".")
	f.repo.Git(t, "commit", "-m", "nested file")

	script := f.canary.Plant(t, "filter", gittest.FilterBody)
	f.repo.AppendConfig(t, "[filter \"canary\"]\n"+
		"\tprocess = "+script+"\n"+
		"\tclean = "+script+"\n"+
		"\tsmudge = "+script+"\n")
	f.repo.Write(t, ".gitattributes", "*.txt filter=canary\n")
	f.repo.Write(t, filepath.Join(sub, "file.txt"), "v1\nchanged\n")

	// The scan of the subdirectory workspace must see the parent's findings
	// (discovery parity with git itself).
	info, err := ScanGitConfig(filepath.Join(f.repo.Root, sub))
	if err != nil {
		t.Fatalf("ScanGitConfig(subdir): %v", err)
	}
	if len(info.Findings) == 0 {
		t.Fatal("ScanGitConfig on a .git-less subdirectory saw no findings from the parent repository")
	}

	diff, err := GetFileDiffInRepo(f.ctx, filepath.Join(f.repo.Root, sub), "file.txt")
	if err != nil {
		t.Fatalf("GetFileDiffInRepo on subdir workspace: %v", err)
	}
	if !contains(diff, "+changed") {
		t.Fatalf("diff missing expected change:\n%s", diff)
	}
	f.canary.RequireNotFired(t)
}

// TestGitCmdInRepoFailClosedOnUnscannableConfig replaces .git/config with a
// directory so the scanner cannot read it. GitCmdInRepo must refuse to
// construct a command (fail closed) instead of running un-neutralized, and
// GitStatus must propagate the error rather than silently degrade.
func TestGitCmdInRepoFailClosedOnUnscannableConfig(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	configPath := f.repo.GitDirFile("config")
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("removing config: %v", err)
	}
	if err := os.Mkdir(configPath, 0o755); err != nil {
		t.Fatalf("replacing config with directory: %v", err)
	}

	if _, err := GitCmdInRepo(f.ctx, f.repo.Root, "status"); err == nil {
		t.Fatal("expected GitCmdInRepo to fail closed on unscannable config")
	}
	if _, err := GitStatus(f.ctx, f.repo.Root); err == nil {
		t.Fatal("expected GitStatus to propagate the fail-closed scan error")
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// scanRequireFinding asserts ScanGitConfig(root) reports a finding of the
// given kind (fixture sanity for canary tests whose hostile keys live in
// files other than the repo root's own .git/config).
func scanRequireFinding(t *testing.T, root, kind string) {
	t.Helper()
	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig(%s): %v", root, err)
	}
	for i := range info.Findings {
		if info.Findings[i].Kind == kind {
			return
		}
	}
	t.Fatalf("fixture not armed as expected: no %q finding in scan of %s", kind, root)
}

// TestCanaryWorktreeCommonConfigNeutered covers review finding [2]: a linked
// worktree whose COMMON config (main/.git/config) arms a canary filter while
// the old scanner looked only at the nonexistent worktrees/<n>/config. The
// scan must see the common config through the worktree's commondir, derive
// the neutralizing set, and a git add inside the worktree through
// GitCmdInRepo must stage the file without ever executing the filter.
func TestCanaryWorktreeCommonConfigNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	wt := filepath.Join(t.TempDir(), "wt")
	f.repo.Git(t, "worktree", "add", wt, "-b", "wtbranch")

	script := f.canary.Plant(t, "wtfilter", gittest.FilterBody)
	f.repo.AppendConfig(t, "[filter \"canary\"]\n"+
		"\tprocess = "+script+"\n"+
		"\tclean = "+script+"\n"+
		"\tsmudge = "+script+"\n")
	// Routing from inside the linked worktree.
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".gitattributes"), []byte("*.txt filter=canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "file.txt"), []byte("hello\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanRequireFinding(t, wt, GitConfigFindingFilter)

	cmd, err := GitCmdInRepo(f.ctx, wt, "add", "file.txt")
	if err != nil {
		t.Fatalf("GitCmdInRepo(worktree): %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("add in linked worktree failed: %v (%s)", err, out.String())
	}
	f.canary.RequireNotFired(t)

	status, err := GitStatus(f.ctx, wt)
	if err != nil {
		t.Fatalf("GitStatus(worktree): %v", err)
	}
	// file.txt is tracked (inherited from the main branch HEAD): a staged
	// modification reports IndexStatus M with an empty worktree status.
	if entry, ok := status[filepath.Join(wt, "file.txt")]; !ok || entry.IndexStatus != "M" {
		t.Fatalf("file was not staged in the worktree: %+v", status)
	}
	f.canary.RequireNotFired(t)
}

// TestCanaryIncludeHiddenFilterViaInfoAttributesNeutered covers review
// finding [1]a: the filter definition lives in an included file the scanner
// cannot read, and the routing comes from .git/info/attributes — a source
// attr.tree does not cover (verified: the canary fires under the exact old
// override set). The scan must record the include, scan the routing file,
// and pin the routed name so git add executes nothing.
func TestCanaryIncludeHiddenFilterViaInfoAttributesNeutered(t *testing.T) {
	skipUnlessAttrTreeCapableGit(t)
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "hiddenfilter", gittest.FilterBody)
	extra := filepath.Join(f.repo.Root, "extra.conf")
	if err := os.WriteFile(extra, []byte("[filter \"x\"]\n\tclean = "+script+"\n\tsmudge = "+script+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.AppendConfig(t, "[include]\n\tpath = "+extra+"\n")
	infoAttrs := filepath.Join(f.repo.GitDir(), "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(infoAttrs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoAttrs, []byte("*.txt filter=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.Write(t, "file.txt", "hello\nchanged\n")
	scanRequireFinding(t, f.repo.Root, GitConfigFindingAttrRouting)

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "add", "file.txt")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("add failed: %v (%s)", err, out.String())
	}
	f.canary.RequireNotFired(t)
}

// TestCanaryAttributesFileRoutingNeutered is the core.attributesFile variant
// of [1]a: git 2.50.1 respects the key from repository config, attr.tree
// does not cover the source, and the verified closure is the empty -c
// override (which also beats a definition hidden in an included file) plus
// the name pin for everything the routing file routes.
func TestCanaryAttributesFileRoutingNeutered(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "attrfilefilter", gittest.FilterBody)
	attrsFile := filepath.Join(t.TempDir(), "attrs")
	if err := os.WriteFile(attrsFile, []byte("*.txt filter=q\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.AppendConfig(t, "[core]\n\tattributesFile = "+attrsFile+"\n[filter \"q\"]\n\tclean = "+script+"\n\tsmudge = "+script+"\n")
	f.repo.Write(t, "file.txt", "hello\nchanged\n")
	scanRequireFinding(t, f.repo.Root, GitConfigFindingAttributesFile)

	cmd, err := GitCmdInRepo(f.ctx, f.repo.Root, "add", "file.txt")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("add failed: %v (%s)", err, out.String())
	}
	f.canary.RequireNotFired(t)
}

// TestCanarySHA256RepoAttrTreeNeutered covers review finding [1]d: on a
// SHA-256 repository the SHA-1 empty tree is a verified silent no-op, so the
// attr.tree kill must use the SHA-256 empty tree. The fixture arms a visible
// filter routed from the worktree .gitattributes — exactly the routing the
// (wrong) SHA-1 constant would leave live.
func TestCanarySHA256RepoAttrTreeNeutered(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittest.RequireGit(t)
	skipUnlessAttrTreeCapableGit(t)
	root := filepath.Join(t.TempDir(), "sha256repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.CommandContext(context.Background(), "git", "-C", root, "init", "-b", "main", "--object-format=sha256").Run(); err != nil {
		t.Skipf("git lacks --object-format=sha256 support: %v", err)
	}
	repo := &gittest.Repo{Root: root}
	repo.Git(t, "config", "user.email", "c0wrk-fixture@example.invalid")
	repo.Git(t, "config", "user.name", "c0wrk fixture")
	repo.Git(t, "config", "commit.gpgsign", "false")
	repo.Write(t, "file.txt", "hello\n")
	repo.Git(t, "add", ".")
	repo.Git(t, "commit", "-m", "initial")

	f := &canaryFixture{t: t, ctx: context.Background(), repo: repo, canary: gittest.NewCanary(t)}
	f.canary.RequireArmed(t)
	script := f.canary.Plant(t, "sha256filter", gittest.FilterBody)
	// The include keeps the attr.tree kill engaged (narrow mode, review
	// [56]: a visible filter alone no longer derives it), so the SHA-256
	// empty-tree selection stays under test.
	repo.AppendConfig(t, "[filter \"x\"]\n\tclean = "+script+"\n\tsmudge = "+script+"\n[include]\n\tpath = extra.conf\n")
	repo.Write(t, ".gitattributes", "*.txt filter=x\n")
	repo.Write(t, "file.txt", "hello\nchanged\n")

	// Self-validating discriminator: the derived set must carry the SHA-256
	// empty tree (the SHA-1 constant is inert here and the canary would run).
	info, err := ScanGitConfig(root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	if !slices.Contains(overrideArgvs(info), "attr.tree="+EmptyTreeSHA256) {
		t.Fatalf("overrides = %v, want attr.tree=%s", overrideArgvs(info), EmptyTreeSHA256)
	}

	cmd, err := GitCmdInRepo(f.ctx, root, "add", "file.txt")
	if err != nil {
		t.Fatalf("GitCmdInRepo: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("add failed: %v (%s)", err, out.String())
	}
	f.canary.RequireNotFired(t)
}
