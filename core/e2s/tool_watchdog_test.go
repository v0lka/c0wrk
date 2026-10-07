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
	prev := toolWatchdogInterval
	toolWatchdogInterval = 5 * time.Millisecond
	defer func() { toolWatchdogInterval = prev }()

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
