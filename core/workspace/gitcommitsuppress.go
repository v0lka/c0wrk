package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// Exec-free detector for the commit-suppression and commit-alteration vectors a
// repository (or the user's global git config) can install around `git commit`:
// commit-family hooks and armed commit signing. Detection grade, not mitigation
// — the baseline already neutralizes both on every invocation. See
// specs/domains/workspace.md (Commit gate) and
// specs/decisions/059-commit-hooks-signing-gate.md (include blind spot,
// withhold-and-decide consumer).

// CommitSuppressionHooksPathMarker is the single hooks entry reported when
// core.hooksPath is set: the redirected dir is outside the repository, so its
// contents are deliberately not listed — the marker carries the fact.
const CommitSuppressionHooksPathMarker = "custom hooks dir (core.hooksPath)"

// commitHookNames is the commit family: the four hooks git runs around a commit.
// Other hooks never fire on a plain commit; *.sample excluded by exact name.
var commitHookNames = []string{
	"pre-commit",
	"prepare-commit-msg",
	"commit-msg",
	"post-commit",
}

// DetectCommitSuppression reports the commit-surface programs a repository and
// the user's global git config arm, without executing anything:
//   - hooks: installed commit-family hooks in <commonDir>/hooks (executable on
//     POSIX, present-at-all on Windows; *.sample never counts), or the single
//     CommitSuppressionHooksPathMarker when core.hooksPath redirects;
//   - signingRepo / signingGlobal: an ARMED commit.gpgsign (truthy/bare) in
//     repository or global config — sibling signing keys are inert without it
//     (still visible as GitConfigFindingSigning from ScanGitConfig).
//
// Includes are recorded but never followed (ADR-033), so include-only arming
// stays invisible — the accepted blind spot of specs/decisions/059-commit-hooks-signing-gate.md. No discoverable .git
// yields an empty result and nil error (global config not consulted); a
// hostile or broken repository propagates the scanner's fail-closed error.
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

// armedCommitGpgsign reports a truthy-gated commit.gpgsign finding — the
// one signing key that makes git execute a signing program during commit.
func armedCommitGpgsign(info *GitConfigInfo) bool {
	for i := range info.Findings {
		f := &info.Findings[i]
		if f.Kind == GitConfigFindingSigning && f.FullKey == "commit.gpgsign" {
			return true
		}
	}
	return false
}

// detectGlobalCommitSigning reports an armed commit.gpgsign in the config files
// git reads OUTSIDE the repository, mirroring git's resolution order:
// $GIT_CONFIG_GLOBAL (empty = global disabled), else ~/.gitconfig plus the XDG
// config ($XDG_CONFIG_HOME/git/config, else ~/.config/git/config), and
// $GIT_CONFIG_SYSTEM (empty = system disabled; /etc/gitconfig otherwise out of
// scope — this flag describes the USER's config). A missing file is clean; a
// malformed/unreadable user-level config is clean too — it is the user's own
// environment (git would refuse to run), not hostile input.
func detectGlobalCommitSigning() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		// No resolvable home means the default global locations cannot be
		// built; an explicit GIT_CONFIG_GLOBAL can still name a file.
		home = ""
	}

	var paths []string
	if p, ok := os.LookupEnv("GIT_CONFIG_GLOBAL"); ok {
		if p != "" {
			paths = append(paths, p)
		}
	} else if home != "" {
		paths = append(paths,
			filepath.Join(home, ".gitconfig"),
			filepath.Join(xdgGitConfigDir(home), "config"),
		)
	}
	// GIT_CONFIG_SYSTEM names a config git reads on EVERY invocation; an
	// empty value disables the system config wholesale (git's semantics).
	if p, ok := os.LookupEnv("GIT_CONFIG_SYSTEM"); ok && p != "" {
		paths = append(paths, p)
	}

	for _, p := range paths {
		if armedCommitGpgsignInFile(p) {
			return true
		}
	}
	return false
}

// xdgGitConfigDir returns git's XDG global config dir: $XDG_CONFIG_HOME/git
// when set (no ~/.config fallback then), <home>/.config/git otherwise.
func xdgGitConfigDir(home string) string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git")
	}
	return filepath.Join(home, ".config", "git")
}

// armedCommitGpgsignInFile reports an armed commit.gpgsign in the config file
// at path; a missing or unreadable file is clean (see detectGlobalCommitSigning).
func armedCommitGpgsignInFile(path string) bool {
	info, err := ScanGitConfigFile(path)
	if err != nil {
		return false
	}
	return armedCommitGpgsign(info)
}

// listExecutableCommitHooks lists the commit-family hooks installed in hooksDir —
// any execute bit on POSIX, any non-directory entry on Windows (no exec bit);
// symlinks resolve to their target, which is what git executes. Missing dir = empty.
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

// hookIsExecutable reports whether the hook at path would run: symlinks are
// followed (git executes the target; dangling = not installed); dirs never count.
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
