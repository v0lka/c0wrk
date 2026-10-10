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
// ATTEMPTS, shared by every trigger (project switch today; startup and focus
// restore through SwitchProject as well). The timestamp is stamped on each
// attempt regardless of outcome, so a flapping network or a burst of switch
// events cannot hammer the remote.
const autoFetchMinInterval = 60 * time.Second

// autoFetchTriggerSwitch names the project-switch trigger (the hook in
// SwitchProject). Kept as a constant so log lines stay greppable and future
// triggers (focus, ticker) introduce their own names instead of inline
// strings.
const autoFetchTriggerSwitch = "switch"

// autoFetchTriggerFocus names the window-focus trigger — the
// RequestGitRemoteRefresh RPC fired by the frontend hook
// useGitFocusRefresh whenever the app window regains focus (VS Code-style
// fetch-on-focus). Same constant-for-greppable-logs rationale as
// autoFetchTriggerSwitch.
const autoFetchTriggerFocus = "focus"

// RequestGitRemoteRefresh asks the backend to refresh the active project's
// remote-tracking refs (git fetch) in the background. It is the
// window-focus trigger of the git auto-fetch feature (git.auto_fetch, see
// GitConfig): the frontend hook useGitFocusRefresh calls it whenever the
// app window gains focus, mirroring VS Code's fetch-on-focus behaviour.
//
// All gating is server-side — the frontend never decides when a fetch is
// appropriate; every gate of autoFetchOnce applies (git.auto_fetch master
// switch, active CODE project vs CHAT/No Project, git work tree, configured
// remote, the shared 60s min-interval, and the remoteOpMu TryLock that
// keeps the funnel out of the way of a manual pull/push/fetch).
//
// The RPC never blocks the UI thread: it launches the fetch in a goroutine
// and returns immediately. Failures are Debug-logged inside the funnel — an
// automatic fetch must be invisible when it cannot run.
func (f *FrontendAPI) RequestGitRemoteRefresh() {
	go f.autoFetchOnce(autoFetchTriggerFocus)
}

// gitTerminalPromptEnvVar / gitTerminalPromptDisabled pin git's credential
// prompting OFF for the automatic fetch: a background fetch must never block
// on an interactive terminal prompt — with the pin it fails fast instead of
// hanging until remoteGitCmdTimeout kills it.
const (
	gitTerminalPromptEnvVar   = "GIT_TERMINAL_PROMPT"
	gitTerminalPromptDisabled = "GIT_TERMINAL_PROMPT=0"
)

// autoFetchOnce runs ONE automatic fetch attempt for the given trigger. It
// is the single funnel every automatic fetch trigger goes through; it is
// fire-and-forget by contract (callers spawn it on its own goroutine) and
// must therefore never block for long, never return an error to a user, and
// never emit anything except the single git:status_changed on success:
//
//   - git.auto_fetch config gate (nil config fails closed; an unset
//     git.auto_fetch inside a loaded config defaults to true, matching
//     ApplyDefaults),
//   - active project must not be No Project (resolveGitRepoRoot),
//   - the workspace must be a git repository (isGitRepo, 30s-cached),
//   - 60s min-interval shared by ALL triggers (autoFetchMu/lastAutoFetchAt,
//     stamped on every attempt),
//   - at least one remote must be configured (local `git remote` check;
//     empty output skips without an event),
//   - remoteOpMu must be free RIGHT NOW (TryLock): when a manual
//     pull/push/fetch holds it, the attempt skips instantly instead of
//     queueing behind the user's operation.
//
// Every failure path is a quiet Debug log — no events, no error toasts. Only
// a successful fetch emits, exactly once, git:status_changed with the repo
// path so the frontend refreshes ahead/behind.
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

	// Min-interval gate — BEFORE any further git subprocess, so a second
	// call within the window skips before starting git at all. The stamp is
	// taken on every attempt that reaches this point (success, network
	// failure, busy lock alike): retry storms are rate-limited even when
	// every attempt fails.
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
// automatic fetches. The config is read under configMu. A nil config fails
// closed (the config subsystem is not loaded — same convention as the git
// trust seeding); a loaded config with git.auto_fetch unset defaults to
// true, matching ApplyDefaults and the GitConfig doc comment. An explicit
// `git.auto_fetch: false` is the only way to switch the funnel off.
func (f *FrontendAPI) autoFetchEnabled() bool {
	f.configMu.RLock()
	cfg := f.config
	f.configMu.RUnlock()
	if cfg == nil {
		return false
	}
	return cfg.Git.AutoFetch == nil || *cfg.Git.AutoFetch
}

