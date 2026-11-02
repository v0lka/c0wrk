package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/core/workspace"
)

// ---------------------------------------------------------------------------
// Automatic background fetch funnel
// ---------------------------------------------------------------------------

// autoFetchMinInterval is the minimum time between two automatic fetch
// ATTEMPTS, shared by every trigger; the stamp is taken on each attempt
// regardless of outcome, so a flapping network or a burst of triggers cannot
// hammer the remote.
const autoFetchMinInterval = 60 * time.Second

// autoFetchTriggerSwitch names the project-switch trigger; kept as a constant
// so log lines stay greppable and future triggers introduce their own names.
const autoFetchTriggerSwitch = "switch"

// autoFetchTriggerFocus names the window-focus trigger —
// RequestGitRemoteRefresh, fired by the frontend hook useGitFocusRefresh on
// focus (VS Code-style fetch-on-focus).
const autoFetchTriggerFocus = "focus"

// RequestGitRemoteRefresh is the window-focus trigger of the git auto-fetch
// feature (see specs/domains/git-auto-fetch.md): it launches one gated
// background fetch and returns immediately. All gating is server-side — every
// gate of autoFetchOnce applies. Failures are Debug-logged inside the funnel:
// an automatic fetch must be invisible when it cannot run.
func (f *FrontendAPI) RequestGitRemoteRefresh() {
	go f.autoFetchOnce(autoFetchTriggerFocus)
}

// gitTerminalPromptEnvVar / gitTerminalPromptDisabled pin git's credential
// prompting OFF for the automatic fetch: fail fast instead of hanging until
// remoteGitCmdTimeout kills it.
const (
	gitTerminalPromptEnvVar   = "GIT_TERMINAL_PROMPT"
	gitTerminalPromptDisabled = "GIT_TERMINAL_PROMPT=0"
)

// autoFetchOnce runs ONE automatic fetch attempt for the given trigger — the
// single funnel every trigger goes through (gates and their order:
// specs/domains/git-auto-fetch.md). Fire-and-forget by contract: never blocks
// long, never returns an error to a user, emits nothing except the single
// git:status_changed on success. Every failure path is a quiet Debug log.
func (f *FrontendAPI) autoFetchOnce(trigger string) {
	if !f.autoFetchEnabled() {
		f.log().Debug("auto-fetch disabled by config", "trigger", trigger)
		return
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		f.log().Debug("auto-fetch skipped: no active git project", "trigger", trigger, "error", err)
		return
	}

	if !f.isGitRepo(repoPath) {
		f.log().Debug("auto-fetch skipped: workspace is not a git repository", "trigger", trigger, "repo", repoPath)
		return
	}

	// Min-interval gate — BEFORE any further git subprocess. The stamp is
	// taken on every attempt that reaches this point, so retry storms are
	// rate-limited even when every attempt fails.
	f.autoFetchMu.Lock()
	if !f.lastAutoFetchAt.IsZero() && time.Since(f.lastAutoFetchAt) < autoFetchMinInterval {
		f.autoFetchMu.Unlock()
		f.log().Debug("auto-fetch skipped: within min interval since last attempt",
			"trigger", trigger, "min_interval", autoFetchMinInterval)
		return
	}
	f.lastAutoFetchAt = time.Now()
	f.autoFetchMu.Unlock()

	remotes, err := f.runGitCmd(repoPath, "remote")
	if err != nil {
		f.log().Debug("auto-fetch skipped: git remote failed", "trigger", trigger, "repo", repoPath, "error", err)
		return
	}
	if strings.TrimSpace(remotes) == "" {
		f.log().Debug("auto-fetch skipped: no remotes configured", "trigger", trigger, "repo", repoPath)
		return
	}

	if !f.remoteOpMu.TryLock() {
		// A manual remote operation (pull/push/fetch) is in flight — get
		// out of its way immediately rather than queueing behind it.
		f.log().Debug("auto-fetch skipped: manual remote operation in progress", "trigger", trigger)
		return
	}
	defer f.remoteOpMu.Unlock()

	if err := f.runAutoFetch(repoPath); err != nil {
		f.log().Debug("auto-fetch failed", "trigger", trigger, "repo", repoPath, "error", err)
		return
	}
	f.emitGitStatusChanged(repoPath)
}

