package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// opRecorder records the relative order of Conductor-triggered context
// operations (trajectory seeding, compaction, prompt building) and LLM calls
// behind a mutex, so tests can assert compaction ran BEFORE the first LLM
// call of the resumed run.
type opRecorder struct {
	mu  sync.Mutex
	ops []string
}

func (r *opRecorder) record(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, op)
}

func (r *opRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}

func (r *opRecorder) count(op string) int {
	n := 0
	for _, o := range r.snapshot() {
		if o == op {
			n++
		}
	}
	return n
}

// indexOf returns the position of the first occurrence of op in the recorded
// sequence, or -1 when absent.
func (r *opRecorder) indexOf(op string) int {
	for i, o := range r.snapshot() {
		if o == op {
			return i
		}
	}
	return -1
}

// resumeCompactionCM is a seedable ContextManager mock whose SeedSteps,
// Compact, and BuildPrompt calls are recorded (in order) into a shared
// opRecorder. Compact returns compactResult so the Conductor emits a real
// ContextCompaction event (nil would model the no-op contract).
type resumeCompactionCM struct {
	mockContextManager
	rec           *opRecorder
	compactResult *agent.CompactionResult
}

func (m *resumeCompactionCM) SeedSteps(steps []agent.Step) {
	m.rec.record("seed")
	m.steps = append([]agent.Step(nil), steps...)
}

func (m *resumeCompactionCM) BuildPrompt() []llm.Message {
	m.rec.record("build_prompt")
	return m.mockContextManager.BuildPrompt()
}

func (m *resumeCompactionCM) Compact(_ context.Context) *agent.CompactionResult {
	m.rec.record("compact")
	return m.compactResult
}

// newResumeCompactionOrchestrator wires an orchestrator whose context factory
// captures the compaction strategy STRING the Conductor run was configured
// with (into strategyOut) and the created CM (into cmOut). The LLM mock
// records every call into the same opRecorder before answering, so the test
// can assert the relative order of compaction vs LLM calls.
func newResumeCompactionOrchestrator(t *testing.T, rec *opRecorder, emitter *spyEmitter, cmOut **resumeCompactionCM, strategyOut *string) *Orchestrator {
	t.Helper()
	registry := createTestRegistry()
	cf := func(systemPrompt string, _ llm.ModelMetadata, strategy string, _ ...orchestration.PruningOverride) ContextManager {
		cm := &resumeCompactionCM{
			mockContextManager: mockContextManager{systemPrompt: systemPrompt},
			rec:                rec,
			compactResult:      &agent.CompactionResult{BeforePercent: 88.5, AfterPercent: 21.25},
		}
		if cmOut != nil {
			*cmOut = cm
		}
		if strategyOut != nil {
			*strategyOut = strategy
		}
		return cm
	}
	mockLLM := &mockLLMCaller{callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
		rec.record("llm")
		return executorFinishResponse("resumed after compaction"), nil
	}}
	return NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: cf,
		Emitter:        emitter,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
}

