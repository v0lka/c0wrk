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
	gitBinary = "git"

	// DefaultAgentDirName duplicates backend/config.DefaultAgentDir because
	// internal/ must not import backend/; backend/config tests pin them equal.
	DefaultAgentDirName = ".c0wrk"

	// GitSafeHooksSegment duplicates core.GitSafeHooksRelativePath (importing
	// core would cycle: core/markitdown imports this package); core tests pin
	// the two equal.
	GitSafeHooksSegment = "git/safe-hooks"

	// gitEditorEnv pins the editor to `true`; commits pass -m
	// (specs/decisions/033-git-subprocess-hardening.md).
	gitEditorEnv = "GIT_EDITOR=true"

	// gitEditorEnvVar is stripped before the pin is appended: glibc getenv
	// resolves duplicates to the FIRST entry.
	gitEditorEnvVar = "GIT_EDITOR"

	// gitAttrEnvPrefix: the whole GIT_ATTR_* family is stripped so
	// GIT_ATTR_SOURCE cannot reroute attribute reads
	// (specs/decisions/033-git-subprocess-hardening.md).
	gitAttrEnvPrefix = "GIT_ATTR_"

	// gitLocaleEnv pins the C locale so git's stderr and %ad dates stay
	// deterministic English: the backend matches that exact text to select
	// friendly errors (isLocalChangesOverwritten, isBranchAlreadyExists in
	// backend/frontend_api_git.go) and the frontend parses %ad with
	// new Date(); a non-English locale silently broke both.
	// See specs/decisions/033-git-subprocess-hardening.md.
	gitLocaleEnv = "LC_ALL=C"
)

// gitLocaleEnvPrefixes: inherited locale variables (the whole LC_* family,
// LANG, LANGUAGE) are stripped before the pin is appended — the GIT_EDITOR
// first-entry rule again.
var gitLocaleEnvPrefixes = []string{"LC_", "LANG=", "LANGUAGE="}

var (
	gitSafeHooksOnce sync.Once

	gitSafeHooksDir string

	// gitSafeHooksErr: a resolution failure is cached — the spawn must keep
	// refusing, not fall back.
	gitSafeHooksErr error
)

// resolveGitSafeHooksDir returns the empty directory handed to git via
// "-c core.hooksPath" (~/.c0wrk/git/safe-hooks, created on first use;
// specs/decisions/033-git-subprocess-hardening.md). Failure classes are
// deliberate: a SYMLINK at the fixed path fails closed (git would follow it
// and run the target's hooks), and so does an unresolvable home dir
// (review [42]: the old "." fallback handed git a RELATIVE hooksPath a
// planted .c0wrk/git/safe-hooks could arm). Every other creation failure is
// best-effort — git runs no hooks for a nonexistent or not-a-directory
// hooksPath, with no fallback to the repository's own hooks.
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
	// MkdirAllReal catches a dangling symlink at creation; the strict
	// re-check below catches one swapped in afterwards — both fail closed
	// per the function contract; every other failure degrades best-effort.
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
// the path anyway: a missing hooksPath disables hook execution entirely —
// the neutralization this directory exists for.
func bestEffortSafeHooksDir(dir string, cause error) (string, error) {
	slog.Warn("safe git hooks directory could not be created; git will run with hooks silently skipped (no repository-hook fallback)",
		"dir", dir, "error", cause)
	return dir, nil
}

// gitSafetyOverrides returns the argv prefix GitCmd prepends to every git
// invocation — the ADR-033 baseline, each -c beating .git/config. Fails
// closed when the safe hooks dir cannot be resolved
// (specs/decisions/033-git-subprocess-hardening.md).
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

// UnhardenedGitArgv is the tests-only escape hatch for pinning hardened argv
// shapes; never for execution
// (specs/decisions/033-git-subprocess-hardening.md).
func UnhardenedGitArgv(args ...string) []string {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, gitBinary)
	return append(argv, args...)
}

// hardenedGitEnv returns the parent environment with the stripped families
// (see gitEditorEnvVar, gitAttrEnvPrefix, gitLocaleEnvPrefixes) removed and
// both pins appended exactly once — see
// specs/decisions/033-git-subprocess-hardening.md.
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

// GitCmd creates a hardened git [exec.Cmd] — the ADR-033 global baseline:
// safety overrides neutralize repository-controlled code execution on every
// invocation (a workspace can arrive with an armed .git/config; see
// specs/decisions/033-git-subprocess-hardening.md), and the console window
// is hidden on Windows. It refuses to spawn git at all (nil cmd + error)
// when the safe hooks directory cannot be resolved. Use it for every git
// invocation; for non-git child processes apply [HideConsole] directly.
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

// GitCmdRaw builds a plain git [exec.Cmd] — no overrides, no environment
// hardening: the trusted repository runs its own config and environment
// (specs/decisions/034-git-trust-opt-out.md). The console window is still
// hidden on Windows; cmd.Env is left unset. Use it ONLY through the trust
// check in core/workspace — never to bypass neutralization.
func GitCmdRaw(ctx context.Context, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+1)
	full = append(full, gitBinary)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	HideConsole(cmd)
	return cmd
}
