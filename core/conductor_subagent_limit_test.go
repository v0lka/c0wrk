package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// concurrencyProbe parks each call on its own release channel. The test owns
// wave admission and drains all in-process workers before reading the result.
type concurrencyProbe struct {
	active    atomic.Int32
	maxActive atomic.Int32
	entered   chan chan struct{}
	abort     chan struct{}
}

func (p *concurrencyProbe) call(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	cur := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		prev := p.maxActive.Load()
		if cur <= prev || p.maxActive.CompareAndSwap(prev, cur) {
			break
		}
	}
	release := make(chan struct{})
	p.entered <- release
	select {
	case <-release:
	case <-p.abort:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: "done",
			ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer":"ok"}`)},
			},
		},
		StopReason: "tool_use",
		Usage:      llm.TokenUsage{InputTokens: 5, OutputTokens: 5},
	}, nil
}

// driveLimitWaves runs only inside a synctest bubble. Wait proves all remaining
// workers are parked, so an occupancy assertion is not a quiet-window sample.
func driveLimitWaves(t *testing.T, p *concurrencyProbe, total, limit int, launch func()) {
	t.Helper()
	done := make(chan struct{})
	defer func() {
		close(p.abort) // Release every entered call before joining, even on Fatal.
		synctest.Wait()
		<-done
	}()
	go func() {
		defer close(done)
		launch()
	}()
	for completed := 0; completed < total; completed += limit {
		synctest.Wait()
		want := min(limit, total-completed)
		if got := len(p.entered); got != want {
			t.Fatalf("wave at completed=%d has %d entrants, want %d", completed, got, want)
		}
		if got := p.active.Load(); got != int32(want) {
			t.Fatalf("wave at completed=%d has %d active calls, want %d", completed, got, want)
		}
		for range want {
			close(<-p.entered)
		}
	}
	synctest.Wait()
	<-done
	if got := p.active.Load(); got != 0 {
		t.Errorf("active calls after join = %d, want 0", got)
	}
}

// newLimitTestLauncher builds a conductorLauncher wired with the shared probe
// and the given max-parallel cap, mirroring the harness the existing
// defaultPlanStepWave tests use.
func newLimitTestLauncher(probe *concurrencyProbe, limit int) *conductorLauncher {
	return &conductorLauncher{
		bb: orchestration.NewMapBlackboard(),
		deps: conductorDeps{
			contextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
				return &mockContextManager{systemPrompt: systemPrompt}
			},
			toolRegistry:         newSubagentTestRegistry(subagentToolSet()),
			llm:                  &mockLLMCaller{callFn: probe.call},
			toolExec:             &mockToolExecutor{},
			emitter:              &mockEmitter{},
			maxParallelSubagents: limit,
		},
	}
}

// limitTestCtx builds the routing context (domain + complexity) that
// buildSubAgentTask derives the default step budget and compaction strategy
// from — mirroring the existing defaultPlanStepWave test harness.
func limitTestCtx() context.Context {
	return WithComplexity(WithDomain(sdktools.WithWorkspacePath(context.Background(), "/ws"), "code"), 5)
}

// TestDefaultPlanStepWave_HonorsMaxParallelSubagents proves the plan-wave path
// honors agents.max_parallel_subagents: a wave of independent steps must not
// run more than the configured number of subagents concurrently.
func TestDefaultPlanStepWave_HonorsMaxParallelSubagents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			steps = 6
			limit = 2
		)
		probe := &concurrencyProbe{entered: make(chan chan struct{}, 6), abort: make(chan struct{})}
		l := newLimitTestLauncher(probe, limit)

		ready := make([]orchestration.PlanStep, steps)
		for i := range ready {
			ready[i] = orchestration.PlanStep{ID: fmt.Sprintf("s%d", i), Summary: "s", Description: "d"}
		}

		var outcomes []planStepOutcome
		driveLimitWaves(t, probe, steps, limit, func() {
			outcomes = l.defaultPlanStepWave(limitTestCtx(), ready, tools.NewDelegationRegistry())
		})
		if len(outcomes) != steps {
			t.Fatalf("got %d outcomes, want %d", len(outcomes), steps)
		}
		for _, o := range outcomes {
			if o.err != nil {
				t.Fatalf("step %s failed to launch: %v", o.stepID, o.err)
			}
		}
		if got := probe.maxActive.Load(); got != limit {
			t.Errorf("plan wave ran %d subagents concurrently, want exactly %d (cap must bind)", got, limit)
		}
	})
}