// TestResume_RequestedCompaction_CompactsBeforeFirstLLMCall is the acceptance
// test for the one-shot resume-compaction flag: with the flag armed via
// RequestResumeCompaction, the resumed Conductor run compacts the merged
// trajectory (seeded prior steps) BEFORE its first LLM call — the recorded
// operation order is seed → compact → build_prompt → llm — and the run's
// compaction strategy is the user-selected one, not the routing-derived
// default. The flag is consumed by that single Resume.
func TestResume_RequestedCompaction_CompactsBeforeFirstLLMCall(t *testing.T) {
	rec := &opRecorder{}
	emitter := &spyEmitter{}
	var cm *resumeCompactionCM
	var strategy string
	orch := newResumeCompactionOrchestrator(t, rec, emitter, &cm, &strategy)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("long running task")

	const userStrategy = "hierarchical"
	orch.RequestResumeCompaction(userStrategy)

	steps := resumeStepsFixture(3)
	result, err := orch.Resume(context.Background(), bb, nil, "", steps, nil, "")
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if result.Output != "resumed after compaction" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if cm == nil {
		t.Fatal("context manager was never created")
	}

	// Exactly one compaction pass across the whole run, and it happened after
	// the trajectory was seeded but BEFORE the first prompt build / LLM call —
	// i.e. the compaction covered the merged trajectory and the first LLM call
	// already saw the compacted context.
	if got := rec.count("compact"); got != 1 {
		t.Fatalf("Compact called %d times, want exactly 1", got)
	}
	seedIdx, compactIdx, promptIdx, llmIdx := rec.indexOf("seed"), rec.indexOf("compact"), rec.indexOf("build_prompt"), rec.indexOf("llm")
	if compactIdx < 0 || promptIdx < 0 || llmIdx < 0 {
		t.Fatalf("missing expected operations in sequence %v", rec.snapshot())
	}
	if compactIdx >= promptIdx || compactIdx >= llmIdx {
		t.Fatalf("compaction must precede the first prompt build and LLM call, got %v", rec.snapshot())
	}
	if seedIdx >= 0 && seedIdx > compactIdx {
		t.Fatalf("compaction must follow trajectory seeding, got %v", rec.snapshot())
	}

	// The run's compaction strategy is the user-selected one (it reaches the
	// ContextManager factory as a string from the Conductor's run wiring).
	if strategy != userStrategy {
		t.Fatalf("run compaction strategy = %q, want %q", strategy, userStrategy)
	}

	// The forced compaction surfaced as exactly one ContextCompaction event
	// carrying the CompactionResult's real before/after percentages.
	compactions := 0
	for _, c := range emitter.calls {
		if c.method == "ContextCompaction" {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatalf("ContextCompaction emitted %d times, want exactly 1", compactions)
	}

	// One-shot: the armed flag was consumed by this Resume.
	if got := orch.consumeResumeCompaction(); got != "" {
		t.Fatalf("flag not consumed by Resume; second consume returned %q", got)
	}
}

// TestResume_WithoutRequestedCompaction_NoStartCompaction verifies the
// default resume behavior: without the flag armed, no start-of-run compaction
// happens — the first recorded operation is the prompt build for the first
// LLM call (threshold-driven compaction remains the only compaction path).
func TestResume_WithoutRequestedCompaction_NoStartCompaction(t *testing.T) {
	rec := &opRecorder{}
	emitter := &spyEmitter{}
	orch := newResumeCompactionOrchestrator(t, rec, emitter, nil, nil)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("plain resume")

	steps := resumeStepsFixture(2)
	if _, err := orch.Resume(context.Background(), bb, nil, "", steps, nil, ""); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if got := rec.count("compact"); got != 0 {
		t.Fatalf("Compact called %d times without an armed request, want 0", got)
	}
	// With a non-empty resumeSteps the trajectory seeding legitimately runs
	// first; what must NOT appear is any compaction before the prompt build.
	ops := rec.snapshot()
	if len(ops) < 2 || ops[0] != "seed" || ops[1] != "build_prompt" {
		t.Fatalf("expected [seed build_prompt ...] with no compaction, got %v", ops)
	}
	compactions := 0
	for _, c := range emitter.calls {
		if c.method == "ContextCompaction" {
			compactions++
		}
	}
	if compactions != 0 {
		t.Fatalf("ContextCompaction emitted %d times without an armed request, want 0", compactions)
	}
}

// TestRequestResumeCompaction_OneShotSemantics unit-tests the arm/consume
// pair directly: consuming clears the flag (a second consume returns ""),
// arming with an empty strategy is a no-op, and re-arming before consumption
// overwrites the pending strategy.
func TestRequestResumeCompaction_OneShotSemantics(t *testing.T) {
	orch := newResumeCompactionOrchestrator(t, &opRecorder{}, &spyEmitter{}, nil, nil)

	if got := orch.consumeResumeCompaction(); got != "" {
		t.Fatalf("fresh orchestrator consume = %q, want empty", got)
	}

	orch.RequestResumeCompaction("summarization")
	if got := orch.consumeResumeCompaction(); got != "summarization" {
		t.Fatalf("first consume = %q, want summarization", got)
	}
	if got := orch.consumeResumeCompaction(); got != "" {
		t.Fatalf("second consume = %q, want empty (one-shot)", got)
	}

	// Empty strategy is the "not armed" sentinel — arming with it is a no-op.
	orch.RequestResumeCompaction("")
	if got := orch.consumeResumeCompaction(); got != "" {
		t.Fatalf("consume after empty arm = %q, want empty", got)
	}

	// Re-arming before consumption overwrites the pending strategy.
	orch.RequestResumeCompaction("sliding_window")
	orch.RequestResumeCompaction("hierarchical")
	if got := orch.consumeResumeCompaction(); got != "hierarchical" {
		t.Fatalf("consume after re-arm = %q, want hierarchical (latest wins)", got)
	}

	// Clearing drops an armed request without consuming it: the session
	// layer's cancel/abandon paths (task discarded, goal takeover,
	// archival) use this so an armed flag cannot fire for an unrelated
	// later task resuming on the same orchestrator.
	orch.RequestResumeCompaction("summarization")
	orch.ClearResumeCompaction()
	if got := orch.consumeResumeCompaction(); got != "" {
		t.Fatalf("consume after clear = %q, want empty", got)
	}

	// Clearing when nothing is armed is a harmless no-op.
	orch.ClearResumeCompaction()
	if got := orch.consumeResumeCompaction(); got != "" {
		t.Fatalf("consume after clear-nothing = %q, want empty", got)
	}
}

// twoTurnDepsRecorder is a goal-loop turn runner that records the deps of
// every turn and drives the loop for exactly two turns: a "not_met" verdict
// on the first (loop continues) and a "met" verdict (with evidence) on the
// second (loop exits).
type twoTurnDepsRecorder struct {
	mu       sync.Mutex
	turnDeps []conductorDeps
	turnNums []int
}

func (r *twoTurnDepsRecorder) run(
	ctx context.Context,
	turn int,
	_ string,
	_ orchestration.Blackboard,
	_ []sdktools.ToolDescriptor,
	_ string,
	_ []llm.Message,
	deps conductorDeps,
) (int, *orchestration.ExecutionResult, error) {
	r.mu.Lock()
	r.turnDeps = append(r.turnDeps, deps)
	r.turnNums = append(r.turnNums, turn)
	first := len(r.turnNums) == 1
	r.mu.Unlock()

	if sink := tools.GoalStatusSinkFrom(ctx); sink != nil {
		if first {
			sink.Declare(goal.Verdict{Status: "not_met", Reason: "still working", DeclaredAt: time.Now()})
		} else {
			sink.Declare(*metVerdict("goal met"))
		}
	}
	return 2, &orchestration.ExecutionResult{}, nil
}

func (r *twoTurnDepsRecorder) depsPerTurn() []conductorDeps {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]conductorDeps(nil), r.turnDeps...)
}