// runAutoFetch executes one hardened `git fetch` in repoPath, bounded by
// remoteGitCmdTimeout with the app context as parent. It runs UNDER the
// remoteOpMu lock held by autoFetchOnce (never takes the lock itself). Any
// error is returned to autoFetchOnce, which logs it at Debug — the automatic
// funnel never surfaces fetch failures to the user.
func (f *FrontendAPI) runAutoFetch(repoPath string) error {
	ctx, cancel := context.WithTimeout(f.ctx(), remoteGitCmdTimeout)
	defer cancel()

	cmd, err := newAutoFetchCmd(ctx, repoPath)
	if err != nil {
		return err
	}

	// git fetch writes progress and diagnostics to stderr only; capture it
	// for the Debug log. stdout stays discarded (nil → /dev/null). The
	// stderr capture is a non-*os.File writer, so an orphan spawned by a
	// trusted repository's pre-fetch hook would hold the pipe open and
	// stall cmd.Run past remoteGitCmdTimeout forever — while holding
	// remoteOpMu, wedging every later remote operation. WaitDelay bounds
	// the window (review [62]); an orphan after a clean exit tolerates as
	// exec.ErrWaitDelay, which is success for this caller.
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

// newAutoFetchCmd builds the `git fetch` command for the automatic funnel on
// top of workspace.GitCmdInRepo, so the full spawn-layer hardening
// (per-invocation config scan + neutralizing -c overrides, sysproc baseline,
// GIT_EDITOR pin, fail-closed on unscannable configs) applies exactly as it
// does to every other git invocation — trusted repositories get their raw
// GitCmdRaw escape hatch through the same call. On top of that baseline it
// pins GIT_TERMINAL_PROMPT=0 (see pinGitTerminalPrompt).
func newAutoFetchCmd(ctx context.Context, repoPath string) (*exec.Cmd, error) {
	cmd, err := workspace.GitCmdInRepo(ctx, repoPath, "fetch")
	if err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}
	cmd.Dir = repoPath

	// Materialize the environment BEFORE strip+append. On the trusted path
	// GitCmdRaw leaves cmd.Env nil, meaning "inherit os.Environ()" —
	// assigning a single-element slice in one var would WIPE the inherited
	// environment down to the lone pin. Building from the materialized
	// os.Environ() (trusted) or the hardened cmd.Env (untrusted) keeps every
	// other variable intact on both paths.
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = pinGitTerminalPrompt(base)
	return cmd, nil
}

// pinGitTerminalPrompt returns env with every GIT_TERMINAL_PROMPT entry
// stripped and a single GIT_TERMINAL_PROMPT=0 appended. The strip is not
// cosmetic: with duplicate entries glibc's getenv (Linux) resolves to the
// FIRST occurrence, so an inherited GIT_TERMINAL_PROMPT=1 would win over an
// appended =0 pin and silently re-enable terminal prompting (same reasoning
// as the GIT_EDITOR strip in sysproc.hardenedGitEnv). The pin appears
// exactly once on both the hardened and the raw (trusted-repo) path.
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
// exactly once via FrontendAPILifecycle.StartAutoFetch (mirroring
// startUpdateCheckerBackground) and FrontendAPILifecycle.Cleanup stops it on
// shutdown. It complements the event-driven triggers above (project switch,
// window focus), which call autoFetchOnce directly and never consult the
// interval.

// autoFetchTriggerTicker names the periodic-ticker trigger, mirroring
// autoFetchTriggerSwitch.
const autoFetchTriggerTicker = "ticker"

// defaultAutoFetchInterval mirrors the config default for
// git.auto_fetch_interval ("2m", applied by config.ApplyDefaults). The
// config layer only stores the raw string, so this fallback covers a missing
// config, an empty value, and an unparseable value alike.
const defaultAutoFetchInterval = 2 * time.Minute

