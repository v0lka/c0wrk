package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// This file implements an exec-free detector for commit-suppression and
// commit-alteration vectors a repository (or the user's global git config)
// can install around `git commit`: installed hooks from the commit family
// and commit-signing configuration. It answers the intake question "what
// will try to run — or sign — when a commit is made here?" without ever
// spawning a process, reusing the same .git discovery (including linked
// worktrees through their commondir) and the same config parser as the
// GitSpawn config scan.
//
// Both vectors are already neutralized by the c0wrk git baseline on every
// invocation (-c core.hooksPath=<safe dir> redirects hooks away, and
// -c commit.gpgsign=false disarms signing), so this is a detection-grade
// picture for the intake UI, not a mitigation. Two deliberate blind spots
// mirror the baseline. First, a configured core.hooksPath is reported as a
// single marker — the target directory is an arbitrary user-configured
// path outside the repository, and it is never listed. Second, include
// directives are parsed and recorded but never followed (the standing
// ADR-033 scanner posture; ResolveIncludes exists for trust fingerprinting
// only): a commit-surface setting defined only inside an included file —
// armed signing above all — stays invisible here, so the commit gate does
// not fire for include-driven setups. Accepted because the baseline
// neutralizes the commit either way and the intake warning already marks
// every include directive; trust remains the path for such repositories.

// CommitSuppressionHooksPathMarker is the single hooks entry reported when
// the repository config sets core.hooksPath: git would run commit hooks
// from that configured directory instead of <commonDir>/hooks, but the
// directory is outside the repository, so its contents are deliberately
// not listed — the marker alone carries the fact.
const CommitSuppressionHooksPathMarker = "custom hooks dir (core.hooksPath)"

// commitHookNames is the hook family that participates in commit creation:
// the four hooks git runs around a commit (before the editor, on the
// message, before the commit is recorded, and after). Other hooks
// (pre-push, post-merge, ...) do not fire on a plain commit and are out of
// this detector's scope; *.sample template files are excluded by exact-name
// matching.
var commitHookNames = []string{
	"pre-commit",
	"prepare-commit-msg",
	"commit-msg",
	"post-commit",
}

// DetectCommitSuppression reports the commit-surface programs a repository
// and the user's global git config arm, without executing anything:
//
//   - hooks: names of installed hooks from the commit family found in the
//     repository's hooks directory (<commonDir>/hooks — for a linked
//     worktree that is the main repository's hooks directory git shares),
//     executable on POSIX and present-at-all on Windows (which has no
//     exec bit); *.sample templates never count. When core.hooksPath
//     redirects hooks, the single CommitSuppressionHooksPathMarker is
//     returned instead and the configured directory is not listed. Sorted
//     for stable reporting; nil when nothing is armed.
//   - signingRepo: the repository (common config, plus the config.worktree
//     overlay when enabled — the config files git reads directly) has an
//     ARMED commit.gpgsign (true/yes/on/1/bare): commits would be signed,
//     i.e. git would execute the signing program (gpg.program or gpg
//     itself) during commit. A disabled gpgsign — the value c0wrk's own
//     baseline and fixtures pin — keeps the flag down, and the sibling
//     signing keys (gpg.format, gpg.program, user.signingkey) alone do not
//     arm signing: without commit.gpgsign they never execute. Each of them
//     is still visible as a GitConfigFindingSigning finding from
//     ScanGitConfig. Included config files are not consulted (see the
//     include-directive blind spot in the file header above), so signing
//     enforced only through an include does not raise this flag.
//   - signingGlobal: the same armed commit.gpgsign in the user's global
//     config (~/.gitconfig or ~/.config/git/config — git's XDG location).
//
// A path with no discoverable .git yields an empty result with a nil error
// (the same convention as ScanGitConfig); the global config is not even
// consulted then — without a repository there is no commit surface. A
// hostile or broken repository (unreadable config, failed commondir
// resolution, unparseable object format) propagates the scanner's
// fail-closed error instead of guessing.
func DetectCommitSuppression(repoPath string) (hooks []string, signingRepo, signingGlobal bool, err error) {
	info, err := ScanGitConfig(repoPath)
	if err != nil {
		return nil, false, false, err
	}
	if info.GitDir == "" {
		return nil, false, false, nil
	}

	signingRepo = armedCommitGpgsign(info)
	for i := range info.Findings {
		if info.Findings[i].Kind == GitConfigFindingHooksPath {
			// core.hooksPath redirects every hook: report the marker only,
			// never list the configured (out-of-repository) directory.
			return []string{CommitSuppressionHooksPathMarker}, signingRepo, detectGlobalCommitSigning(), nil
		}
	}

	hooks, err = listExecutableCommitHooks(filepath.Join(info.CommonDir, "hooks"))
	if err != nil {
		return nil, false, false, err
	}
	return hooks, signingRepo, detectGlobalCommitSigning(), nil
}