// autoFetchEnabled reports whether the git.auto_fetch master gate allows
// automatic fetches. A nil config fails closed; an unset value defaults to
// true, matching ApplyDefaults — an explicit `git.auto_fetch: false` is the
// only way to switch the funnel off.
func (f *FrontendAPI) autoFetchEnabled() bool {
	f.configMu.RLock()
	cfg := f.config
	f.configMu.RUnlock()
	if cfg == nil {
		return false
	}
	return cfg.Git.AutoFetch == nil || *cfg.Git.AutoFetch
}

// runAutoFetch executes one hardened `git fetch` bounded by
// remoteGitCmdTimeout, UNDER the remoteOpMu lock held by autoFetchOnce (it
// never takes the lock itself); errors return to the caller's Debug log — the
// automatic funnel never surfaces fetch failures to the user.
func (f *FrontendAPI) runAutoFetch(repoPath string) error {
	ctx, cancel := context.WithTimeout(f.ctx(), remoteGitCmdTimeout)
	defer cancel()

	cmd, err := newAutoFetchCmd(ctx, repoPath)
	if err != nil {
		return err
	}

	// git fetch writes progress and diagnostics to stderr only; capture it
	// for the Debug log (stdout stays discarded). The stderr capture is a
	// non-*os.File writer, so an orphan spawned by a trusted repository's
	// pre-fetch hook would hold the pipe open and stall cmd.Run past
	// remoteGitCmdTimeout — while holding remoteOpMu. WaitDelay bounds that
	// window; an orphan after a clean exit tolerates as exec.ErrWaitDelay,
	// which is success for this caller.
	cmd.WaitDelay = commitWaitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("git fetch: %w: %s", err, msg)
		}
		return fmt.Errorf("git fetch: %w", err)
	}
	return nil
}

// newAutoFetchCmd builds the automatic funnel's `git fetch` on top of
// workspace.GitCmdInRepo, so the full spawn-layer hardening applies exactly as
// to every other git invocation, then pins GIT_TERMINAL_PROMPT=0 (see
// pinGitTerminalPrompt).
func newAutoFetchCmd(ctx context.Context, repoPath string) (*exec.Cmd, error) {
	cmd, err := workspace.GitCmdInRepo(ctx, repoPath, "fetch")
	if err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}
	cmd.Dir = repoPath

	// Materialize the environment BEFORE strip+append: on the trusted path
	// cmd.Env is nil ("inherit os.Environ()"), and assigning a single-element
	// slice in one var would WIPE the inherited environment down to the lone
	// pin. Building from the materialized environ keeps every other variable
	// intact on both paths.
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = pinGitTerminalPrompt(base)
	return cmd, nil
}

// pinGitTerminalPrompt returns env with every GIT_TERMINAL_PROMPT entry
// stripped and a single =0 appended. The strip is not cosmetic: glibc's
// getenv resolves duplicate names to the FIRST occurrence, so an inherited =1
// would win over an appended =0 and silently re-enable prompting.
func pinGitTerminalPrompt(env []string) []string {
	filtered := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, gitTerminalPromptEnvVar+"=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	return append(filtered, gitTerminalPromptDisabled)
}

// ---------------------------------------------------------------------------
// Periodic auto-fetch ticker (git.auto_fetch_interval)
// ---------------------------------------------------------------------------

// The ticker loop is infrastructure, not an RPC: desktop startup starts it
// exactly once via FrontendAPILifecycle.StartAutoFetch and Cleanup stops it.
// It complements the event-driven triggers, which call autoFetchOnce directly
// and never consult the interval.

// autoFetchTriggerTicker names the periodic-ticker trigger, mirroring
// autoFetchTriggerSwitch.
const autoFetchTriggerTicker = "ticker"

// defaultAutoFetchInterval mirrors the config default for
// git.auto_fetch_interval ("2m"); the config layer only stores the raw
// string, so this fallback covers missing, empty and unparseable alike.
const defaultAutoFetchInterval = 2 * time.Minute

