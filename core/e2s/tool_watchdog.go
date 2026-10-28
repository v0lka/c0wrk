package e2s

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrToolTimeout — a single action's tool call exceeded Config.ToolCallTimeout
// (wrapped with the tool name at the dispatch site); the E2S dispatch has
// no way to reach the executor's watchdog, so it reuses the sp4rk
// executor's sentinel.
var ErrToolTimeout = agent.ErrToolTimeout

// defaultToolWatchdogInterval is the watchdog's pause-poll cadence while a
// tool call is in flight — deliberately short, because a blocked syscall
// cannot be interrupted and the pause must be polled.
const defaultToolWatchdogInterval = 250 * time.Millisecond

// toolCallTimeoutExemptTools names the E2S-available tools exempt from
// Config.ToolCallTimeout — they block inside Execute by design (on a human,
// or on blocking delegate sub-work), so a wall clock would fail a healthy
// run. Deliberately NOT operator-configurable: the timeouts knob covers
// only the Conductor/executor loops.
var toolCallTimeoutExemptTools = map[string]struct{}{
	"ask_user": {},
	"delegate": {},
}

// toolCallOutcome carries the detached Registry.Execute result back to
// executeToolCall; its channel is buffered (capacity 1) so the producer
// always sends and exits, even on abandoned pause/timeout/cancel paths.
type toolCallOutcome struct {
	result sdktools.ToolResult
	err    error
}

// executeToolCall dispatches a single E2S action through l.registry.Execute
// while staying responsive to interrupting signals (see specs/domains/e2s.md
// for the full contract): the call runs on a detached, panic-recovering
// goroutine and select races completion, ctx.Done() (→ ctx.Err()), the pause
// checker polled every l.watchdogInterval (→ ErrPaused — c0wrk pause does
// not cancel ctx, so the poll is the only mid-call observation), and
// Config.ToolCallTimeout (→ ErrToolTimeout; zero disables, exempt tools
// unbounded).
//
// Abandoning the wait cancels only the call's child context: the tool is
// asked to stop, never preempted — a tool past its last cancellation check
// may still complete detached, so its side effects must be treated as
// possibly having occurred. Mirrors the sp4rk executor's watchdog; must run
// on the Run goroutine, registry non-nil.
func (l *Loop) executeToolCall(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	// A cancellable child so abandon paths dismiss a still-pending call
	// instead of orphaning it; ctx itself is never cancelled here.
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

	// A completion already delivered must win over any arm: select picks
	// uniformly among ready cases, so drain before blocking
	// (see drainToolResult).
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

	// The timeout arm arms only for a positive ceiling on a non-exempt tool;
	// zero means "no timeout".
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
			// Step-boundary precedence: cancellation beats pause when both
			// arms are ready in the same selection.
			if err := ctx.Err(); err != nil {
				if res, ok, dErr := drainToolResult(done); ok {
					return res, dErr
				}
				return sdktools.ToolResult{}, err
			}
			// A completion on this tick beats the pause.
			if res, ok, dErr := drainToolResult(done); ok {
				return res, dErr
			}
			if l.cfg.PauseChecker(ctx) {
				l.log().Debug("e2s tool watchdog: pause observed while tool in flight", "tool", name)
				return sdktools.ToolResult{}, ErrPaused
			}
		case <-timeoutCh:
			// Cancellation beats timeout when both arms are ready in the same
			// selection.
			if err := ctx.Err(); err != nil {
				if res, ok, dErr := drainToolResult(done); ok {
					return res, dErr
				}
				return sdktools.ToolResult{}, err
			}
			// A completion at the ceiling beats the timeout.
			if res, ok, dErr := drainToolResult(done); ok {
				return res, dErr
			}
			l.log().Debug("e2s tool watchdog: tool call exceeded the configured timeout",
				"tool", name, "timeout", l.cfg.ToolCallTimeout.String())
			// Dismiss a pending prompt / blocking sub-work (don't orphan it).
			cancelCall()
			return sdktools.ToolResult{}, ErrToolTimeout
		}
	}
}

// drainToolResult non-blockingly receives an already-delivered tool result so
// a blocking arm honours a completion ready at the same instant it fires.
func drainToolResult(done <-chan toolCallOutcome) (sdktools.ToolResult, bool, error) {
	select {
	case out := <-done:
		return out.result, true, out.err
	default:
		return sdktools.ToolResult{}, false, nil
	}
}
