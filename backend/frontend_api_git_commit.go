package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/core/workspace"
)

// ---------------------------------------------------------------------------
// Commit RPC — suppression-aware commit flow
// ---------------------------------------------------------------------------

// commitOutputLimit caps the combined commit stdout+stderr captured into
// CommitResult.Output. A hook-heavy or signing commit can spray megabytes
// (GPG banners, hook progress, verbose templates); the panel only ever
// shows a bounded excerpt, so anything past 64 KiB is dropped with an
// explicit marker.
const commitOutputLimit = 64 * 1024

// commitOutputTruncationMarker is appended to Output when the combined
// commit output exceeded commitOutputLimit and was cut.
const commitOutputTruncationMarker = "\n[output truncated at 64 KiB]"

// defaultGitCommitTimeout is the fallback commit-spawn budget when the
// config is not loaded or the value is zero. Mirrors the config default
// (timeouts.gitCommitTimeout, 300s).
const defaultGitCommitTimeout = 300 * time.Second

// CommitSuppression describes what a commit in an UNTRUSTED repository
// would have executed — the commit-family hooks git would run and the
// armed commit signing (repo and global config) — as detected by
// workspace.DetectCommitSuppression without spawning anything. It is the
// payload the frontend surfaces when a commit was withheld (Suppressed is
// non-nil) so the user can decide whether to force the hardened commit or
// trust the repository instead.
type CommitSuppression struct {
	// Hooks lists installed commit-family hooks (pre-commit,
	// prepare-commit-msg, commit-msg, post-commit) that would fire, or the
	// single workspace.CommitSuppressionHooksPathMarker when core.hooksPath
	// redirects hooks to a custom directory. Empty when no hooks are armed.
	Hooks []string `json:"hooks,omitempty"`
	// SigningRepo reports an armed commit.gpgsign in the repository config:
	// a commit would execute the signing program (gpg or gpg.program).
	SigningRepo bool `json:"signing_repo,omitempty"`
	// SigningGlobal reports an armed commit.gpgsign in the user's global
	// git config (~/.gitconfig or the XDG location).
	SigningGlobal bool `json:"signing_global,omitempty"`
}

// CommitResult is the payload of the Commit RPC. A normal (or forced)
// commit returns the new commit's SHA plus the bounded combined stdout and
// stderr of the git commit spawn; Output is how the UI shows hook and
// signing output for trusted repositories (a trusted repo runs its own
// hooks, so their output lands there). Suppressed is non-nil exactly when
// the commit was withheld in an untrusted repository — then Sha/Output are
// empty and no commit exists.
type CommitResult struct {
	// Sha is the 40-character SHA of the newly created commit (empty when
	// Suppressed is non-nil or the call errored).
	Sha string `json:"sha,omitempty"`
	// Output is the bounded combined stdout+stderr of the git commit spawn,
	// truncated past 64 KiB with an explicit marker (empty when Suppressed
	// is non-nil).
	Output string `json:"output,omitempty"`
	// Suppressed is non-nil when the commit was withheld because the
	// untrusted repository arms commit hooks or signing and force was not
	// requested. Nothing was executed and no commit was created.
	Suppressed *CommitSuppression `json:"suppressed,omitempty"`
}

// gitCommitTimeout returns the configured budget for the git commit spawn
// (timeouts.gitCommitTimeout, seconds — the long budget hooks, GPG signing
// and large repositories may need) with the 300s default fallback when the
// config is unset or the value is zero, mirroring serviceLLMTimeout.
func (f *FrontendAPI) gitCommitTimeout() time.Duration {
	f.configMu.RLock()
	cfg := f.config
	f.configMu.RUnlock()
	if cfg != nil && cfg.Timeouts.GitCommitTimeout > 0 {
		return time.Duration(cfg.Timeouts.GitCommitTimeout) * time.Second
	}
	return defaultGitCommitTimeout
}