// autoFetchInterval resolves the current ticker period: the test seam wins,
// else the configured raw string is re-read under configMu (runtime edits
// apply on the next tick) and parsed with time.ParseDuration. Empty or
// unparseable falls back to the default; a value <= 0 — the explicit "0"
// sentinel — disables ONLY the periodic ticker.
func (f *FrontendAPI) autoFetchInterval() time.Duration {
	if d := f.autoFetchIntervalOverride; d > 0 {
		return d
	}
	f.configMu.RLock()
	cfg := f.config
	f.configMu.RUnlock()
	if cfg == nil {
		return defaultAutoFetchInterval
	}
	raw := cfg.Git.AutoFetchInterval
	if raw == "" {
		return defaultAutoFetchInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		f.log().Debug("git.auto_fetch_interval unparseable, using default", "value", raw)
		return defaultAutoFetchInterval
	}
	return d
}

// autoFetchDisabledRecheck is the cadence at which a parked ticker loop
// re-reads the interval, so restoring a positive value re-arms the periodic
// fetch without an app restart.
const autoFetchDisabledRecheck = time.Minute

// autoFetchDisabledRecheckInterval resolves the parked-state re-check
// cadence; the test seam wins (loop tests shorten it).
func (f *FrontendAPI) autoFetchDisabledRecheckInterval() time.Duration {
	if d := f.autoFetchDisabledRecheckOverride; d > 0 {
		return d
	}
	return autoFetchDisabledRecheck
}

// StartAutoFetch starts the periodic background auto-fetch loop — an
// infrastructure method, never a Wails RPC; idempotent. The loop context
// derives from f.ctx(), so it also dies with the application; Cleanup's
// cancel is the designated, testable stop path.
func (l *FrontendAPILifecycle) StartAutoFetch() {
	f := l.f
	f.autoFetchLoopMu.Lock()
	if f.autoFetchLoopCancel != nil {
		f.autoFetchLoopMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(f.ctx())
	done := make(chan struct{})
	f.autoFetchLoopCancel = cancel
	f.autoFetchLoopDone = done
	f.autoFetchLoopMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				f.log().Error("auto-fetch ticker loop panicked", "panic", r)
			}
		}()
		f.autoFetchLoop(ctx, done)
	}()
}

// stopAutoFetchLoop cancels the ticker loop — idempotent, never waits for
// the goroutine; an in-flight autoFetchOnce is already bounded by
// remoteGitCmdTimeout and f.ctx().
func (f *FrontendAPI) stopAutoFetchLoop() {
	f.autoFetchLoopMu.Lock()
	cancel := f.autoFetchLoopCancel
	f.autoFetchLoopMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// autoFetchLoop is the periodic cycle body; it closes done on exit so tests
// can observe the goroutine's termination. On every tick the interval is
// re-read, so runtime config edits apply without a restart: a changed
// positive period resets the ticker (that tick does not fetch), and a period
// <= 0 parks the loop at the slow re-check cadence until a positive value
// re-arms it.
func (f *FrontendAPI) autoFetchLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)

	interval := f.autoFetchInterval()
	disabled := interval <= 0
	if disabled {
		// git.auto_fetch_interval "0": the ticker never fetches (the
		// event-driven triggers bypass this loop); park on the slow
		// re-check cadence so a later positive interval re-arms.
		interval = f.autoFetchDisabledRecheckInterval()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := f.autoFetchInterval()
			switch {
			case cur <= 0:
				// Disabled at runtime: park on the slow re-check cadence
				// (this tick does not fetch) instead of stranding on
				// ctx.Done() until a restart.
				if !disabled {
					disabled = true
					interval = f.autoFetchDisabledRecheckInterval()
					ticker.Reset(interval)
				}
			case disabled || cur != interval:
				// Period changed (or re-enabled from parked): re-arm at
				// the new cadence; this tick intentionally does not fetch.
				disabled = false
				interval = cur
				ticker.Reset(interval)
			default:
				f.autoFetchTick(autoFetchTriggerTicker)
			}
		}
	}
}

// autoFetchTick dispatches one periodic fetch into the shared autoFetchOnce
// funnel; autoFetchTickFn is a test seam (nil in production) letting
// ticker-loop tests observe ticks without git or network access.
func (f *FrontendAPI) autoFetchTick(trigger string) {
	if fn := f.autoFetchTickFn; fn != nil {
		fn(trigger)
		return
	}
	f.autoFetchOnce(trigger)
}
