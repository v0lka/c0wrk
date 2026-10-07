package e2s

import (
	"context"
	"encoding/json"
	"fmt"
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

// toolCallTimeoutExemptTools names the E2S-available tools exempt from
// Config.ToolCallTimeout. They block inside Execute by design — on a human
// (ask_user) or on sub-work (a blocking delegate running its subagents to
// completion) — so a raw wall clock would fail a perfectly healthy run at the
// ceiling. Mirrors the sp4rk executor's default exemption set, narrowed to the
// tools E2S actually exposes (the plan-workflow tools declare_plan/execute_plan
// are stripped from the E2S catalog, and propose_goal with the goal-only set).
var toolCallTimeoutExemptTools = map[string]struct{}{
	"ask_user": {},
	"delegate": {},
}

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
//     l.watchdogInterval, default defaultToolWatchdogInterval) — returning
//     ErrPaused so the caller can emit a resumable checkpoint. c0wrk pause does
//     NOT cancel ctx, so this poll is the only way a pause is observed while a
//     tool is blocked;
//   - cfg.ToolCallTimeout elapsing — returning ErrToolTimeout. A zero timeout
//     (the default) disables this arm, and a tool named in
//     toolCallTimeoutExemptTools (ask_user, a blocking delegate) is never
//     bounded at all, so an interactive or long-running call cannot fail the
//     run at the ceiling.
//
// IMPORTANT: the underlying tool call is asked to stop — not preempted — when
// the pause, timeout, or cancellation arm fires: it runs on a child of ctx that
// is cancelled on every abandon path (a pause, a timeout, or a cancellation —
// plus the deferred cancel on any OTHER exit), so a
// still-pending interactive prompt or blocking sub-work is dismissed rather than
// orphaned and a late "Allow" cannot execute a mutation with no owning run
// (mirrors the sp4rk executor). ctx itself is never cancelled here. Because Go
// cannot preempt a blocked syscall, a tool already past its last cancellation
// check — or parked in an uninterruptible call — can still run to completion in
// its detached goroutine; only the WAIT is abandoned. Callers must therefore
// treat the tool's side effects as having possibly occurred, and must not assume
// the call was stopped. The goroutine itself is bounded: it ends as soon as
// Execute returns or panics (its send on the size-1 channel never blocks), so no
// goroutine leaks once the tool finishes — the one case that can outlive the run
// is a tool that never returns, which no watchdog can reclaim.
//
// A panic inside l.registry.Execute is recovered on the detached goroutine
// (where the call now runs) and delivered as the outcome's error, preserving
// the process-level panic containment that held when the call ran inline on
// the guarded Run goroutine — an unrecovered panic on this goroutine would
// abort the whole host.
//
// Mirrors the sp4rk executor's (*Executor).executeToolCall so the E2S path —
// which bypasses the executor entirely — carries the same
// "no single tool call may block the loop indefinitely" guarantee. It must be
// called from the Run goroutine (the PauseChecker and ToolCallTimeout config
// fields are read here without synchronization, matching their set-before-Run
// contract). The registry must be non-nil; executeSingle only reaches this path
// when l.registry != nil.
func (l *Loop) executeToolCall(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	// Derive a cancellable context for the tool call so the abandon paths can
	// dismiss a still-pending call (a confirmation card, an ask_user prompt,
	// blocking sub-work) instead of orphaning it — the same guarantee the sp4rk
	// executor's watchdog provides. ctx itself is never cancelled here — only
	// this child, and the deferred cancel releases it on every exit.
	callCtx, cancelCall := context.WithCancel(ctx)
	defer cancelCall()

	done := make(chan toolCallOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- toolCallOutcome{err: fmt.Errorf("tool %q panicked: %v", name, r)}
			}
		}()
		res, err := l.registry.Execute(callCtx, name, input)
		done <- toolCallOutcome{result: res, err: err}
	}()

	// A tool that has ALREADY completed must win over an arm that happens to be
	// ready at the same instant: select picks uniformly at random among ready
	// cases, so entering the blocking loop with both `done` and ctx.Done() (or
	// the pause tick, or the timeout) ready could discard a finished tool's real
	// result and report the call as cancelled, paused, or timed out instead.
	// A tool that has ALREADY completed must win over any arm: select picks
	// uniformly at random among ready cases, so a completion ready at the same
	// instant as ctx.Done()/a pause tick/the timeout must be drained first, or a
	// finished tool's real result would be discarded (see drainToolResult).
	if res, ok, err := drainToolResult(done); ok {
		return res, err
	}

	// Pause polling is armed only when a checker is installed; otherwise the
	// tick channel stays nil and its select arm is permanently disabled.
	var tickCh <-chan time.Time
	if l.cfg.PauseChecker != nil {
		interval := l.watchdogInterval
		if interval <= 0 {
			interval = defaultToolWatchdogInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	// The timeout arm is armed only when a positive ceiling is configured AND
	// the tool is not exempt; otherwise the timer channel stays nil (disabled).
	// A zero value means "no timeout", preserving the pre-watchdog behavior for
	// callers that do not opt in.
	var timeoutCh <-chan time.Time
	if l.cfg.ToolCallTimeout > 0 {
		if _, exempt := toolCallTimeoutExemptTools[name]; !exempt {
			timer := time.NewTimer(l.cfg.ToolCallTimeout)
			defer timer.Stop()
			timeoutCh = timer.C
		}
	}

	for {
		select {
		case out := <-done:
			return out.result, out.err
		case <-ctx.Done():
			// A tool that completed at the same instant must still win.
			if res, ok, err := drainToolResult(done); ok {
				return res, err
			}
			return sdktools.ToolResult{}, ctx.Err()
		case <-tickCh:
			// Mirror the step-boundary precedence: a cancelled/deadline run
			// must report cancellation, never pause, when the pause tick and
			// ctx.Done() are ready in the same selection.
			if err := ctx.Err(); err != nil {
				if res, ok, dErr := drainToolResult(done); ok {
					return res, dErr
				}
				return sdktools.ToolResult{}, err
			}
			// A tool that completed on this tick must win over the pause.
			if res, ok, dErr := drainToolResult(done); ok {
				return res, dErr
			}
			if l.cfg.PauseChecker(ctx) {
				l.log().Debug("e2s tool watchdog: pause observed while tool in flight", "tool", name)
				return sdktools.ToolResult{}, ErrPaused
			}
		case <-timeoutCh:
			// Mirror the pause-tick arm: a cancelled/deadline run must report
			// cancellation, not a timeout, when the ceiling and ctx.Done() are
			// ready in the same selection.
			if err := ctx.Err(); err != nil {
				if res, ok, dErr := drainToolResult(done); ok {
					return res, dErr
				}
				return sdktools.ToolResult{}, err
			}
			// A tool that completed at the ceiling must win over the timeout.
			if res, ok, dErr := drainToolResult(done); ok {
				return res, dErr
			}
			l.log().Debug("e2s tool watchdog: tool call exceeded the configured timeout",
				"tool", name, "timeout", l.cfg.ToolCallTimeout.String())
			// Cancel the tool's own context so a pending prompt / blocking
			// sub-work is dismissed rather than orphaned.
			cancelCall()
			return sdktools.ToolResult{}, ErrToolTimeout
		}
	}
}

// drainToolResult non-blockingly receives an already-delivered tool result so
// every blocking arm can honour a completion ready at the same instant the arm
// fires (select picks uniformly among ready cases). Mirrors the sp4rk
// executor's helper.
func drainToolResult(done <-chan toolCallOutcome) (sdktools.ToolResult, bool, error) {
	select {
	case out := <-done:
		return out.result, true, out.err
	default:
		return sdktools.ToolResult{}, false, nil
	}
}