// armedCommitGpgsign reports whether the parsed config carries an armed
// commit.gpgsign — the truthy-gated signing finding from the config
// scanner. It is the one signing key that makes git execute a signing
// program during commit; the sibling signing keys are inert without it.
func armedCommitGpgsign(info *GitConfigInfo) bool {
	for i := range info.Findings {
		f := &info.Findings[i]
		if f.Kind == GitConfigFindingSigning && f.FullKey == "commit.gpgsign" {
			return true
		}
	}
	return false
}

// detectGlobalCommitSigning reports an armed commit.gpgsign in the user's
// global git config, checking both locations git reads (~/.gitconfig and
// the XDG ~/.config/git/config). A missing file is simply clean; a present
// but malformed global config is treated as clean rather than fatal — it is
// the user's own environment (git itself would refuse to run with it, so
// nothing can silently sign), not hostile repository input the repo scan
// must fail closed on.
func detectGlobalCommitSigning() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		// No resolvable home means no readable global config: nothing can
		// be armed there. The repo-side picture stays complete.
		return false
	}
	for _, p := range []string{
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".config", "git", "config"),
	} {
		info, err := ScanGitConfigFile(p)
		if err != nil {
			// Unreadable-but-present (e.g. oversized): same reasoning as a
			// malformed file — the user's own environment, reported clean
			// rather than failing the whole repo detection.
			continue
		}
		if armedCommitGpgsign(info) {
			return true
		}
	}
	return false
}

// listExecutableCommitHooks lists the commit-family hooks installed in
// hooksDir: regular files (symlinks resolve to their target — git executes
// the target) that carry any execute bit on POSIX, or any non-directory
// entry on Windows, where the filesystem has no exec bit and presence is
// what git runs. A missing directory is an empty result (git runs no hooks
// then); anything else that fails to read or stat fails closed.
func listExecutableCommitHooks(hooksDir string) ([]string, error) {
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read hooks dir %s: %w", hooksDir, err)
	}
	var hooks []string
	for _, e := range entries {
		name := e.Name()
		if !isCommitHookName(name) {
			// Excludes every other file, notably the *.sample templates
			// git init seeds the directory with (they are executable).
			continue
		}
		executable, err := hookIsExecutable(filepath.Join(hooksDir, name))
		if err != nil {
			return nil, fmt.Errorf("stat hook %s: %w", name, err)
		}
		if executable {
			hooks = append(hooks, name)
		}
	}
	if len(hooks) == 0 {
		return nil, nil
	}
	sort.Strings(hooks)
	return hooks, nil
}

// isCommitHookName reports whether name is one of the four commit-family
// hook names (exact match, which by construction excludes *.sample).
func isCommitHookName(name string) bool {
	for _, n := range commitHookNames {
		if name == n {
			return true
		}
	}
	return false
}

// hookIsExecutable reports whether the hook at path would run. Symlinks
// are followed (os.Stat): git executes the link's target, so a symlinked
// hook is armed exactly when its target is; a dangling one targets nothing
// and counts as not installed. Directories never count.
func hookIsExecutable(path string) (bool, error) {
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if st.IsDir() {
		return false, nil
	}
	if runtime.GOOS == "windows" {
		// Windows has no exec bit: any present hook file is runnable.
		return true, nil
	}
	return st.Mode().Perm()&0o111 != 0, nil
}