// Commit creates a git commit with the given message at the active
// project's repository root. The message must be non-empty and is passed
// to git as a separate argv element (never interpolated into the command
// line).
//
// Untrusted repositories (the default) commit through the hardened git
// baseline, which neutralizes hooks and signing — so committing there
// would SILENTLY skip programs the repository installed. When
// DetectCommitSuppression finds armed commit hooks or signing in an
// untrusted repository and force is false, the commit is withheld instead:
// nothing runs, no commit is created, and the result carries a non-nil
// Suppressed description for the UI to surface. With force true the commit
// proceeds hardened exactly as before (hooks and signing still neutralized)
// — the explicit override.
//
// Trusted repositories (security.trusted_git_repos, the TrustGitRepo RPC)
// go straight to the commit: the spawn layer runs raw git for them, so
// their own hooks and signing EXECUTE — that is what trust means — and the
// commit gets its own long timeout (timeouts.gitCommitTimeout, default
// 300s; quick git probes keep the 30s budget) plus a combined stdout+stderr
// capture so hook/signing output reaches the UI.
//
// On success the new commit's SHA is resolved (git rev-parse HEAD, 30s
// probe budget) and git:status_changed is emitted. Errors are as before:
// no project / No Project / empty message / git failure (stderr surfaced
// in the error).
func (f *FrontendAPI) Commit(message string, force bool) (CommitResult, error) {
	if !commitMsgRe.MatchString(message) {
		return CommitResult{}, errors.New("commit message must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return CommitResult{}, err
	}

	// Trusted repositories commit directly: the spawn layer runs raw git
	// for them (hooks/signing execute — the canary suite in core/workspace
	// proves it), and they get the long commit budget.
	if f.gitRepoTrusted(repoPath) {
		return f.runCommitSpawn(repoPath, message)
	}

	// Untrusted: detect (exec-free) what a commit would have executed. A
	// detection error fails the commit closed — no commit is made on a
	// repository whose commit surface cannot be inspected.
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
				},
			}, nil
		}
	}

	// Nothing armed (or an explicit force): the plain hardened commit, now
	// with the long commit budget and combined output capture — an
	// untrusted repository cannot arm anything, but a forced commit on a
	// suppressed repo still deserves the same budget (its baseline
	// neutralization runs the same git commit) and the same visibility.
	return f.runCommitSpawn(repoPath, message)
}

// commitWaitDelay bounds how long the commit spawn may keep its output
// pipes open after the process itself exits or the timeout kills it: a
// hook's orphaned children inherit the pipes, and without this bound
// cmd.Run would stall until they exit — voiding GitCommitTimeout.
const commitWaitDelay = time.Second

// runCommitSpawn spawns the actual git commit: GitCmdInRepo resolves the
// trust decision (raw git for a trusted root, the hardened baseline for
// everything else), the spawn gets the configured commit timeout, and both
// output streams are captured into a bounded combined Output. The SHA is
// resolved afterwards with the standard quick-probe budget.
func (f *FrontendAPI) runCommitSpawn(repoPath, message string) (CommitResult, error) {
	ctx, cancel := context.WithTimeout(f.ctx(), f.gitCommitTimeout())
	defer cancel()

	cmd, err := workspace.GitCmdInRepo(ctx, repoPath, "commit", "-m", message)
	if err != nil {
		return CommitResult{}, fmt.Errorf("git commit: %w", err)
	}
	cmd.Dir = repoPath
	cmd.WaitDelay = commitWaitDelay

	// Capture both streams the same way runGitCmdCombined does: two
	// buffers, stdout first, so hook and signing output reaches the UI.
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return CommitResult{}, fmt.Errorf("git commit: %w: %s", err, out.String())
	}

	// Resolve the SHA of the commit just created so the frontend can
	// display it. git rev-parse HEAD yields the 40-character commit SHA.
	sha, err := f.runGitCmd(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return CommitResult{}, fmt.Errorf("resolve new commit SHA: %w", err)
	}
	sha = strings.TrimSpace(sha)

	f.emitGitStatusChanged(repoPath)
	return CommitResult{Sha: sha, Output: out.String()}, nil
}

// limitedBuffer is an io.Writer that accumulates written bytes up to
// commitOutputLimit and appends a truncation marker once the cap is
// crossed; writes beyond the cap are counted but dropped. It is safe for
// concurrent use: os/exec assigns it to both Stdout and Stderr of one
// exec.Cmd and copies each pipe on its own goroutine.
type limitedBuffer struct {
	mu        sync.Mutex
	b         strings.Builder
	written   int
	truncated bool
}

// Write implements io.Writer with the 64 KiB cap.
func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.truncated {
		l.written += len(p)
		return len(p), nil
	}
	room := commitOutputLimit - l.written
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

// String returns the captured output with the truncation marker appended
// when the cap was crossed. Trims the surrounding whitespace like
// combinedGitOutput.
func (l *limitedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := strings.TrimSpace(l.b.String())
	if l.truncated {
		out += commitOutputTruncationMarker
	}
	return out
}
