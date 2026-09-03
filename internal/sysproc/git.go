package sysproc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
)

var (
	// gitSafeHooksOnce guards the one-time resolution and creation of the
	// safe hooks directory used by gitSafetyOverrides.
	gitSafeHooksOnce sync.Once

	// gitSafeHooksDir caches the resolved absolute hooksPath.
	gitSafeHooksDir string
)

// resolveGitSafeHooksDir returns the absolute path of the empty directory
// handed to git via "-c core.hooksPath". The directory lives under the c0wrk
// default agent directory (~/.c0wrk/git/safe-hooks) and is created on first
// use. Creation is best-effort: git treats a nonexistent hooksPath exactly
// like an empty one — hooks are silently skipped, with no fallback to the
// repository's own .git/hooks — so a creation failure never downgrades to
// repo-controlled hooks.
func resolveGitSafeHooksDir() string {
	gitSafeHooksOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		gitSafeHooksDir = filepath.Join(home, DefaultAgentDirName, GitSafeHooksSegment)
		_ = os.MkdirAll(gitSafeHooksDir, 0o700)
	})
	return gitSafeHooksDir
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
func gitSafetyOverrides() []string {
	return []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + resolveGitSafeHooksDir(),
		"-c", "commit.gpgsign=false",
	}
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
// environment with any inherited GIT_EDITOR stripped, then GIT_EDITOR=true
// appended exactly once. The strip is not cosmetic — with duplicate entries
// glibc's getenv (Linux) resolves to the FIRST occurrence, so an inherited
// GIT_EDITOR would win over the appended pin and re-open the editor vector.
func hardenedGitEnv() []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		if strings.HasPrefix(kv, gitEditorEnvVar+"=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, gitEditorEnv)
}

// GitCmd creates a hardened git [exec.Cmd].
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
func GitCmd(ctx context.Context, args ...string) *exec.Cmd {
	overrides := gitSafetyOverrides()
	full := make([]string, 0, len(args)+len(overrides)+1)
	full = append(full, gitBinary)
	full = append(full, overrides...)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Env = hardenedGitEnv()
	HideConsole(cmd)
	return cmd
}
