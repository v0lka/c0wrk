package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// concurrencyProbe is a shared LLM caller that records the peak number of
// simultaneous calls and, to make the measurement deterministic, parks each
// call until `target` calls are in flight (or a grace period elapses). Each
// subagent makes exactly one LLM call (the scripted finish terminates the
// loop), so peak in-flight calls == peak concurrent subagents.
type concurrencyProbe struct {
	target    int32
	active    int32
	maxActive int32
}

func (p *concurrencyProbe) call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	cur := atomic.AddInt32(&p.active, 1)
	for {
		prev := atomic.LoadInt32(&p.maxActive)
		if cur <= prev || atomic.CompareAndSwapInt32(&p.maxActive, prev, cur) {
			break
		}
	}
	// Park until the expected peak is reached so the observation is not racy.
	// The grace deadline keeps the run from hanging when the cap is lower than
	// target (the target is then simply unreachable).
	deadline := time.Now().Add(500 * time.Millisecond)
	for atomic.LoadInt32(&p.active) < p.target && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(5 * time.Millisecond)
	atomic.AddInt32(&p.active, -1)

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
	const (
		steps = 6
		limit = 2
	)
	probe := &concurrencyProbe{target: limit}
	l := newLimitTestLauncher(probe, limit)

	ready := make([]orchestration.PlanStep, steps)
	for i := range ready {
		ready[i] = orchestration.PlanStep{ID: fmt.Sprintf("s%d", i), Summary: "s", Description: "d"}
	}

	outcomes := l.defaultPlanStepWave(limitTestCtx(), ready, tools.NewDelegationRegistry())
	if len(outcomes) != steps {
		t.Fatalf("got %d outcomes, want %d", len(outcomes), steps)
	}
	for _, o := range outcomes {
		if o.err != nil {
			t.Fatalf("step %s failed to launch: %v", o.stepID, o.err)
		}
	}
	if got := atomic.LoadInt32(&probe.maxActive); got != limit {
		t.Errorf("plan wave ran %d subagents concurrently, want exactly %d (cap must bind)", got, limit)
	}
}

// TestRunRegularBlocking_HonorsMaxParallelSubagents proves the delegate path
// honors the SAME cap: a batch of blocking delegations must not run more than
// the configured number of subagents concurrently. Together with the plan-wave
// test this covers the shared single-limit requirement for both call kinds.
func TestRunRegularBlocking_HonorsMaxParallelSubagents(t *testing.T) {
	const (
		n     = 6
		limit = 2
	)
	probe := &concurrencyProbe{target: limit}
	l := newLimitTestLauncher(probe, limit)

	tasks := make([]tools.DelegationTask, n)
	for i := range tasks {
		tasks[i] = tools.DelegationTask{ID: fmt.Sprintf("d%d", i), Summary: "s", Task: "t"}
	}

	results := l.runRegularBlocking(limitTestCtx(), tasks, tools.NewDelegationRegistry())
	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
	for _, r := range results {
		if r.Status == tools.DelegationStatusFailed {
			t.Fatalf("delegation %s failed to launch: %v", r.ID, r.Error)
		}
	}
	if got := atomic.LoadInt32(&probe.maxActive); got != limit {
		t.Errorf("delegate blocking ran %d subagents concurrently, want exactly %d (cap must bind)", got, limit)
	}
}
