package backend

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/core/workspace"
)

// defaultGitCommitTimeout is the fallback commit-spawn budget when the
// config is unloaded or zero; mirrors timeouts.gitCommitTimeout's default.
const defaultGitCommitTimeout = 300 * time.Second

// CommitSuppression describes what a commit in an UNTRUSTED repository would
// have executed — detected exec-free by workspace.DetectCommitSuppression.
// It is the payload the frontend surfaces when a commit was withheld so the
// user can force the hardened commit or trust the repository instead.
// See specs/decisions/059-commit-hooks-signing-gate.md.
type CommitSuppression struct {
	// Hooks lists the installed commit-family hooks that would fire, or the
	// single workspace.CommitSuppressionHooksPathMarker when core.hooksPath
	// redirects hooks elsewhere; empty when none are armed.
	Hooks []string `json:"hooks,omitempty"`
	// SigningRepo: an armed commit.gpgsign in the repository config.
	SigningRepo bool `json:"signing_repo,omitempty"`
	// SigningGlobal: an armed commit.gpgsign in the config git reads outside
	// the repository (~/.gitconfig, XDG, $GIT_CONFIG_GLOBAL/_SYSTEM).
	SigningGlobal bool `json:"signing_global,omitempty"`
	// RepoRoot is the trust root the gate keys on (the main work-tree root,
	// via workspace.ResolveGitTrustRoot); the UI names exactly what trusting
	// records. Empty only in the degenerate no-repository fallback.
	RepoRoot string `json:"repo_root,omitempty"`
}

// CommitResult is the payload of the Commit RPC: the new commit's SHA plus
// the bounded combined hook/signing output — or, exactly when the commit was
// withheld in an untrusted repository, a non-nil Suppressed with empty
// Sha/Output and no commit created.
// See specs/decisions/059-commit-hooks-signing-gate.md.
type CommitResult struct {
	// Sha is the 40-character SHA of the newly created commit.
	Sha string `json:"sha,omitempty"`
	// Output is the bounded combined stdout+stderr of the git commit spawn.
	Output string `json:"output,omitempty"`
	// Suppressed is non-nil when armed hooks/signing withheld the commit.
	Suppressed *CommitSuppression `json:"suppressed,omitempty"`
}

// gitCommitTimeout returns the configured commit-spawn budget (the long
// budget hooks and GPG signing may need) with the 300s default fallback.
func (f *FrontendAPI) gitCommitTimeout() time.Duration {
	f.configMu.RLock()
	cfg := f.config
	f.configMu.RUnlock()
	if cfg != nil && cfg.Timeouts.GitCommitTimeout > 0 {
		return time.Duration(cfg.Timeouts.GitCommitTimeout) * time.Second
	}
	return defaultGitCommitTimeout
}

// Commit creates a git commit with the given message at the active project's
// repository root. The message is passed to git as a separate argv element.
// Untrusted repositories commit through the hardened baseline; an armed
// hook/signing surface withholds the commit (Suppressed, nil error) unless
// force is set, while trusted repositories run raw git — their own hooks and
// signing execute. The gate keys on the trust ROOT (the main work-tree root,
// via workspace.ResolveGitTrustRoot), so a subdirectory or linked-worktree
// workspace commits as trusted against an already-stored root.
// See specs/decisions/059-commit-hooks-signing-gate.md.
func (f *FrontendAPI) Commit(message string, force bool) (CommitResult, error) {
	if !commitMsgRe.MatchString(message) {
		return CommitResult{}, errors.New("commit message must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return CommitResult{}, err
	}

	// The trust store keys on the MAIN work-tree root; no root discovered
	// means no repository, so keep the raw path and both checks fail closed.
	trustPath := workspace.ResolveGitTrustRoot(repoPath)
	if trustPath == "" {
		trustPath = repoPath
	}

	// The trust check probes every key form of repoPath (trust root, legacy
	// work-tree root, raw path) so the decision agrees across workspaces.
	if f.gitRepoTrusted(repoPath) {
		return f.runCommitSpawn(repoPath, message)
	}

	// A detection error fails the commit closed — nothing runs when the
	// commit surface cannot be inspected.
	hooks, signingRepo, signingGlobal, err := workspace.DetectCommitSuppression(repoPath)
	if err != nil {
		return CommitResult{}, fmt.Errorf("scan commit surface: %w", err)
	}
	if len(hooks) > 0 || signingRepo || signingGlobal {
		if !force {
			return CommitResult{
				Suppressed: &CommitSuppression{
					Hooks:         hooks,
					SigningRepo:   signingRepo,
					SigningGlobal: signingGlobal,
					RepoRoot:      trustPath,
				},
			}, nil
		}
	}

	// Nothing armed (or an explicit force): the plain hardened commit.
	return f.runCommitSpawn(repoPath, message)
}

// commitWaitDelay bounds how long the commit spawn may keep its output pipes
// open after the process exits: a hook's orphaned children inherit the pipes,
// and without this bound cmd.Run would stall until they exit.
const commitWaitDelay = time.Second

// runCommitSpawn spawns the hardened-or-raw git commit (GitCmdInRepo's trust
// decision) under the configured budget, capturing both output streams into
// a bounded combined Output.
func (f *FrontendAPI) runCommitSpawn(repoPath, message string) (CommitResult, error) {
	ctx, cancel := context.WithTimeout(f.ctx(), f.gitCommitTimeout())
	defer cancel()

	cmd, err := workspace.GitCmdInRepo(ctx, repoPath, "commit", "-m", message)
	if err != nil {
		return CommitResult{}, fmt.Errorf("git commit: %w", err)
	}
	cmd.Dir = repoPath
	cmd.WaitDelay = commitWaitDelay

	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	// exec.ErrWaitDelay surfaces only when the process exited SUCCESSFULLY
	// while an orphaned grandchild (a trusted repo's hook daemon) still held
	// the pipes past WaitDelay. The commit has landed; treating it as an
	// error would report success as failure, skip the git:status_changed
	// emission and make any retry fail with "nothing to commit". Only the
	// orphan's output tail past the delay is lost.
	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return CommitResult{}, fmt.Errorf("git commit: %w: %s", err, out.String())
	}

	sha, err := f.runGitCmd(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return CommitResult{}, fmt.Errorf("resolve new commit SHA: %w", err)
	}
	sha = strings.TrimSpace(sha)

	f.emitGitStatusChanged(repoPath)
	return CommitResult{Sha: sha, Output: out.String()}, nil
}

// limitedBuffer accumulates written bytes up to gitOutputLimit (the shared
// git-output cap from frontend_api_git.go) and appends
// gitOutputTruncationMarker once the cap is crossed. Concurrency-safe: os/exec
// copies each of an exec.Cmd's output pipes on its own goroutine.
type limitedBuffer struct {
	mu        sync.Mutex
	b         strings.Builder
	written   int
	truncated bool
}

// Write implements io.Writer; over-cap writes count their bytes but drop them.
func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.truncated {
		l.written += len(p)
		return len(p), nil
	}
	room := gitOutputLimit - l.written
	if len(p) > room {
		l.b.WriteString(string(p[:max(room, 0)]))
		l.written += len(p)
		l.truncated = true
		return len(p), nil
	}
	l.b.WriteString(string(p))
	l.written += len(p)
	return len(p), nil
}

// String returns the captured output, whitespace-trimmed like
// combinedGitOutput, with the truncation marker appended when the cap was
// crossed.
func (l *limitedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := strings.TrimSpace(l.b.String())
	if l.truncated {
		out += gitOutputTruncationMarker
	}
	return out
}
