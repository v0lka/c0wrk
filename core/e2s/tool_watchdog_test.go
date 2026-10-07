package e2s

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// blockingRegistry is a tool registry whose Execute blocks until released,
// modeling a tool that never returns on its own — the exact failure the
// dispatch watchdog exists to bound. released is closed by the test to let the
// detached tool goroutine finish (its send on the size-1 outcome channel always
// succeeds, so no goroutine leaks).
type blockingRegistry struct {
	released chan struct{}
}

func (r *blockingRegistry) List() []sdktools.ToolDescriptor { return nil }

func (r *blockingRegistry) Execute(_ context.Context, _ string, _ json.RawMessage) (sdktools.ToolResult, error) {
	<-r.released
	return sdktools.ToolResult{Content: "released"}, nil
}

func (r *blockingRegistry) IsToolUntrusted(string) bool { return false }

func (r *blockingRegistry) ToolSource(string) string { return "core" }

// runWithWatchdog runs the loop on its own goroutine and fails the test if Run
// does not return within the window. This is a bounded hang detector: a bare
// loop.Run call on a blocking tool would hang the whole suite, so the watchdog
// is what actually proves the timeout bounds the call.
func runWithWatchdog(t *testing.T, loop *Loop) (*Result, error) {
	t.Helper()
	ctx := t.Context()
	type outcome struct {
		res *Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := loop.Run(ctx)
		ch <- outcome{res: res, err: err}
	}()
	select {
	case o := <-ch:
		return o.res, o.err
	case <-time.After(5 * time.Second):
		t.Fatal("E2S Run did not return — a blocking tool hung the loop despite a configured ToolCallTimeout")
		return nil, nil
	}
}

// TestRun_ToolCallTimeoutDoesNotHang pins the core acceptance criterion: a tool
// that blocks past Config.ToolCallTimeout makes Run return promptly with an
// error wrapping ErrToolTimeout (naming the tool) instead of hanging.
func TestRun_ToolCallTimeoutDoesNotHang(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "slow", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"never"}`),
	}}
	reg := &blockingRegistry{released: make(chan struct{})}
	defer close(reg.released)

	cfg := testConfig()
	cfg.ToolCallTimeout = 30 * time.Millisecond
	loop := New(caller, reg, nil, cfg)

	res, err := runWithWatchdog(t, loop)
	if !errors.Is(err, ErrToolTimeout) {
		t.Fatalf("err = %v, want ErrToolTimeout", err)
	}
	// The E2S sentinel IS the executor's sentinel, so either name must match.
	if !errors.Is(err, agent.ErrToolTimeout) {
		t.Fatalf("err = %v, want the shared agent.ErrToolTimeout sentinel", err)
	}
	if res == nil || res.Status != RunStatusFailed {
		t.Fatalf("status = %v, want failed", res)
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Errorf("error %q must name the timed-out tool", err)
	}
}

// TestRun_ToolWithinTimeoutUnaffected proves the watchdog is transparent when a
// tool completes before the ceiling: the run finishes normally.
func TestRun_ToolWithinTimeoutUnaffected(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "quick", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	cfg := testConfig()
	cfg.ToolCallTimeout = time.Second
	loop := New(caller, &mockRegistry{}, nil, cfg)

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil || res.Status != RunStatusFinished {
		t.Fatalf("status = %v, want finished", res)
	}
}

// TestRun_PauseObservedWhileToolInFlight pins the second watchdog arm: a
// cooperative pause that trips while the tool is blocked stops the loop with a
// resumable ErrPaused checkpoint — not the timeout, and certainly not a hang.
func TestRun_PauseObservedWhileToolInFlight(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "slow", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"never"}`),
	}}
	reg := &blockingRegistry{released: make(chan struct{})}
	defer close(reg.released)

	// The checker passes the turn-1 step boundary (its first call returns
	// false) and trips on the next poll — i.e. while the blocking tool is in
	// flight.
	var armed atomic.Bool
	cfg := testConfig()
	cfg.PauseChecker = func(context.Context) bool { return armed.Swap(true) }
	cfg.ToolCallTimeout = 0 // the pause, not the timeout, must be the trigger
	loop := New(caller, reg, nil, cfg)
	// Shorten this loop's pause-poll cadence so the pause is observed without a
	// multi-hundred-millisecond wait. The cadence lives on the instance (never
	// in package state), so the override cannot leak into production or into a
	// parallel test.
	loop.watchdogInterval = 5 * time.Millisecond

	res, err := runWithWatchdog(t, loop)
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if res == nil || res.Status != RunStatusPaused {
		t.Fatalf("status = %v, want paused", res)
	}
}

