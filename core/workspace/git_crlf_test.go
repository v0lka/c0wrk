package workspace

// Review [56] (post-v0.7.3) regression tests: the attr.tree blanket kill
// disables benign attributes too, so a legitimate CRLF-normalized repository
// (`* text=auto`, index storing LF, worktree holding CRLF) reported every
// text file as falsely modified with whole-file numstat diffs. The fix
// narrows the kill to the one case per-name pins cannot cover (include
// directives hiding driver definitions): visible drivers are neutralized by
// name instead, which covers every routing source without touching benign
// attributes.
//
//   - TestCanaryCRLFRepoNarrowModeKeepsStatusClean is the acceptance
//     regression: a filter-configured CRLF repository must show a clean
//     status and an empty numstat under c0wrk's derived overrides, with a
//     non-vacuity control re-proving the collateral exists the moment the
//     blanket kill is engaged (the empirical basis of the [56]a docs).
//   - TestCanaryIncludeHiddenFilterViaAttributesFileNeutered pins the
//     narrowing's security boundary (linked with the step_5 [1]a closure):
//     the include + core.attributesFile combination still engages attr.tree
//     and still blocks an include-hidden filter routed through that source.

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// runInRepoGit runs one git invocation through GitCmdInRepo and returns its
// combined output, failing the test on a non-zero exit.
func runInRepoGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd, err := GitCmdInRepo(t.Context(), root, args...)
	if err != nil {
		t.Fatalf("GitCmdInRepo(%v): %v", args, err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, out.String())
	}
	return out.String()
}

// TestCanaryCRLFRepoNarrowModeKeepsStatusClean builds the exact repository
// class review [56] found broken: a legitimate CRLF-normalized repo whose
// local config carries a filter section (e.g. `git lfs install --local`).
// Under the old blanket attr.tree kill such a repo showed ` M crlf.txt` and
// a whole-file numstat diff on c0wrk's status/diff surfaces. In narrow mode
// the derived set pins the visible filter by name and leaves attribute
// interpretation intact, so status and numstat stay clean — while the
// control invocation with attr.tree forced back on re-manifests the
// collateral (proving the assertion can actually detect it).
func TestCanaryCRLFRepoNarrowModeKeepsStatusClean(t *testing.T) {
	f := newCanaryFixture(t, "hello\n")
	repo := f.repo

	// CRLF-normalized fixture: `* text=auto` is committed, the worktree
	// holds CRLF content while the index stores the normalized LF form.
	// core.autocrlf is pinned locally so a machine's global setting cannot
	// skew the comparison (repo-local keys beat global ones).
	repo.Git(t, "config", "core.autocrlf", "false")
	repo.Write(t, ".gitattributes", "* text=auto\n*.bin filter=x\n")
	repo.Write(t, "crlf.txt", "line one\r\nline two\r\n")
	repo.Git(t, "add", ".")
	repo.Git(t, "commit", "-m", "crlf fixture")

	// The legitimate-filter trigger that used to derive the blanket kill.
	script := f.canary.Plant(t, "crlffilter", gittest.FilterBody)
	repo.AppendConfig(t, "[filter \"x\"]\n\tclean = "+script+"\n\tsmudge = "+script+"\n")
	f.canary.RequireArmed(t)
	f.requireFinding(GitConfigFindingFilter)

	info, err := ScanGitConfig(repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	// Narrow mode: the visible filter is pinned by name and attr.tree is
	// NOT part of the derived set — that absence is the whole [56] fix.
	argvs := overrideArgvs(info)
	if slices.Contains(argvs, "attr.tree="+EmptyTreeSHA1) {
		t.Fatalf("narrow mode must not engage attr.tree for a visible filter: %v", argvs)
	}
	for _, want := range []string{"filter.x.process=", "filter.x.clean=cat", "filter.x.smudge=cat"} {
		if !slices.Contains(argvs, want) {
			t.Fatalf("derived overrides = %v, want %q among them", argvs, want)
		}
	}

	// Acceptance: no falsely-modified entries, no inflated numstat.
	if got := runInRepoGit(t, repo.Root, "status", "--porcelain"); got != "" {
		t.Errorf("false-modified status under narrow mode: %q", got)
	}
	if got := runInRepoGit(t, repo.Root, "diff", "--numstat"); got != "" {
		t.Errorf("inflated numstat under narrow mode: %q", got)
	}

	// Non-vacuity control (the [56]a collateral, empirically re-confirmed):
	// forcing the pre-[56] blanket kill back on makes the SAME repository
	// report the CRLF file as wholly modified with a whole-file numstat.
	kill := []string{"-c", "attr.tree=" + EmptyTreeSHA1}
	got := runInRepoGit(t, repo.Root, append(slices.Clone(kill), "status", "--porcelain")...)
	if !strings.Contains(got, "M crlf.txt") {
		t.Fatalf("control: attr.tree must reproduce the false-modified collateral, got status %q", got)
	}
	got = runInRepoGit(t, repo.Root, append(slices.Clone(kill), "diff", "--numstat")...)
	if !strings.Contains(got, "crlf.txt") {
		t.Fatalf("control: attr.tree must reproduce the whole-file numstat collateral, got %q", got)
	}

	// The narrowing must not weaken the filter neutralization itself: the
	// routed name stays pinned, so staging a routed file executes nothing.
	repo.Write(t, "data.bin", "payload\n")
	runInRepoGit(t, repo.Root, "add", "data.bin")
	f.canary.RequireNotFired(t)
}

// TestCanaryIncludeHiddenFilterViaAttributesFileNeutered pins the security
// boundary of the [56] narrowing (the step_5 [1]a closure stays intact): a
// filter defined in an included file — invisible to the scanner — and routed
// through core.attributesFile. Includes are exactly the case per-name pins
// cannot cover, so attr.tree must STILL be engaged there, alongside the
// core.attributesFile= kill and the routed-name pins, and a git add must
// execute nothing.
func TestCanaryIncludeHiddenFilterViaAttributesFileNeutered(t *testing.T) {
	skipUnlessAttrTreeCapableGit(t)
	f := newCanaryFixture(t, "hello\n")
	script := f.canary.Plant(t, "hiddenattrfile", gittest.FilterBody)
	// The driver definition lives in an included file the scanner
	// deliberately never reads; only the routing is visible.
	extra := filepath.Join(f.repo.Root, "extra.conf")
	if err := os.WriteFile(extra, []byte("[filter \"q\"]\n\tclean = "+script+"\n\tsmudge = "+script+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	attrsFile := filepath.Join(t.TempDir(), "attrs")
	if err := os.WriteFile(attrsFile, []byte("*.txt filter=q\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.repo.AppendConfig(t, "[include]\n\tpath = "+extra+"\n[core]\n\tattributesFile = "+attrsFile+"\n")
	f.repo.Write(t, "file.txt", "hello\nchanged\n")
	scanRequireFinding(t, f.repo.Root, GitConfigFindingAttrRouting)

	// The narrowing's guard: includes keep the blanket kill engaged.
	info, err := ScanGitConfig(f.repo.Root)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	argvs := overrideArgvs(info)
	for _, want := range []string{
		"attr.tree=" + EmptyTreeSHA1,
		"core.attributesFile=",
		"filter.q.process=",
		"filter.q.clean=cat",
		"filter.q.smudge=cat",
	} {
		if !slices.Contains(argvs, want) {
			t.Fatalf("overrides = %v, want %q among them", argvs, want)
		}
	}

	runInRepoGit(t, f.repo.Root, "add", "file.txt")
	f.canary.RequireNotFired(t)
}
