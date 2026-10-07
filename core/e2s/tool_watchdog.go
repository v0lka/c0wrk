package e2s

import (
	"context"
	"encoding/json"
	"time"

	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrToolTimeout is returned (wrapped with the tool name by the dispatch site)
// from Loop.Run when a single action's tool call does not complete within
// Config.ToolCallTimeout. It is the SAME sentinel the sp4rk executor surfaces
// (agent.ErrToolTimeout), so a host matching either name observes the identical
// timeout class; the E2S loop's own dispatch has no way to reach the executor's
// watchdog, so it reuses the sentinel rather than minting a parallel one.
var ErrToolTimeout = agent.ErrToolTimeout

// defaultToolWatchdogInterval is the cadence at which the E2S dispatch watchdog
// polls the cooperative pause checker while a tool call is in flight. It is
// deliberately short so a pause is observed within a fraction of a second even
// when the underlying tool is blocked (a blocking syscall cannot be interrupted,
// so the watchdog must poll). It mirrors the executor's tool watchdog cadence.
const defaultToolWatchdogInterval = 250 * time.Millisecond

// toolWatchdogInterval is the live pause-poll cadence used by executeToolCall.
// It is a package-level var (not a const) purely as a test seam: production
// keeps defaultToolWatchdogInterval, and tests shorten it so a pause is
// observed without a multi-hundred-millisecond wait. It must not be mutated
// while an executeToolCall is running.
var toolWatchdogInterval = defaultToolWatchdogInterval

// toolCallOutcome carries the result of the detached Registry.Execute call back
// to executeToolCall. The channel it travels on is buffered (capacity 1) so the
// producing goroutine always sends and exits, even when executeToolCall has
// already returned via the pause/timeout/cancel arms — that is what keeps the
// goroutine from leaking on the abandoned paths.
type toolCallOutcome struct {
	result sdktools.ToolResult
	err    error
}

// executeToolCall dispatches a single E2S action through l.registry.Execute
// while remaining responsive to signals that must interrupt a stuck call. It
// runs the tool in its own goroutine and selects over four events:
//
//   - the tool completing normally — its result (and error) are returned as-is;
//   - ctx.Done() — the run's context was cancelled or its deadline elapsed
//     (shutdown/cancel), returning ctx.Err();
//   - the cooperative pause checker tripping (polled every
//     toolWatchdogInterval) — returning ErrPaused so the caller can emit a
//     resumable checkpoint. c0wrk pause does NOT cancel ctx, so this poll is
//     the only way a pause is observed while a tool is blocked;
//   - cfg.ToolCallTimeout elapsing — returning ErrToolTimeout. A zero timeout
//     (the default) disables this arm.
//
// IMPORTANT: the underlying tool call is NOT cancelled when the pause, timeout,
// or cancellation arm fires. Go cannot preempt a blocked syscall, so the tool
// keeps running in its detached goroutine until it returns on its own; only the
// WAIT is abandoned. Callers must therefore treat the tool's side effects as
// having possibly occurred, and must not assume the call was stopped. The
// goroutine itself is bounded: it ends as soon as Execute returns (its send on
// the size-1 channel never blocks), so no goroutine leaks once the tool
// finishes — the one case that can outlive the run is a tool that never
// returns, which no watchdog can reclaim.
//
// Mirrors the sp4rk executor's (*Executor).executeToolCall so the E2S path —
// which bypasses the executor entirely — carries the same
// "no single tool call may block the loop indefinitely" guarantee. It must be
// called from the Run goroutine (the PauseChecker and ToolCallTimeout config
// fields are read here without synchronization, matching their set-before-Run
// contract). The registry must be non-nil; executeSingle only reaches this path
// when l.registry != nil.
func (l *Loop) executeToolCall(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	done := make(chan toolCallOutcome, 1)
	go func() {
		res, err := l.registry.Execute(ctx, name, input)
		done <- toolCallOutcome{result: res, err: err}
	}()

	// Pause polling is armed only when a checker is installed; otherwise the
	// tick channel stays nil and its select arm is permanently disabled.
	var tickCh <-chan time.Time
	if l.cfg.PauseChecker != nil {
		ticker := time.NewTicker(toolWatchdogInterval)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	// The timeout arm is armed only when a positive ceiling is configured;
	// otherwise the timer channel stays nil (disabled). A zero value means
	// "no timeout", preserving the pre-watchdog behavior for callers that do
	// not opt in.
	var timeoutCh <-chan time.Time
	if l.cfg.ToolCallTimeout > 0 {
		timer := time.NewTimer(l.cfg.ToolCallTimeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	for {
		select {
		case out := <-done:
			return out.result, out.err
		case <-ctx.Done():
			return sdktools.ToolResult{}, ctx.Err()
		case <-tickCh:
			if l.cfg.PauseChecker(ctx) {
				l.log().Debug("e2s tool watchdog: pause observed while tool in flight", "tool", name)
				return sdktools.ToolResult{}, ErrPaused
			}
		case <-timeoutCh:
			l.log().Debug("e2s tool watchdog: tool call exceeded the configured timeout",
				"tool", name, "timeout", l.cfg.ToolCallTimeout.String())
			return sdktools.ToolResult{}, ErrToolTimeout
		}
	}
}