// TestRun_BatchSubCallTimeoutAbortsAction proves the batch meta-tool path is
// bounded too: a blocking sub-call aborts the whole action with ErrToolTimeout
// rather than hanging.
func TestRun_BatchSubCallTimeoutAbortsAction(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, sdktools.ToolBatch, `{"calls":[{"tool":"slow","input":{}}]}`),
	}}
	reg := &blockingRegistry{released: make(chan struct{})}
	defer close(reg.released)

	cfg := testConfig()
	cfg.ToolCallTimeout = 30 * time.Millisecond
	loop := New(caller, reg, nil, cfg)

	res, err := runWithWatchdog(t, loop)
	if !errors.Is(err, ErrToolTimeout) {
		t.Fatalf("err = %v, want ErrToolTimeout", err)
	}
	if res == nil || res.Status != RunStatusFailed {
		t.Fatalf("status = %v, want failed", res)
	}
}

// panickingRegistry models a tool that panics inside Execute — the case the
// detached watchdog goroutine must contain so a tool bug cannot abort the host.
type panickingRegistry struct{}

func (r *panickingRegistry) List() []sdktools.ToolDescriptor { return nil }

func (r *panickingRegistry) Execute(context.Context, string, json.RawMessage) (sdktools.ToolResult, error) {
	panic("boom in tool")
}

func (r *panickingRegistry) IsToolUntrusted(string) bool { return false }

func (r *panickingRegistry) ToolSource(string) string { return "core" }

// TestRun_PanicInToolCallContained pins #12: a tool that panics on the detached
// watchdog goroutine must not crash the process — the panic is recovered there
// and surfaced as an ordinary error observation, so the loop proceeds to the
// next turn instead of aborting the host.
func TestRun_PanicInToolCallContained(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "explode", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"recovered"}`),
	}}
	loop := New(caller, &panickingRegistry{}, nil, testConfig())

	res, err := runWithWatchdog(t, loop)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil || res.Status != RunStatusFinished {
		t.Fatalf("status = %v, want finished — a tool panic must be contained, not abort the run", res.Status)
	}
	found := false
	for _, s := range res.Steps {
		if strings.Contains(s.Observation, "panicked") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("trajectory %+v must carry the recovered panic as an error observation", res.Steps)
	}
}

// cancellingBlockingRegistry cancels the run's context as soon as Execute is
// entered (modeling a shutdown landing while the tool is in flight) and then
// blocks until released, so the watchdog observes cancellation, not a result.
type cancellingBlockingRegistry struct {
	cancel   context.CancelFunc
	released chan struct{}
}

func (r *cancellingBlockingRegistry) List() []sdktools.ToolDescriptor { return nil }

func (r *cancellingBlockingRegistry) Execute(context.Context, string, json.RawMessage) (sdktools.ToolResult, error) {
	r.cancel()
	<-r.released
	return sdktools.ToolResult{Content: "released"}, nil
}

func (r *cancellingBlockingRegistry) IsToolUntrusted(string) bool { return false }

func (r *cancellingBlockingRegistry) ToolSource(string) string { return "core" }

// runWithCtxWatchdog is runWithWatchdog with a caller-supplied context, so a
// test can drive cancellation itself. It remains a bounded hang detector.
func runWithCtxWatchdog(ctx context.Context, t *testing.T, loop *Loop) (*Result, error) {
	t.Helper()
	type outcome struct {
		res *Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := loop.Run(ctx)
		ch <- outcome{res: res, err: err}
	}()
	select {
	case o := <-ch:
		return o.res, o.err
	case <-time.After(5 * time.Second):
		t.Fatal("E2S Run did not return — the cancelled in-flight tool hung the loop")
		return nil, nil
	}
}

// TestRun_CancellationDuringSingleToolCallNoSpuriousStep pins #7: cancelling
// the run while a single (non-batch) tool call is in flight must checkpoint as
// canceled WITHOUT appending a bogus "tool execution error: context canceled"
// step — a fabricated failure observation for a call that was merely
// interrupted.
func TestRun_CancellationDuringSingleToolCallNoSpuriousStep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "probe", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"never"}`),
	}}
	reg := &cancellingBlockingRegistry{cancel: cancel, released: make(chan struct{})}
	defer close(reg.released)

	loop := New(caller, reg, nil, testConfig())

	res, err := runWithCtxWatchdog(ctx, t, loop)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res == nil || res.Status != RunStatusCanceled {
		t.Fatalf("status = %v, want canceled", res.Status)
	}
	for _, s := range res.Steps {
		if strings.Contains(s.Observation, "tool execution error") {
			t.Fatalf("cancelled dispatch must not append a spurious error step: %+v", s)
		}
	}
}

