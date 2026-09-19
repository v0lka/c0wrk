package core

// Regression test for the auto-resume wave's context parity: the wave
// relaunches paused subagents DIRECTLY through the launcher, bypassing
// orchestration.Conductor.Run — the sole place the mainline executor context
// gains the blackboard-backed stores. Before resumePausedWork injected them
// itself, a relaunched subagent ran with a nil FactStore, so store_fact /
// search_facts returned "Fact store not available".

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// factProbeCaller records, per LLM call, whether the call context carries a
// FactStore. The executor passes the run ctx to every LLM call, so this is a
// faithful probe of what the agent would see in store_fact.
type factProbeCaller struct {
	inner agent.LLMCaller

	mu        sync.Mutex
	withFS    int
	withoutFS int
}

func (p *factProbeCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	has := agent.FactStoreFromContext(ctx) != nil
	p.mu.Lock()
	if has {
		p.withFS++
	} else {
		p.withoutFS++
	}
	p.mu.Unlock()
	return p.inner.Call(ctx, req)
}

// TestResumeWave_SubagentsCarryFactStore: a paused blocking delegation is
// relaunched by the resume wave; every LLM call in that wave (both the
// conductor's and the relaunched subagent's) must see a FactStore.
func TestResumeWave_SubagentsCarryFactStore(t *testing.T) {
	script := &pauseScriptLLM{script: []pauseScriptStep{
		// Run 1: delegate del_1.
		{respond: assistantToolCall("c1", "delegate", `{"tasks":[{"id":"del_1","summary":"do the thing","task":"do the thing carefully"}]}`)},
		// del_1's subagent: gated tool call; pause armed while blocked.
		{respond: assistantToolCall("g1", "bash_exec", `{"command":"echo part1","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		// Wave: del_1 continues from its checkpoint and finishes.
		{respond: executorFinishResponse("del_1 done")},
		// The resumed conductor's ONLY LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}
	probe := &factProbeCaller{inner: script}

	emitter := &launchRecorder{}
	rec := &cmRecorder{}
	o := newWaveOrchestrator(t, probe, emitter, rec)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)

	registry := createTestRegistry()
	registry.Register(coretools.NewDelegateTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-factstore", recStore)
	bb.SetOriginalRequest("delegate the thing")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)

	// --- Run 1: pause mid-del_1 ---
	clearSignal := o.installPauseSignal()
	deps1 := o.buildConductorDeps(nil, nil)
	outCh := make(chan error, 1)
	go func() {
		_, err := RunConductor(ctx, "delegate the thing", bb, availableTools, deps1, plansDir)
		outCh <- err
	}()
	gated := script.script[1]
	select {
	case <-gated.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the del_1 subagent's gated call")
	}
	o.PauseSession()
	close(gated.gate)
	select {
	case err := <-outCh:
		if !errors.Is(err, agent.ErrPaused) {
			t.Fatalf("run 1 error = %v, want ErrPaused", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for run 1 to pause")
	}
	clearSignal()

	bb.specs = recStore.recorded()

	// Reset the probe: only calls made during Resume matter.
	probe.mu.Lock()
	probe.withFS, probe.withoutFS = 0, 0
	probe.mu.Unlock()

	// --- Resume: wave relaunches del_1, then ONE conductor call ---
	res, err := o.Resume(ctx, bb, nil, plansDir, nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}

	probe.mu.Lock()
	withFS, withoutFS := probe.withFS, probe.withoutFS
	probe.mu.Unlock()

	t.Logf("Resume LLM calls: with FactStore=%d, without FactStore=%d", withFS, withoutFS)

	if withoutFS != 0 {
		t.Errorf("%d LLM call(s) during Resume ran WITHOUT a FactStore — "+
			"store_fact/search_facts would return \"Fact store not available\"", withoutFS)
	}
	// The wave's relaunched subagent call AND the conductor call must both
	// carry the store.
	if withFS < 2 {
		t.Errorf("LLM calls WITH a FactStore during Resume = %d, want >= 2 "+
			"(conductor + relaunched subagent)", withFS)
	}
}
