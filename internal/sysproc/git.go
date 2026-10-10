package sysproc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/v0lka/sp4rk/safeio"
)

const (
	// gitBinary is the executable GitCmd resolves and runs.
	gitBinary = "git"

	// DefaultAgentDirName mirrors backend/config.DefaultAgentDir (".c0wrk").
	// GitCmd places its safe hooks directory under the default agent
	// directory without importing backend (internal/ must not import
	// backend/), so the value is duplicated here and pinned equal to the
	// canonical constant by backend/config tests.
	DefaultAgentDirName = ".c0wrk"

	// GitSafeHooksSegment mirrors core.GitSafeHooksRelativePath (the
	// canonical home of the constant). The value is duplicated here because
	// importing core would create an import cycle (core imports
	// core/markitdown, which imports this package); core tests pin the two
	// equal.
	GitSafeHooksSegment = "git/safe-hooks"

	// gitEditorEnv pins git's editor to `true` (a no-op) so no configured
	// core.editor or editor environment variable can ever be launched. On
	// operations that open an editor, git fails closed ("Aborting commit due
	// to empty commit message") instead of spawning it; callers that need an
	// editor-free commit pass -m.
	gitEditorEnv = "GIT_EDITOR=true"

	// gitEditorEnvVar is the name half of gitEditorEnv. The inherited
	// environment is filtered for it (see hardenedGitEnv): on glibc/Linux
	// getenv resolves duplicate names to the FIRST entry, so appending
	// GIT_EDITOR=true on top of an inherited GIT_EDITOR would leave the
	// inherited value effective and silently void the pin.
	gitEditorEnvVar = "GIT_EDITOR"

	// gitAttrEnvPrefix is the prefix of git's attribute-environment
	// variables. GIT_ATTR_SOURCE (documented) redirects where git reads
	// attributes from — the same knob as attr.tree, which an inherited value
	// could reroute away from the neutralizing empty tree; GIT_ATTR_SYSTEM
	// and GIT_ATTR_GLOBAL name additional attributes files. None of them are
	// attacker-controllable in c0wrk's launch model, but a poisoned parent
	// environment (or a future git adding another GIT_ATTR_* knob) must not
	// be able to resurrect attribute-routed command execution, so the whole
	// prefix is stripped from every spawned git process.
	gitAttrEnvPrefix = "GIT_ATTR_"

	// gitLocaleEnv pins the C locale on every hardened git process so its
	// output is deterministic. git localizes stderr ("Ваши локальные
	// изменения … будут перезаписаны" vs "Your local changes … would be
	// overwritten") and date formats (%ad weekday/month names) after the
	// user's environment; the backend matches that English text to select
	// friendly errors (isLocalChangesOverwritten, isBranchAlreadyExists in
	// backend/frontend_api_git.go) and the frontend parses %ad dates with
	// new Date(), which cannot read non-English weekday/month names. A
	// non-English user locale silently broke both.
	gitLocaleEnv = "LC_ALL=C"

	// gitLocaleEnvPrefixes are the prefixes of inherited locale variables
	// stripped before the pin is appended: LANG, LANGUAGE, and the whole
	// LC_* family (which includes LC_ALL itself). Same duplicate-entry
	// reasoning as GIT_EDITOR — glibc resolves duplicate names to the FIRST
	// entry, so stripping before appending is what makes the pin effective.
)

var gitLocaleEnvPrefixes = []string{"LC_", "LANG=", "LANGUAGE="}

var (
	// gitSafeHooksOnce guards the one-time resolution and creation of the
	// safe hooks directory used by gitSafetyOverrides.
	gitSafeHooksOnce sync.Once

	// gitSafeHooksDir caches the resolved absolute hooksPath.
	gitSafeHooksDir string

	// gitSafeHooksErr caches the resolution failure (unresolvable home
	// dir): the spawn must keep refusing, not fall back.
	gitSafeHooksErr error
)