// autoFetchInterval resolves the current ticker period. The test seam
// autoFetchIntervalOverride wins; otherwise the configured raw string is
// re-read under configMu (so a runtime config edit applies on the next tick
// without an app restart) and parsed with time.ParseDuration. An empty or
// unparseable value falls back to defaultAutoFetchInterval. A parsed value
// <= 0 — the explicit "0" sentinel — disables ONLY the periodic ticker; the
// event-driven triggers are unaffected because they do not consult the
// interval at all.
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
// (git.auto_fetch_interval "0") re-reads the interval, so restoring a
// positive value re-arms the periodic fetch without an app restart. Cheap by
// design — one config read and one ticker reset per minute while disabled.
const autoFetchDisabledRecheck = time.Minute

// autoFetchDisabledRecheckInterval resolves the parked-state re-check
// cadence. The test seam autoFetchDisabledRecheckOverride wins (loop tests
// shorten it); production uses autoFetchDisabledRecheck.
func (f *FrontendAPI) autoFetchDisabledRecheckInterval() time.Duration {
	if d := f.autoFetchDisabledRecheckOverride; d > 0 {
		return d
	}
	return autoFetchDisabledRecheck
}

// StartAutoFetch starts the periodic background auto-fetch loop. It is an
// infrastructure method (FrontendAPILifecycle pattern — never exposed as a
// Wails RPC): desktop startup calls it once after the backend is ready, and
// Cleanup stops the loop on shutdown. Idempotent: a second call while the
// loop is running is a no-op, so the "start exactly once" guarantee holds
// even if the startup path is ever invoked twice.
//
// The loop context is derived from f.ctx(), so the loop also dies with the
// application context even if Cleanup were skipped; Cleanup's cancel is the
// designated, testable stop path.
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

// stopAutoFetchLoop cancels the ticker loop. Idempotent and non-blocking: it
// never waits for the goroutine. An in-flight autoFetchOnce is already
// bounded by remoteGitCmdTimeout (2 min) and cancelled through f.ctx() (see
// runAutoFetch), so a non-blocking stop keeps Cleanup fast while nothing
// leaks past those bounds.
func (f *FrontendAPI) stopAutoFetchLoop() {
	f.autoFetchLoopMu.Lock()
	cancel := f.autoFetchLoopCancel
	f.autoFetchLoopMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// autoFetchLoop is the body of the periodic auto-fetch cycle. It closes the
// done channel on exit so tests (and future callers) can observe the
// goroutine's termination — proving Cleanup leaves no leaked loop behind.
//
// On every tick the interval is re-read via autoFetchInterval, so runtime
// config edits apply without an app restart: a changed positive period
// resets the ticker (that tick does not fetch), and a period <= 0 parks the
// loop at the slow autoFetchDisabledRecheck cadence — while parked the loop
// keeps re-reading the interval, so restoring a positive value re-arms the
// ticker without an app restart (the fetch ticks themselves never fire while
// disabled).
func (f *FrontendAPI) autoFetchLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)

	interval := f.autoFetchInterval()
	disabled := interval <= 0
	if disabled {
		// git.auto_fetch_interval "0": the ticker never fetches. The
		// event-driven triggers keep working — they bypass this loop.
		// Park on the slow re-check cadence rather than on ctx alone so
		// a later positive interval re-arms without an app restart.
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
				// (this tick does not fetch) so a later positive interval
				// re-arms the ticker instead of stranding the loop on
				// ctx.Done() until an app restart.
				if !disabled {
					disabled = true
					interval = f.autoFetchDisabledRecheckInterval()
					ticker.Reset(interval)
				}
			case disabled || cur != interval:
				// Period changed (or re-enabled from the parked state):
				// re-arm the ticker at the new cadence. This tick
				// intentionally does not fetch — the next fetch is one
				// full new interval away.
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
// funnel. autoFetchTickFn is a test seam (nil in production) replacing the
// real funnel so ticker-loop tests observe ticks without git or network
// access; the funnel's own gates (master switch, min-interval, active CODE
// project, ...) apply unchanged in production.
func (f *FrontendAPI) autoFetchTick(trigger string) {
	if fn := f.autoFetchTickFn; fn != nil {
		fn(trigger)
		return
	}
	f.autoFetchOnce(trigger)
}