// TestRunRegularBlocking_HonorsMaxParallelSubagents proves the delegate path
// honors the SAME cap: a batch of blocking delegations must not run more than
// the configured number of subagents concurrently. Together with the plan-wave
// test this covers the shared single-limit requirement for both batch call
// kinds.
func TestRunRegularBlocking_HonorsMaxParallelSubagents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			n     = 6
			limit = 2
		)
		probe := &concurrencyProbe{entered: make(chan chan struct{}, 6), abort: make(chan struct{})}
		l := newLimitTestLauncher(probe, limit)

		tasks := make([]tools.DelegationTask, n)
		for i := range tasks {
			tasks[i] = tools.DelegationTask{ID: fmt.Sprintf("d%d", i), Summary: "s", Task: "t"}
		}

		var results []tools.DelegationResult
		driveLimitWaves(t, probe, n, limit, func() {
			results = l.runRegularBlocking(limitTestCtx(), tasks, tools.NewDelegationRegistry())
		})
		if len(results) != n {
			t.Fatalf("got %d results, want %d", len(results), n)
		}
		for _, r := range results {
			if r.Status == tools.DelegationStatusFailed {
				t.Fatalf("delegation %s failed to launch: %v", r.ID, r.Error)
			}
		}
		if got := probe.maxActive.Load(); got != limit {
			t.Errorf("delegate blocking ran %d subagents concurrently, want exactly %d (cap must bind)", got, limit)
		}
	})
}

// TestLaunchAsync_HonorsMaxParallelSubagents proves the async delegate path
// honors the SAME cap. runWave dispatches mode:"async" tasks to launchAsync,
// which runs each on its own background goroutine — without the shared limiter
// a single delegate call (up to maxDelegationBatchSize tasks) would start them
// all at once. The cap must bind there too, so this asserts the async fan-out
// never exceeds it.
func TestLaunchAsync_HonorsMaxParallelSubagents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			n     = 6
			limit = 2
		)
		probe := &concurrencyProbe{entered: make(chan chan struct{}, 6), abort: make(chan struct{})}
		l := newLimitTestLauncher(probe, limit)

		registry := tools.NewDelegationRegistry()
		tasks := make([]tools.DelegationTask, n)
		for i := range tasks {
			id := fmt.Sprintf("a%d", i)
			tasks[i] = tools.DelegationTask{ID: id, Summary: "s", Task: "t", Mode: "async"}
			if err := registry.Register(id, "s", nil, "async"); err != nil {
				t.Fatalf("register %s: %v", id, err)
			}
		}

		// Launch returns as soon as every async task is dispatched (each reports
		// "running"); the subagents keep running in the background.
		var results []tools.DelegationResult
		driveLimitWaves(t, probe, n, limit, func() {
			results = l.Launch(limitTestCtx(), tasks, registry)
		})
		if len(results) != n {
			t.Fatalf("got %d results, want %d", len(results), n)
		}
		for _, r := range results {
			if r.Status != tools.DelegationStatusRunning {
				t.Fatalf("async delegation %s status = %v, want running", r.ID, r.Status)
			}
		}

		if pending := registry.ListPending(); len(pending) > 0 {
			t.Fatalf("async delegations pending after worker drain: %v", pending)
		}

		if got := probe.maxActive.Load(); got != limit {
			t.Errorf("delegate async ran %d subagents concurrently, want exactly %d (cap must bind)", got, limit)
		}
	})
}