// resolveGitSafeHooksDir returns the absolute path of the empty directory
// handed to git via "-c core.hooksPath". The directory lives under the c0wrk
// default agent directory (~/.c0wrk/git/safe-hooks) and is created on first
// use. Two error classes, deliberately split: a SYMLINK at the fixed path
// fails closed (review fix) — git would follow a symlinked core.hooksPath
// and execute whatever hooks the target holds. Every OTHER creation failure
// (a non-directory component such as ~/.c0wrk existing as a regular file,
// EACCES on a read-only home, ENOSPC) is best-effort: git treats a
// nonexistent — or not-a-directory — hooksPath exactly like an empty one —
// hooks are silently skipped, with no fallback to the repository's own
// .git/hooks — so the resolved directory is still returned (with a Warn) and
// git keeps working without downgrading to repo-controlled hooks. Home
// resolution is NOT
// best-effort (review [42]): when os.UserHomeDir fails there is no absolute
// safe location, and the old "." fallback would have handed git a RELATIVE
// hooksPath that resolves inside the repository — letting a planted
// .c0wrk/git/safe-hooks/pre-commit
// become the "safe" hook. The error fails closed through GitCmd: no git
// process is spawned at all.
func resolveGitSafeHooksDir() (string, error) {
	gitSafeHooksOnce.Do(func() {
		gitSafeHooksDir, gitSafeHooksErr = resolveGitSafeHooksDirUncached()
	})
	return gitSafeHooksDir, gitSafeHooksErr
}

func resolveGitSafeHooksDirUncached() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve home directory for the safe git hooks path (fail closed: a relative core.hooksPath would resolve inside the repository): %w", err)
	}
	dir := filepath.Join(home, DefaultAgentDirName, GitSafeHooksSegment)
	// Build the directory out of REAL components, and split the failure
	// classes (see the function contract above): ONLY a symlink AT the fixed
	// safe-hooks component fails closed — a dangling or swapped-in one via
	// MkdirAllReal's creation checks, a PRE-EXISTING one via the strict
	// re-check below, which resolution would otherwise honor; a symlinked
	// core.hooksPath is the planted-hook attack,
	// and GitCmd spawns no git process at all for it (same as an
	// unresolvable home dir). Every other failure — a non-directory component
	// (~/.c0wrk or ~/.c0wrk/git existing as a regular file), EACCES, ENOSPC —
	// degrades to the historical best-effort behavior: return the resolved
	// directory, because git runs NO hooks for a nonexistent OR
	// not-a-directory hooksPath (there is no fallback to the repository's own
	// hooks), which is exactly the neutralization this directory exists for;
	// the Warn keeps the broken environment observable instead of silently
	// unhooked.
	if err := safeio.MkdirAllReal(dir, 0o700); err != nil {
		if errors.Is(err, safeio.ErrSymlink) {
			return "", fmt.Errorf("safe git hooks directory refused (fail closed: a symlinked core.hooksPath would run planted hooks): %w", err)
		}
		return bestEffortSafeHooksDir(dir, err)
	}
	if err := safeio.CheckRealDirsBelow(filepath.Dir(dir), dir); err != nil {
		if errors.Is(err, safeio.ErrSymlink) {
			return "", fmt.Errorf("safe git hooks directory refused (fail closed: a symlinked core.hooksPath would run planted hooks): %w", err)
		}
		return bestEffortSafeHooksDir(dir, err)
	}
	return dir, nil
}

// bestEffortSafeHooksDir logs the non-security creation failure and returns
// the resolved safe-hooks path anyway: a missing hooksPath disables hook
// execution entirely (git skips hooks; no repo-hook fallback), which is the
// neutralization this directory exists for.
func bestEffortSafeHooksDir(dir string, cause error) (string, error) {
	slog.Warn("safe git hooks directory could not be created; git will run with hooks silently skipped (no repository-hook fallback)",
		"dir", dir, "error", cause)
	return dir, nil
}

// gitSafetyOverrides returns the argv prefix GitCmd prepends to every git
// invocation. Each "-c key=value" is applied on the command line and
// therefore takes precedence over the repository's own .git/config:
//
//   - core.fsmonitor=false: never starts an attacker-supplied fsmonitor
//     daemon, which git spawns on routine index-refresh operations such as
//     status and diff.
//   - core.hooksPath=<safe empty dir>: no repository hook can run. Hooks are
//     the classic code-execution vector on commit, merge, rebase, and many
//     other operations.
//   - commit.gpgsign=false: never invokes a repository-configured signing
//     binary during commit.
//
// It fails closed when the safe hooks dir cannot be resolved (unresolvable
// home dir, review [42]) — callers must refuse to spawn git.
func gitSafetyOverrides() ([]string, error) {
	hooksDir, err := resolveGitSafeHooksDir()
	if err != nil {
		return nil, err
	}
	return []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + hooksDir,
		"-c", "commit.gpgsign=false",
	}, nil
}