// TestRun_CancelWinsOverPauseWhileToolInFlight pins the E2S watchdog's
// cancellation precedence: when a tool is in flight and BOTH the run context is
// cancelled AND the pause checker trips on the same tick, Run must report a
// cancellation (RunStatusCanceled), never a resumable pause.
func TestRun_CancelWinsOverPauseWhileToolInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "probe", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"never"}`),
	}}
	reg := &cancellingBlockingRegistry{cancel: cancel, released: make(chan struct{})}
	defer close(reg.released)

	// The checker passes the turn-1 step boundary (its first call returns false)
	// and trips on the next poll — i.e. while the tool is in flight, at the same
	// time the registry cancels the context.
	var armed atomic.Bool
	cfg := testConfig()
	cfg.PauseChecker = func(context.Context) bool { return armed.Swap(true) }
	loop := New(caller, reg, nil, cfg)
	loop.watchdogInterval = time.Millisecond

	res, err := runWithCtxWatchdog(ctx, t, loop)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (cancellation must win over a simultaneous pause tick)", err)
	}
	if res == nil || res.Status != RunStatusCanceled {
		t.Fatalf("status = %v, want canceled", res.Status)
	}
}

// TestRun_ExemptToolNotBoundedByTimeout pins the E2S half of the interactive-tool
// exemption: an E2S-available tool that blocks on a human (ask_user) or on
// sub-work (a blocking delegate) must not be killed by Config.ToolCallTimeout,
// while an ordinary tool still is. The blocking registry releases the exempt
// call well after the ceiling would have fired; the run must then finish
// normally instead of failing with ErrToolTimeout.
func TestRun_ExemptToolNotBoundedByTimeout(t *testing.T) {
	caller := &scriptedCaller{responses: []*llm.ChatResponse{
		stepResponse(`{}`, "ask_user", `{}`),
		stepResponse(`{}`, "finish", `{"answer":"done"}`),
	}}
	reg := &blockingRegistry{released: make(chan struct{})}
	cfg := testConfig()
	cfg.ToolCallTimeout = 20 * time.Millisecond
	loop := New(caller, reg, nil, cfg)

	// Release the blocking tool AFTER the ceiling would have fired. The
	// two-case watchdog shape (a non-empty timeout arm plus a context guard) is
	// the sanctioned barrier — it does not sleep to give the scheduler a
	// chance, and the context arm prevents a leaked goroutine if the run has
	// already ended.
	go func() {
		select {
		case <-time.After(80 * time.Millisecond):
			close(reg.released)
		case <-t.Context().Done():
		}
	}()

	res, err := runWithWatchdog(t, loop)
	if err != nil {
		t.Fatalf("exempt E2S tool must not be bounded by the ceiling, got err=%v", err)
	}
	if res == nil || res.Status != RunStatusFinished {
		t.Fatalf("status = %v, want finished", res)
	}
}