// TestResumeGoalLoop_ForcedCompactionFirstTurnOnly verifies the goal-loop
// resume branch threads the consumed strategy into the FIRST resumed turn's
// conductor deps only (one-shot, mirroring the nudge): that is the turn whose
// ContextManager carries the seeded prior trajectory, so its start-of-run
// compaction covers the merged trajectory; the second turn runs without the
// override.
func TestResumeGoalLoop_ForcedCompactionFirstTurnOnly(t *testing.T) {
	o := newGoalTestOrchestrator()
	recorder := &twoTurnDepsRecorder{}
	o.goalTurnRunner = recorder.run

	gs := &goal.GoalState{
		Condition: "ship the feature",
		Status:    goal.StatusActive,
		Budget:    goal.GoalBudget{MaxTurns: 10},
		TurnCount: 1,
		CreatedAt: time.Now(),
	}
	bb := orchestration.NewMapBlackboard()
	routing := &router.RoutingDecision{Domain: "general", Complexity: 3}

	seedSteps := resumeStepsFixture(2)
	if _, err := o.resumeGoalLoop(
		context.Background(), "resume the goal", bb, nil, "", routing, gs, seedSteps, "", "summarization",
	); err != nil {
		t.Fatalf("resumeGoalLoop failed: %v", err)
	}

	deps := recorder.depsPerTurn()
	if len(deps) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(deps))
	}
	if deps[0].forceCompactionStrategy != "summarization" {
		t.Fatalf("turn 1 forceCompactionStrategy = %q, want summarization", deps[0].forceCompactionStrategy)
	}
	if deps[1].forceCompactionStrategy != "" {
		t.Fatalf("turn 2 forceCompactionStrategy = %q, want empty (first turn only)", deps[1].forceCompactionStrategy)
	}
	if len(deps[0].resumeSteps) != len(seedSteps) {
		t.Fatalf("turn 1 resumeSteps = %d, want %d (trajectory seeded on the compacted turn)", len(deps[0].resumeSteps), len(seedSteps))
	}
}