// UnhardenedGitArgv builds the plain argv git would receive without the
// safety overrides GitCmd prepends. It is a narrow escape hatch for tests,
// which use it to pin the shape of hardened argv — e.g. that a wrapper
// layer's arguments survive unmodified as the tail of [exec.Cmd.Args]. It
// must never be used to execute git.
func UnhardenedGitArgv(args ...string) []string {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, gitBinary)
	return append(argv, args...)
}

// hardenedGitEnv returns the environment for a git child process: the parent
// environment with any inherited GIT_EDITOR, GIT_ATTR_*, and locale variables
// stripped, then GIT_EDITOR=true and LC_ALL=C appended exactly once each.
// The GIT_EDITOR strip is not cosmetic — with duplicate entries glibc's
// getenv (Linux) resolves to the FIRST occurrence, so an inherited
// GIT_EDITOR would win over the appended pin and re-open the editor vector.
// The GIT_ATTR_* strip closes the attribute-routing environment family (see
// gitAttrEnvPrefix). The locale pins (see gitLocaleEnv) make git's stderr
// and date output deterministic English regardless of the user's desktop
// locale, which the backend's friendly-error matchers and the frontend's
// date parsing both rely on.
func hardenedGitEnv() []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+2)
	for _, kv := range parent {
		if strings.HasPrefix(kv, gitEditorEnvVar+"=") || strings.HasPrefix(kv, gitAttrEnvPrefix) {
			continue
		}
		stripped := false
		for _, p := range gitLocaleEnvPrefixes {
			if strings.HasPrefix(kv, p) {
				stripped = true
				break
			}
		}
		if stripped {
			continue
		}
		env = append(env, kv)
	}
	return append(env, gitEditorEnv, gitLocaleEnv)
}

// GitCmd creates a hardened git [exec.Cmd], or refuses to spawn git at all
// (nil cmd + error) when the safe hooks directory cannot be resolved: an
// unresolvable home dir has no safe absolute core.hooksPath, and git must
// never run without the baseline (review [42]).
//
// Every invocation carries safety overrides that neutralize
// repository-controlled code execution regardless of the repo's .git/config
// contents: a workspace can arrive as plain files with .git intact (archive,
// shared drive, USB — git clone does not transfer config), and such repos can
// carry attacker-chosen command-bearing keys that git executes on routine
// operations, before any trust decision. gitSafetyOverrides pins fsmonitor,
// hooks, and commit signing to safe values, and GIT_EDITOR=true is appended
// to the environment so no configured editor can be spawned. The -c
// command-line form wins over .git/config without touching the repository.
//
// GitCmd also hides the console window on Windows (CREATE_NO_WINDOW):
// c0wrk-desktop is a GUI-subsystem app and every git invocation would
// otherwise flash a console window on screen.
//
// Use this helper for every git invocation so neither protection can be
// forgotten. For non-git child processes apply [HideConsole] directly.
func GitCmd(ctx context.Context, args ...string) (*exec.Cmd, error) {
	overrides, err := gitSafetyOverrides()
	if err != nil {
		return nil, err
	}
	full := make([]string, 0, len(args)+len(overrides)+1)
	full = append(full, gitBinary)
	full = append(full, overrides...)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Env = hardenedGitEnv()
	HideConsole(cmd)
	return cmd, nil
}

// GitCmdRaw builds a plain git [exec.Cmd] with no safety overrides and no
// environment hardening: the repository's own .git/config and the inherited
// environment apply exactly as if git were run from a terminal. It is the
// escape hatch for repositories the user has explicitly trusted
// (security.trusted_git_repos, consulted through core/gittrust), where the
// trust decision deliberately opts the repository back into its own hooks,
// filters, and signing configuration — the opposite of [GitCmd]'s
// neutralize-everything baseline.
//
// GitCmdRaw still hides the console window on Windows (CREATE_NO_WINDOW):
// c0wrk-desktop is a GUI-subsystem app, and a trusted repository deserves the
// same no-flash behavior as the hardened path. It does not set cmd.Env, so
// the child inherits the parent environment untouched — no GIT_EDITOR pin and
// no GIT_ATTR_* strip.
//
// Use this ONLY through the trust check in core/workspace — never to bypass
// the neutralization for an untrusted repository.
func GitCmdRaw(ctx context.Context, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+1)
	full = append(full, gitBinary)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	HideConsole(cmd)
	return cmd
}
