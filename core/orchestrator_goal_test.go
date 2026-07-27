package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// mockProposer is a test double for tools.GoalProposer. It records the
// proposal it received and returns a preset response (and optional error),
// simulating the desktop approval flow without any real I/O.
type mockProposer struct {
	response tools.GoalProposalResponse
	err      error
	got      tools.GoalProposal
	calls    int
}

func (m *mockProposer) Propose(_ context.Context, p tools.GoalProposal) (tools.GoalProposalResponse, error) {
	m.got = p
	m.calls++
	return m.response, m.err
}

func TestCapturingProposer_CapturesProposalAndReturnsEditedResponse(t *testing.T) {
	// The mock simulates a user who edited both fields on approval.
	mock := &mockProposer{
		response: tools.GoalProposalResponse{
			Decision:  "approve",
			Condition: "all goal-package tests pass with -race",
			Verify:    "go test -race ./core/goal/...",
		},
	}
	capturer := &capturingProposer{delegate: mock}

	// Simulate the agent proposing its original wording.
	original := tools.GoalProposal{
		Condition: "improve the goal system",
		Verify:    "tests pass",
	}
	resp, err := capturer.Propose(context.Background(), original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The delegate (mock) received the agent's ORIGINAL proposal.
	if mock.got.Condition != original.Condition || mock.got.Verify != original.Verify {
		t.Errorf("delegate did not receive the agent's proposal: got %+v, want %+v", mock.got, original)
	}
	if mock.calls != 1 {
		t.Errorf("expected delegate called once, got %d", mock.calls)
	}

	// The caller (propose_goal tool) received the user's EDITED response.
	if resp.Condition != mock.response.Condition || resp.Verify != mock.response.Verify {
		t.Errorf("caller did not receive the edited response: got %+v", resp)
	}

	// The capturer recorded both for post-run reconstruction.
	gotProposal, gotResponse, captured := capturer.outcome()
	if !captured {
		t.Fatal("expected proposal to be captured")
	}
	if gotProposal.Condition != original.Condition {
		t.Errorf("captured proposal condition = %q, want %q", gotProposal.Condition, original.Condition)
	}
	if gotResponse.Decision != "approve" || gotResponse.Condition != mock.response.Condition {
		t.Errorf("captured response = %+v, want edited approve", gotResponse)
	}
}

func TestBuildGoalState_ApproveWithEditsUsesEditedValues(t *testing.T) {
	proposal := tools.GoalProposal{
		Condition: "original condition",
		Verify:    "original verify",
	}
	resp := tools.GoalProposalResponse{
		Decision:  "approve",
		Condition: "edited condition",
		Verify:    "edited verify",
	}
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	gs, err := buildGoalState(proposal, resp, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gs.Condition != "edited condition" {
		t.Errorf("Condition = %q, want edited %q", gs.Condition, "edited condition")
	}
	if gs.VerifyClause != "edited verify" {
		t.Errorf("VerifyClause = %q, want edited %q", gs.VerifyClause, "edited verify")
	}
	if gs.Status != goal.StatusActive {
		t.Errorf("Status = %q, want %q", gs.Status, goal.StatusActive)
	}
	if gs.Budget.IsUnlimited() == false {
		t.Error("expected unlimited budget at derivation time")
	}
	if !gs.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", gs.CreatedAt, now)
	}
}

func TestBuildGoalState_ApproveWithoutEditsFallsBackToProposal(t *testing.T) {
	// When the user approves without editing, the response carries empty
	// condition/verify and the GoalState must fall back to the agent's wording.
	proposal := tools.GoalProposal{
		Condition: "agent condition",
		Verify:    "agent verify",
	}
	resp := tools.GoalProposalResponse{Decision: "approve"}

	gs, err := buildGoalState(proposal, resp, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gs.Condition != "agent condition" {
		t.Errorf("Condition = %q, want proposal fallback %q", gs.Condition, "agent condition")
	}
	if gs.VerifyClause != "agent verify" {
		t.Errorf("VerifyClause = %q, want proposal fallback %q", gs.VerifyClause, "agent verify")
	}
}

func TestBuildGoalState_CancelReturnsError(t *testing.T) {
	proposal := tools.GoalProposal{Condition: "c", Verify: "v"}
	resp := tools.GoalProposalResponse{Decision: "cancel"}

	gs, err := buildGoalState(proposal, resp, time.Now())
	if err == nil {
		t.Fatalf("expected error for cancel, got GoalState %+v", gs)
	}
	if gs != nil {
		t.Errorf("expected nil GoalState on cancel, got %+v", gs)
	}
}

func TestBuildGoalState_ClarifyWithoutApprovalReturnsError(t *testing.T) {
	proposal := tools.GoalProposal{
		Condition:          "c",
		Verify:             "v",
		NeedsClarification: true,
		Clarification:      "which scope?",
	}
	resp := tools.GoalProposalResponse{Decision: "clarify", Clarification: "the core package"}

	_, err := buildGoalState(proposal, resp, time.Now())
	if err == nil {
		t.Fatal("expected error for clarify-without-approval")
	}
}

func TestBuildGoalState_UnknownDecisionReturnsError(t *testing.T) {
	proposal := tools.GoalProposal{Condition: "c", Verify: "v"}
	resp := tools.GoalProposalResponse{Decision: "bogus"}

	_, err := buildGoalState(proposal, resp, time.Now())
	if err == nil {
		t.Fatal("expected error for unknown decision")
	}
}

// TestDeriveGoal_MockProposerCapturesAndReturnsEditedGoal exercises the full
// capture + reconstruction path end-to-end (without the conductor): a mock
// proposer stands in for the desktop approval flow, the capturingProposer
// records the agent's proposal, and buildGoalState turns the edited approve
// response into a GoalState. This verifies the acceptance criterion: "a unit
// test with a mock proposer verifies the proposal is captured and an edited
// response is returned."
func TestDeriveGoal_MockProposerCapturesAndReturnsEditedGoal(t *testing.T) {
	mock := &mockProposer{
		response: tools.GoalProposalResponse{
			Decision:  "approve",
			Condition: "the derived goal ships green",
			Verify:    "go test ./core/goal/...",
		},
	}
	capturer := &capturingProposer{delegate: mock}

	// Simulate the agent calling propose_goal after investigation.
	agentProposal := tools.GoalProposal{
		Condition: "make the goal system work",
		Verify:    "tests pass",
	}
	if _, err := capturer.Propose(context.Background(), agentProposal); err != nil {
		t.Fatalf("unexpected proposer error: %v", err)
	}

	// Reconstruct the goal the way deriveGoal does after the conductor run.
	proposal, response, captured := capturer.outcome()
	if !captured {
		t.Fatal("deriveGoal expected the agent to have called propose_goal")
	}
	gs, err := buildGoalState(proposal, response, time.Now())
	if err != nil {
		t.Fatalf("buildGoalState: %v", err)
	}

	// The proposal was captured verbatim (the agent's original wording).
	if proposal.Condition != agentProposal.Condition {
		t.Errorf("captured proposal condition = %q, want %q", proposal.Condition, agentProposal.Condition)
	}
	// The returned GoalState reflects the user's EDITED values.
	if gs.Condition != mock.response.Condition {
		t.Errorf("GoalState.Condition = %q, want edited %q", gs.Condition, mock.response.Condition)
	}
	if gs.VerifyClause != mock.response.Verify {
		t.Errorf("GoalState.VerifyClause = %q, want edited %q", gs.VerifyClause, mock.response.Verify)
	}
}

func TestDeriveGoal_NilProposerReturnsError(t *testing.T) {
	o := &Orchestrator{}
	_, err := o.deriveGoal(context.Background(), "msg", nil, nil, conductorDeps{})
	if err == nil {
		t.Fatal("expected error when deps.goalProposer is nil")
	}
}

func TestEnsureProposeGoalTool_AddsWhenMissing(t *testing.T) {
	registry := sdktools.NewToolRegistry()
	registry.Register(tools.NewProposeGoalTool())
	base := []sdktools.ToolDescriptor{{Name: "read_file"}, {Name: "finish"}}
	got := ensureProposeGoalTool(base, registry)
	if len(got) != len(base)+1 {
		t.Fatalf("expected %d tools, got %d", len(base)+1, len(got))
	}
	var found bool
	for _, td := range got {
		if td.Name == "propose_goal" {
			found = true
			if td.Description == "" {
				t.Error("propose_goal descriptor has empty description")
			}
		}
	}
	if !found {
		t.Error("propose_goal was not appended")
	}
}

func TestEnsureProposeGoalTool_NoopWhenPresent(t *testing.T) {
	base := []sdktools.ToolDescriptor{{Name: "read_file"}, {Name: "propose_goal"}}
	got := ensureProposeGoalTool(base, sdktools.NewToolRegistry())
	if len(got) != len(base) {
		t.Fatalf("expected no change (%d), got %d", len(base), len(got))
	}
}

func TestEnsureProposeGoalTool_NoopWhenNotRegistered(t *testing.T) {
	base := []sdktools.ToolDescriptor{{Name: "read_file"}}
	// Empty registry (propose_goal not registered): list returned unchanged.
	got := ensureProposeGoalTool(base, sdktools.NewToolRegistry())
	if len(got) != len(base) {
		t.Fatalf("expected no change when tool unregistered (%d), got %d", len(base), len(got))
	}
}

func TestCapturingProposer_DelegateErrorIsPropagated(t *testing.T) {
	wantErr := errors.New("approval channel closed")
	mock := &mockProposer{err: wantErr}
	capturer := &capturingProposer{delegate: mock}

	_, err := capturer.Propose(context.Background(), tools.GoalProposal{Condition: "c", Verify: "v"})
	if !errors.Is(err, wantErr) {
		t.Errorf("expected delegate error propagated, got %v", err)
	}
}

// ----------------------------------------------------------------------------
// runGoalTurns unit tests
// ----------------------------------------------------------------------------

// newGoalTestOrchestrator builds a minimal Orchestrator wired with a mock
// emitter so the loop's ServiceWithMeta event emissions don't panic. All other
// fields are zero — the mock turn runner never touches the real Conductor
// stack, so LLM/tool/context dependencies are not needed.
//
// Verification is set to "off" so these turn-mechanics tests reproduce today's
// behavior exactly (a "met" verdict terminates immediately, no verifier
// invoked). Tests that exercise the verification gate use
// newVerificationTestOrchestrator (orchestrator_verification_gating_test.go),
// which enables "independent" mode and injects a verifier.
func newGoalTestOrchestrator() *Orchestrator {
	return &Orchestrator{
		emitter: &mockEmitter{},
		config: OrchestratorConfig{
			GoalLoop: GoalLoopSettings{Verification: "off"},
		},
	}
}

// mockGoalTurnRunner is a configurable stand-in for the Conductor turn. It
// simulates the agent's behaviour per turn: optionally making tool calls
// (reported as toolCallCount) and/or declaring a verdict via the context sink.
// The turnVerds/turnCalls slices are indexed by turn (1-based), defaulting to a
// no-op idle turn when exhausted.
type mockGoalTurnRunner struct {
	turnVerds []*goal.Verdict // verdict to declare on turn N (1-based)
	turnCalls []int           // tool-call count to report on turn N (1-based)
	calls     int
}

func (m *mockGoalTurnRunner) run(
	ctx context.Context,
	turn int,
	_ string,
	_ orchestration.Blackboard,
	_ []sdktools.ToolDescriptor,
	_ string,
	_ []llm.Message,
	_ conductorDeps,
) (int, *orchestration.ExecutionResult, error) {
	m.calls++
	// Report the configured tool-call count (default 0 = idle).
	toolCalls := 0
	if turn-1 < len(m.turnCalls) {
		toolCalls = m.turnCalls[turn-1]
	}
	// Declare the configured verdict into the sink, if any.
	if turn-1 < len(m.turnVerds) && m.turnVerds[turn-1] != nil {
		if sink := tools.GoalStatusSinkFrom(ctx); sink != nil {
			sink.Declare(*m.turnVerds[turn-1])
		}
	}
	return toolCalls, &orchestration.ExecutionResult{}, nil
}

// metVerdict builds a valid "met" verdict with one piece of evidence.
func metVerdict(reason string) *goal.Verdict {
	return &goal.Verdict{
		Status:     "met",
		Reason:     reason,
		DeclaredAt: time.Now(),
		Evidence:   []goal.GoalEvidence{{Type: goal.EvidenceTypeFile, Ref: "main.go", Summary: "implemented"}},
	}
}

// TestRunGoalTurns_MetVerdictExitsAfterTurn1 verifies the core acceptance
// criterion: when a mock turn returns a "met" verdict (with evidence), the loop
// exits after turn 1 with Status == met.
func TestRunGoalTurns_MetVerdictExitsAfterTurn1(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done")},
		turnCalls: []int{2},
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(
		context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run,
	)

	if result.Status != goal.StatusMet {
		t.Fatalf("Status = %q, want %q", result.Status, goal.StatusMet)
	}
	if runner.calls != 1 {
		t.Errorf("expected exactly 1 turn, got %d", runner.calls)
	}
	if result.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", result.TurnCount)
	}
	if result.LastVerdict == nil || result.LastVerdict.Status != "met" {
		t.Errorf("LastVerdict = %+v, want met", result.LastVerdict)
	}
}

// TestRunGoalTurns_ZeroToolTurnBlockedIdle verifies the anti-spin criterion:
// a turn that makes ZERO tool calls AND declares no verdict transitions to
// blocked_idle.
func TestRunGoalTurns_ZeroToolTurnBlockedIdle(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{
		turnCalls: []int{0}, // idle: no tools, no verdict
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "stuck"}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(
		context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run,
	)

	if result.Status != goal.StatusBlockedIdle {
		t.Fatalf("Status = %q, want %q", result.Status, goal.StatusBlockedIdle)
	}
	if runner.calls != 1 {
		t.Errorf("expected exactly 1 turn before halt, got %d", runner.calls)
	}
}

// TestRunGoalTurns_NotMetThenMet verifies the loop continues across turns: a
// "not_met" verdict (with tool calls) does not terminate, and a subsequent
// "met" verdict does.
func TestRunGoalTurns_NotMetThenMet(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{
			{Status: "not_met", Reason: "still working", DeclaredAt: time.Now()},
			metVerdict("now done"),
		},
		turnCalls: []int{3, 2},
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "iterate"}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(
		context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run,
	)

	if result.Status != goal.StatusMet {
		t.Fatalf("Status = %q, want %q", result.Status, goal.StatusMet)
	}
	if runner.calls != 2 {
		t.Errorf("expected 2 turns, got %d", runner.calls)
	}
	if result.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", result.TurnCount)
	}
}

// TestRunGoalTurns_TurnBudgetExhausted verifies the budget criterion: when the
// turn count reaches MaxTurns without a met verdict, the loop halts exhausted.
func TestRunGoalTurns_TurnBudgetExhausted(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{
			{Status: "not_met", DeclaredAt: time.Now()},
			{Status: "not_met", DeclaredAt: time.Now()},
		},
		turnCalls: []int{1, 1},
	}
	gs := &goal.GoalState{
		Status: goal.StatusActive,
		Budget: goal.GoalBudget{MaxTurns: 2},
	}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(
		context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run,
	)

	if result.Status != goal.StatusExhausted {
		t.Fatalf("Status = %q, want %q", result.Status, goal.StatusExhausted)
	}
	if result.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", result.TurnCount)
	}
}

// TestRunGoalTurns_PauseSignalBreaks verifies the pause criterion: when the
// pause signal is set, the loop transitions to paused at the top of the next
// turn.
func TestRunGoalTurns_PauseSignalBreaks(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{
		turnCalls: []int{1}, // turn 1 makes progress (so it doesn't halt idle)
	}
	gs := &goal.GoalState{Status: goal.StatusActive}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	// Run one iteration manually by setting pause BEFORE the loop's second
	// top-of-turn check. Turn 1 runs (makes 1 tool call, no verdict → would
	// continue). Set pause so turn 2's top-of-turn check halts.
	// We pre-set the pause signal so the FIRST top-of-turn check halts before
	// any turn runs — this isolates the pause path from the turn logic.
	pause.Store(true)

	result := o.runGoalTurns(
		context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run,
	)

	if result.Status != goal.StatusPaused {
		t.Fatalf("Status = %q, want %q", result.Status, goal.StatusPaused)
	}
	if runner.calls != 0 {
		t.Errorf("expected 0 turns (paused before turn 1), got %d", runner.calls)
	}
	if result.TurnCount != 0 {
		t.Errorf("TurnCount = %d, want 0", result.TurnCount)
	}
}

// TestRunGoalTurns_AgentBlockedVerdict verifies that an explicit "blocked"
// verdict transitions to blocked_idle immediately (distinct from the anti-spin
// idle path).
func TestRunGoalTurns_AgentBlockedVerdict(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{{Status: "blocked", Reason: "need user input", DeclaredAt: time.Now()}},
		turnCalls: []int{2},
	}
	gs := &goal.GoalState{Status: goal.StatusActive}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(
		context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run,
	)

	if result.Status != goal.StatusBlockedIdle {
		t.Fatalf("Status = %q, want %q", result.Status, goal.StatusBlockedIdle)
	}
	if result.LastVerdict == nil || result.LastVerdict.Status != "blocked" {
		t.Errorf("LastVerdict = %+v, want blocked", result.LastVerdict)
	}
}

// TestResolveGoalBudget verifies the turn-only resolution: a non-zero
// override sets MaxTurns; nil or a zero MaxTurns means unlimited.
func TestResolveGoalBudget(t *testing.T) {
	t.Run("nil override is unlimited", func(t *testing.T) {
		got := resolveGoalBudget(nil)
		if !got.IsUnlimited() {
			t.Errorf("got %+v, want unlimited", got)
		}
	})
	t.Run("override wins for set turns", func(t *testing.T) {
		ovr := &goal.GoalBudget{MaxTurns: 2}
		got := resolveGoalBudget(ovr)
		if got.MaxTurns != 2 {
			t.Errorf("MaxTurns = %d, want 2 (overridden)", got.MaxTurns)
		}
	})
	t.Run("zero override is unlimited", func(t *testing.T) {
		ovr := &goal.GoalBudget{MaxTurns: 0}
		got := resolveGoalBudget(ovr)
		if !got.IsUnlimited() {
			t.Errorf("got %+v, want unlimited (zero override)", got)
		}
	})
}

// TestDetectAndStripGoalMode verifies the /goal prefix detection used by the
// backend to plumb the Goal flag.
func TestDetectAndStripGoalMode(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantMsg  string
		wantGoal bool
	}{
		{"plain goal command", "/goal make tests pass", "make tests pass", true},
		{"goal at end", "/goal", "", true},
		{"no goal prefix", "make tests pass", "make tests pass", false},
		{"does not match goals-report", "/goals-report run", "/goals-report run", false},
		// Note: the backend preprocesses (trims) the message before detection,
		// so /goal must be at the start; leading whitespace is not matched.
		{"leading whitespace not matched", "  /goal do x", "  /goal do x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// DetectAndStripGoalMode matches against the raw text; trim for
			// the leading-spaces case consistency.
			gotMsg, gotGoal := DetectAndStripGoalMode(tc.input)
			if gotGoal != tc.wantGoal {
				t.Errorf("isGoal = %v, want %v", gotGoal, tc.wantGoal)
			}
			if gotMsg != tc.wantMsg {
				t.Errorf("cleaned = %q, want %q", gotMsg, tc.wantMsg)
			}
		})
	}
}

// TestCountingToolExec_Counts verifies the anti-spin counting wrapper tallies
// Execute calls.
func TestCountingToolExec_Counts(t *testing.T) {
	noop := &noopToolExec{}
	counter := &turnUsageCounter{}
	wrapped := &countingToolExec{inner: noop, counter: counter}
	for i := 0; i < 3; i++ {
		if _, err := wrapped.Execute(context.Background(), "read_file", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if counter.toolCalls != 3 {
		t.Errorf("toolCalls = %d, want 3", counter.toolCalls)
	}
}

// noopToolExec is a minimal ToolExecutor for testing the counting wrapper.
type noopToolExec struct{}

func (n *noopToolExec) Execute(_ context.Context, _ string, _ json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{Content: "ok"}, nil
}
func (n *noopToolExec) GetToolSource(_ string) string { return "test" }
func (n *noopToolExec) IsToolUntrusted(_ string) bool { return false }
func (n *noopToolExec) CacheStrategy(_ context.Context, _ string, _ json.RawMessage) sdktools.CacheMode {
	return sdktools.CacheModeDefault
}

// TestMemGoalStatusSink verifies the verdict sink round-trips a verdict and
// reports nil before the first declaration.
func TestMemGoalStatusSink(t *testing.T) {
	s := &memGoalStatusSink{}
	if s.Last() != nil {
		t.Fatal("expected nil before any declaration")
	}
	v := goal.Verdict{Status: "met", Reason: "r", DeclaredAt: time.Now()}
	s.Declare(v)
	got := s.Last()
	if got == nil || got.Status != "met" {
		t.Errorf("Last() = %+v, want met", got)
	}
}

// ----------------------------------------------------------------------------
// resumeGoalLoop unit tests (acceptance criterion #3: a paused goal resumes
// into the goal loop with executor steps re-seeded)
// ----------------------------------------------------------------------------

// goalSeedRecorder is a turn runner that captures the deps it receives so the
// test can assert the resumed trajectory was seeded into the executor on turn 1.
// It declares a "met" verdict on turn 1 so the loop exits after one turn.
type goalSeedRecorder struct {
	mu        sync.Mutex
	turns     int
	turn1Deps conductorDeps
}

func (r *goalSeedRecorder) run(
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
	r.turns++
	// Capture the deps of the FIRST resumed turn. The turn counter continues
	// from gs.TurnCount (so the first resumed turn is not necessarily 1); a
	// once-capture handles that regardless of the absolute turn number.
	if r.turns == 1 {
		r.turn1Deps = deps
	}
	r.mu.Unlock()
	// Declare a "met" verdict so the loop exits after this turn.
	if sink := tools.GoalStatusSinkFrom(ctx); sink != nil {
		sink.Declare(*metVerdict("resumed goal met"))
	}
	return 2, &orchestration.ExecutionResult{}, nil
}

// TestResumeGoalLoop_PausedResumesAndSeedsSteps is the acceptance test: a
// paused GoalState is re-activated, the goal loop re-enters (turn runner
// called), and the prior trajectory (resumeSteps) is seeded into the executor
// deps on turn 1.
func TestResumeGoalLoop_PausedResumesAndSeedsSteps(t *testing.T) {
	o := newGoalTestOrchestrator()
	recorder := &goalSeedRecorder{}
	o.goalTurnRunner = recorder.run

	seedSteps := []agent.Step{
		{Thought: "prior turn 1", Observation: "did something"},
		{Thought: "prior turn 2", Observation: "did more"},
	}
	pausedGS := &goal.GoalState{
		Condition:    "ship the feature",
		VerifyClause: "go test ./...",
		Budget:       goal.GoalBudget{MaxTurns: 10},
		TurnCount:    2,
		Status:       goal.StatusPaused,
		CreatedAt:    time.Now(),
	}
	bb := orchestration.NewMapBlackboard()
	routing := &router.RoutingDecision{Domain: "general", Complexity: 3}

	result, err := o.resumeGoalLoop(
		context.Background(), "resume the goal", bb, nil, "", routing, pausedGS, seedSteps,
	)
	if err != nil {
		t.Fatalf("resumeGoalLoop failed: %v", err)
	}

	// The loop re-entered (turn runner called exactly once before the met verdict).
	if recorder.turns != 1 {
		t.Fatalf("expected turn runner called once, got %d", recorder.turns)
	}

	// The resumed trajectory was seeded into turn 1's executor deps.
	recorder.mu.Lock()
	gotDeps := recorder.turn1Deps
	recorder.mu.Unlock()
	if len(gotDeps.resumeSteps) != len(seedSteps) {
		t.Fatalf("expected %d seeded resumeSteps on turn 1, got %d", len(seedSteps), len(gotDeps.resumeSteps))
	}
	if gotDeps.resumeSteps[0].Thought != seedSteps[0].Thought {
		t.Errorf("seeded step[0].Thought = %q, want %q", gotDeps.resumeSteps[0].Thought, seedSteps[0].Thought)
	}

	// The paused goal was re-activated and then marked met by the verdict.
	if result.Status != orchestration.ExecutionStatusSuccess {
		t.Errorf("result status = %q, want success", result.Status)
	}
}

// TestResumeGoalLoop_ActiveGoalResumes verifies a still-active (non-paused)
// goal also re-enters the loop — both non-terminal statuses resume.
func TestResumeGoalLoop_ActiveGoalResumes(t *testing.T) {
	o := newGoalTestOrchestrator()
	recorder := &goalSeedRecorder{}
	o.goalTurnRunner = recorder.run

	activeGS := &goal.GoalState{
		Condition: "active goal",
		Status:    goal.StatusActive,
		CreatedAt: time.Now(),
	}
	bb := orchestration.NewMapBlackboard()
	routing := &router.RoutingDecision{Domain: "general", Complexity: 3}

	result, err := o.resumeGoalLoop(
		context.Background(), "continue", bb, nil, "", routing, activeGS, nil,
	)
	if err != nil {
		t.Fatalf("resumeGoalLoop failed: %v", err)
	}
	if recorder.turns != 1 {
		t.Fatalf("expected turn runner called once for active goal, got %d", recorder.turns)
	}
	if result.Status != orchestration.ExecutionStatusSuccess {
		t.Errorf("result status = %q, want success", result.Status)
	}
}

// TestResume_DelegatesToGoalLoopForPausedGoal verifies that Resume threads a
// non-terminal goal state into resumeGoalLoop (the public Resume path that
// ResumeTask calls), rather than the plain Conductor path. With a mock turn
// runner installed, the goal loop runs without the full Conductor stack.
func TestResume_DelegatesToGoalLoopForPausedGoal(t *testing.T) {
	o := newGoalTestOrchestrator()
	recorder := &goalSeedRecorder{}
	o.goalTurnRunner = recorder.run
	// A tool registry is required because Resume computes availableTools before
	// branching; an empty registry is fine since the mock runner ignores tools.
	o.toolRegistry = sdktools.NewToolRegistry()

	seedSteps := []agent.Step{{Thought: "checkpoint", Observation: "prior"}}
	pausedGS := &goal.GoalState{
		Condition: "goal via resume",
		Status:    goal.StatusPaused,
		CreatedAt: time.Now(),
	}
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("resume the paused goal")

	result, err := o.Resume(
		context.Background(), bb, nil, "", seedSteps, pausedGS,
	)
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	// The goal loop ran (turn runner invoked) and the verdict marked it met.
	if recorder.turns != 1 {
		t.Fatalf("expected goal loop turn runner called once, got %d (Resume may have taken the plain Conductor path)", recorder.turns)
	}
	recorder.mu.Lock()
	gotDeps := recorder.turn1Deps
	recorder.mu.Unlock()
	if len(gotDeps.resumeSteps) != len(seedSteps) {
		t.Fatalf("expected resumeSteps seeded into turn 1, got %d", len(gotDeps.resumeSteps))
	}
	if result.Status != orchestration.ExecutionStatusSuccess {
		t.Errorf("result status = %q, want success", result.Status)
	}
}

// TestResume_FallsThroughForTerminalGoal verifies that a terminal goal state
// (e.g. met) does NOT re-enter the goal loop — Resume falls through to the
// normal Conductor path. The key assertion is that the goal turn runner is
// never called.
func TestResume_FallsThroughForTerminalGoal(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("already met"),
	}}
	emitter := &spyEmitter{}
	o := newResumeTestOrchestrator(t, mockLLM, emitter, nil)
	recorder := &goalSeedRecorder{}
	o.goalTurnRunner = recorder.run

	metGS := &goal.GoalState{Condition: "already done", Status: goal.StatusMet, CreatedAt: time.Now()}
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("a finished goal task")

	// A terminal goal must NOT enter the goal loop; Resume falls through to the
	// plain Conductor path (which finishes via the mock LLM).
	if _, err := o.Resume(context.Background(), bb, nil, "", nil, metGS); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if recorder.turns != 0 {
		t.Fatalf("expected goal loop NOT entered for terminal goal, but turn runner called %d times", recorder.turns)
	}
}

// ----------------------------------------------------------------------------
// defaultGoalTurnRunner regression tests (Issue #1: anti-spin count read order)
// ----------------------------------------------------------------------------

// TestDefaultGoalTurnRunner_ReadsCountAfterRun is the regression test for the
// tool-call count read-order bug. The original implementation read
// ce.counter.toolCalls BEFORE calling RunConductor; since runGoalTurns installs
// a fresh turnUsageCounter (starting at zero) each turn and countingToolExec
// only increments it DURING the run, the pre-run read always yielded 0 — making
// the anti-spin safety net non-functional in production. The fix reads the
// count AFTER RunConductor returns.
//
// This test runs the REAL defaultGoalTurnRunner over a real Conductor stack
// (mock LLM makes one tool call, then finishes) and asserts the returned count
// reflects the tool call made during the run (1), proving the count is read
// post-run, not pre-run.
func TestDefaultGoalTurnRunner_ReadsCountAfterRun(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Executor turn 1: make one tool call (bash_exec)
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:    "c1",
							Name:  "bash_exec",
							Input: json.RawMessage(`{"command":"echo hi","timeout":"5s"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			case 2: // Executor turn 2: finish
				return executorFinishResponse("done"), nil
			default:
				return executorFinishResponse("done"), nil
			}
		},
	}
	emitter := &spyEmitter{}
	o := newResumeTestOrchestrator(t, mockLLM, emitter, nil)

	// Build the deps the way runGoalTurns does: buildConductorDeps then wrap
	// the tool executor in a countingToolExec backed by a fresh counter.
	counter := &turnUsageCounter{}
	deps := o.buildConductorDeps(nil, nil)
	deps.toolExec = &countingToolExec{inner: deps.toolExec, counter: counter}

	// Provide the bash_exec tool descriptor the LLM will call. The finish tool
	// is injected by the executor itself.
	availableTools := []sdktools.ToolDescriptor{
		{Name: "bash_exec", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`), Source: "test"},
	}

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("goal task")
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 3)

	toolCalls, _, err := o.defaultGoalTurnRunner(
		ctx, 1, "goal task", bb, availableTools, "", nil, deps,
	)
	if err != nil {
		t.Fatalf("defaultGoalTurnRunner returned error: %v", err)
	}

	// Regression assertion: the count is read AFTER the run, so it reflects
	// the one tool call the conductor made (1), not the pre-run zero. Before
	// the fix this would have been 0 — making the anti-spin check fire on the
	// first turn and misclassifying a productive turn as blocked_idle.
	if toolCalls != 1 {
		t.Errorf("toolCalls = %d, want 1 (count must be read AFTER the run; pre-run read bug would yield 0)", toolCalls)
	}
	// Sanity: the counter was actually incremented during the run.
	if counter.toolCalls != 1 {
		t.Errorf("counter.toolCalls = %d, want 1 (the conductor should have made one tool call)", counter.toolCalls)
	}
}

// TestRunGoalTurns_LoopReadsPostRunCount is the loop-level companion to the
// above: a turn runner that increments the loop-installed counter DURING its
// execution (mimicking what the real Conductor does via countingToolExec) and
// reports the post-run count. The loop must NOT misclassify this productive
// turn as blocked_idle. This guards the read-order contract at the integration
// seam where the loop feeds the anti-spin check.
func TestRunGoalTurns_LoopReadsPostRunCount(t *testing.T) {
	o := newGoalTestOrchestrator()

	// A turn runner that faithfully mimics defaultGoalTurnRunner's contract:
	// it performs work that increments the shared counter DURING the run, then
	// reads deps.toolExec.counter AFTER. The loop reads the returned count.
	countingRunner := func(
		ctx context.Context,
		_ int,
		_ string,
		_ orchestration.Blackboard,
		_ []sdktools.ToolDescriptor,
		_ string,
		_ []llm.Message,
		deps conductorDeps,
	) (int, *orchestration.ExecutionResult, error) {
		// Simulate two tool calls happening DURING the turn (what the real
		// Conductor does via countingToolExec.Execute).
		if ce, ok := deps.toolExec.(*countingToolExec); ok {
			ce.counter.toolCalls += 2
		}
		// Declare a "met" verdict so the loop exits after this turn.
		if sink := tools.GoalStatusSinkFrom(ctx); sink != nil {
			sink.Declare(*metVerdict("done with tool calls"))
		}
		// Read AFTER the simulated run — mirroring the fixed defaultGoalTurnRunner.
		toolCalls := 0
		if ce, ok := deps.toolExec.(*countingToolExec); ok {
			toolCalls = ce.counter.toolCalls
		}
		return toolCalls, &orchestration.ExecutionResult{}, nil
	}

	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(
		context.Background(), "msg", bb, nil, "", nil, gs, pause, countingRunner,
	)

	// The productive turn must be classified as MET (via the verdict), NOT
	// blocked_idle. Before the fix, a pre-run read would have seen 0 tool
	// calls; combined with no verdict read yet, the anti-spin path could have
	// fired. Here the verdict IS declared, so the primary assertion is that
	// the loop reached the met path at all.
	if result.Status != goal.StatusMet {
		t.Fatalf("Status = %q, want %q (productive turn must not be misclassified)", result.Status, goal.StatusMet)
	}
	if result.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", result.TurnCount)
	}
}

// ----------------------------------------------------------------------------
// Counting wrapper + PauseGoal + token-budget coverage tests
// ----------------------------------------------------------------------------

// TestCountingToolExec_Delegates verifies the non-Execute delegate methods pass
// through to the inner executor.
func TestCountingToolExec_Delegates(t *testing.T) {
	inner := &noopToolExec{}
	counter := &turnUsageCounter{}
	wrapped := &countingToolExec{inner: inner, counter: counter}

	if got := wrapped.GetToolSource("x"); got != "test" {
		t.Errorf("GetToolSource = %q, want test", got)
	}
	if wrapped.IsToolUntrusted("x") {
		t.Error("IsToolUntrusted = true, want false")
	}
	if got := wrapped.CacheStrategy(context.Background(), "x", nil); got != sdktools.CacheModeDefault {
		t.Errorf("CacheStrategy = %v, want CacheModeDefault", got)
	}
}

// TestPauseGoal_SignalFlipsWhenSet verifies PauseGoal flips the active pause
// signal so the running loop will suspend at the top of its next turn.
func TestPauseGoal_SignalFlipsWhenSet(t *testing.T) {
	o := newGoalTestOrchestrator()
	pause := &atomic.Bool{}
	o.activeGoalPause.Store(pause)
	t.Cleanup(func() { o.activeGoalPause.Store(nil) })

	if pause.Load() {
		t.Fatal("pause signal should start false")
	}
	o.PauseGoal()
	if !pause.Load() {
		t.Error("PauseGoal did not set the pause signal to true")
	}
}

// TestPauseGoal_NoopWithoutSignal verifies PauseGoal is a safe no-op when no
// goal loop is active (activeGoalPause is nil).
func TestPauseGoal_NoopWithoutSignal(t *testing.T) {
	o := newGoalTestOrchestrator()
	// activeGoalPause is nil — must not panic.
	o.PauseGoal()
}

// TestRunGoalTurns_HardCeilingExhausted verifies the goalLoopMaxTurns hard
// ceiling halts an otherwise-unlimited goal as exhausted.
func TestRunGoalTurns_HardCeilingExhausted(t *testing.T) {
	o := newGoalTestOrchestrator()
	// Build enough turns to reach the hard ceiling without a budget cap.
	ceiling := goalLoopMaxTurns
	verds := make([]*goal.Verdict, ceiling)
	calls := make([]int, ceiling)
	for i := range ceiling {
		// not_met verdicts with tool calls so the loop keeps iterating.
		verds[i] = &goal.Verdict{Status: "not_met", DeclaredAt: time.Now()}
		calls[i] = 1
	}
	runner := &mockGoalTurnRunner{turnVerds: verds, turnCalls: calls}
	gs := &goal.GoalState{Status: goal.StatusActive} // unlimited budget
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(
		context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run,
	)
	if result.Status != goal.StatusExhausted {
		t.Fatalf("Status = %q, want %q (hard ceiling)", result.Status, goal.StatusExhausted)
	}
	if result.TurnCount != ceiling {
		t.Errorf("TurnCount = %d, want %d (hard ceiling)", result.TurnCount, ceiling)
	}
}

// TestRunGoalTurns_HardCeilingRespectsExplicitMaxTurns is the regression test
// for Core#4: the hard turn ceiling must NOT truncate an explicit MaxTurns
// override greater than the ceiling. The override contract says an explicitly
// set MaxTurns wins, so the loop must run all of those turns.
func TestRunGoalTurns_HardCeilingRespectsExplicitMaxTurns(t *testing.T) {
	o := newGoalTestOrchestrator()
	// Explicit MaxTurns exceeds the hard ceiling (goalLoopMaxTurns).
	maxTurns := goalLoopMaxTurns + 10
	verds := make([]*goal.Verdict, maxTurns)
	calls := make([]int, maxTurns)
	for i := range maxTurns {
		verds[i] = &goal.Verdict{Status: "not_met", DeclaredAt: time.Now()}
		calls[i] = 1
	}
	runner := &mockGoalTurnRunner{turnVerds: verds, turnCalls: calls}
	gs := &goal.GoalState{Status: goal.StatusActive, Budget: goal.GoalBudget{MaxTurns: maxTurns}}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run)
	if result.Status != goal.StatusExhausted {
		t.Fatalf("Status = %q, want %q (explicit MaxTurns reached)", result.Status, goal.StatusExhausted)
	}
	if result.TurnCount != maxTurns {
		t.Errorf("TurnCount = %d, want %d (hard ceiling must not truncate explicit MaxTurns)", result.TurnCount, maxTurns)
	}
}

// TestRunGoalTurns_ContextCancelledPauses verifies that a cancelled context
// transitions the goal to paused (not an error — the loop can resume later).
func TestRunGoalTurns_ContextCancelledPauses(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{turnCalls: []int{1}}
	gs := &goal.GoalState{Status: goal.StatusActive}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	result := o.runGoalTurns(ctx, "msg", bb, nil, "", nil, gs, pause, runner.run)
	if result.Status != goal.StatusPaused {
		t.Fatalf("Status = %q, want %q (cancelled context pauses)", result.Status, goal.StatusPaused)
	}
	if runner.calls != 0 {
		t.Errorf("expected 0 turns (context cancelled before turn 1), got %d", runner.calls)
	}
}

// TestGoalLoopResult_MapsStatus verifies goalLoopResult maps goal statuses to
// execution statuses correctly.
func TestGoalLoopResult_MapsStatus(t *testing.T) {
	o := newGoalTestOrchestrator()
	bb := orchestration.NewMapBlackboard()
	cases := []struct {
		status goal.GoalStatus
		want   orchestration.ExecutionStatus
	}{
		{goal.StatusMet, orchestration.ExecutionStatusSuccess},
		{goal.StatusExhausted, orchestration.ExecutionStatusFailed},
		{goal.StatusPaused, orchestration.ExecutionStatusPartial},
		{goal.StatusBlockedIdle, orchestration.ExecutionStatusPartial},
	}
	for _, tc := range cases {
		result := o.goalLoopResult("out", bb, nil, tc.status, "cond")
		if result.Status != tc.want {
			t.Errorf("goalLoopResult(%q).Status = %q, want %q", tc.status, result.Status, tc.want)
		}
	}
}

// TestEmitGoalStatus_AllFields verifies emitGoalStatus includes verdict
// metadata when present.
func TestEmitGoalStatus_AllFields(t *testing.T) {
	emitter := &spyEmitter{}
	o := &Orchestrator{emitter: emitter}
	gs := &goal.GoalState{
		Status:      goal.StatusActive,
		TurnCount:   3,
		Condition:   "ship it",
		Budget:      goal.GoalBudget{MaxTurns: 10},
		LastVerdict: &goal.Verdict{Status: "not_met", Reason: "wip"},
	}
	o.emitGoalStatus(context.Background(), gs)
	if len(emitter.calls) != 1 {
		t.Fatalf("expected 1 emission, got %d", len(emitter.calls))
	}
	c := emitter.calls[0]
	if c.method != "ServiceWithMeta" {
		t.Errorf("method = %q, want ServiceWithMeta", c.method)
	}
}

// ----------------------------------------------------------------------------
// runGoalLoop integration regression (step_3): routing runs BEFORE derivation
// and the real routing flows through to derivation + turns.
// ----------------------------------------------------------------------------

const (
	// goalRoutingSkillMarker is embedded in the temp skill's body. Its presence
	// in the DERIVATION system prompt proves routeAndActivateSkills set
	// WithActiveSkills BEFORE deriveGoal built its prompt.
	goalRoutingSkillMarker = "ROUTING-BEFORE-DERIVATION-SKILL-MARKER-9f3a"
	// goalRoutingAgentsMarker is embedded in the workspace AGENTS.md. Its
	// presence in the derivation prompt proves buildSpecializedSystemPrompt
	// renders the shared prefix (AGENTS.md) under goal mode.
	goalRoutingAgentsMarker = "ROUTING-BEFORE-DERIVATION-AGENTS-MARKER-7c2e"
)

// goalRoutingSpyTurnRunner is a spy for the goal turn runner. It captures the
// ctx it receives (so the test can assert routing reached the turn loop) and
// declares a "met" verdict on turn 1 so runGoalTurns terminates immediately.
type goalRoutingSpyTurnRunner struct {
	mu     sync.Mutex
	calls  int
	gotCtx context.Context
}

func (s *goalRoutingSpyTurnRunner) run(
	ctx context.Context,
	_ int,
	_ string,
	_ orchestration.Blackboard,
	_ []sdktools.ToolDescriptor,
	_ string,
	_ []llm.Message,
	_ conductorDeps,
) (int, *orchestration.ExecutionResult, error) {
	s.mu.Lock()
	s.calls++
	s.gotCtx = ctx
	s.mu.Unlock()
	if sink := tools.GoalStatusSinkFrom(ctx); sink != nil {
		sink.Declare(goal.Verdict{
			Status:     "met",
			Reason:     "routing-before-derivation wiring verified end-to-end",
			DeclaredAt: time.Now(),
			Evidence: []goal.GoalEvidence{{
				Type:    goal.EvidenceTypeFile,
				Ref:     "core/orchestrator_goal.go",
				Summary: "routing precedes derivation and reaches the turn loop",
			}},
		})
	}
	// Report a non-zero tool-call count so the anti-spin guard sees a
	// productive turn (the verdict already terminates the loop, but this keeps
	// the assertion faithful to a real turn).
	return 2, &orchestration.ExecutionResult{}, nil
}

// TestRunGoalLoop_RoutingBeforeDerivation is the headline integration
// regression for the goal-mode routing fix. It wires the full
// HandleMessage → runGoalLoop path with a temp skill (distinctive body marker),
// an AGENTS.md in the workspace, a mock goal proposer (auto-approve), and a SPY
// turn runner, then drives HandleMessage with Goal=true and asserts:
//
//  1. The DERIVATION conductor's system prompt contains the active-skill marker
//     — only possible if routeAndActivateSkills (which sets WithActiveSkills)
//     ran BEFORE deriveGoal built its prompt (the core ordering fix).
//  2. The derivation prompt also contains AGENTS.md / workspace context,
//     proving buildSpecializedSystemPrompt's shared prefix renders under goal mode.
//  3. The observed routing is the router's REAL decision (domain=code,
//     complexity=4), not the old general/3 placeholder.
//  4. The spy turn runner's ctx carries domain=code + complexity=4, proving the
//     enriched ctx reached the turn loop.
//  5. The spy turn runner was called and the goal reached StatusMet, verifying
//     the wiring end-to-end (anti-spin/termination).
//
// On the OLD code (no routeAndActivateSkills in runGoalLoop; placeholder routing
// general/3), subtests 1, 3, and 4 fail: no skill marker reaches the derivation
// prompt, no Routing event is emitted, and no routing reaches the turn ctx.
func TestRunGoalLoop_RoutingBeforeDerivation(t *testing.T) {
	// --- temp workspace with AGENTS.md (shared-prefix proof) ---
	wsDir := t.TempDir()
	agentsContent := "# Project Instructions\n" + goalRoutingAgentsMarker + "\nRun go test before committing."
	if err := os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	// --- temp skill whose body carries a distinctive marker ---
	skillDir := t.TempDir()
	const skillName = "goal-routing-skill"
	skillPath := filepath.Join(skillDir, skillName)
	if err := os.MkdirAll(skillPath, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillMD := "---\nname: " + skillName + "\ndescription: Skill exercising goal-mode routing\n---\n" +
		"When activated, follow these steps. " + goalRoutingSkillMarker + "\n"
	if err := os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	sm := skills.NewSkillManager([]string{skillDir}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("skill scan: %v", err)
	}

	// --- mock LLM ---
	// call 1 = router classification; call 2 = derivation conductor turn 1
	// (propose_goal tool_use); call >= 3 = finish. The derivation system prompt
	// is captured from the call-2 ChatRequest's first (system) message.
	var derivationSystemPrompt string
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // router classification
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 4, "needs_clarification": false, "matched_skills": ["` + skillName + `"]}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // derivation conductor turn 1 → propose_goal
				if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
					derivationSystemPrompt = req.Messages[0].Content
				}
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:    "pg1",
							Name:  "propose_goal",
							Input: json.RawMessage(`{"condition":"ship the routing-before-derivation fix","verify":"go test ./core/... passes"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			default: // derivation conductor turn >= 2 → finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:    "fn1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer":"goal derived"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			}
		},
	}

	// --- registry: mock bash_exec + propose_goal (so the derivation conductor
	// can execute propose_goal; ensureProposeGoalTool also needs it registered). ---
	registry := createTestRegistry()
	registry.Register(tools.NewProposeGoalTool())

	emitter := &spyEmitter{}

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		Emitter:        emitter,
		CircuitBreaker: defaultCircuitBreakerConfig,
		SkillManager:   sm,
	})
	// Auto-approving proposer so derivation completes without real I/O.
	orchestrator.SetGoalProposer(&mockProposer{
		response: tools.GoalProposalResponse{
			Decision:  "approve",
			Condition: "ship the routing-before-derivation fix",
			Verify:    "go test ./core/... passes",
		},
	})
	spy := &goalRoutingSpyTurnRunner{}
	orchestrator.goalTurnRunner = spy.run
	// Inject a confirming verifier so the spy's met verdict passes the
	// independent-verification gate and terminates the loop on turn 1. This test
	// exercises routing/continuation wiring, not verification — a real verifier
	// would need the full Conductor stack. Confirms every met claim immediately.
	orchestrator.goalVerifier = confirmingVerifierFn

	ctx := sdktools.WithWorkspacePath(context.Background(), wsDir)
	result, err := orchestrator.HandleMessage(ctx, "achieve the routing-before-derivation goal", "session-goal-routing", HandleOptions{Goal: true})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	// --- subtests: each maps to one acceptance criterion ---

	t.Run("derivation prompt contains active-skill marker (routing ran before derivation)", func(t *testing.T) {
		if derivationSystemPrompt == "" {
			t.Fatal("derivation conductor system prompt was not captured (call 2 did not run)")
		}
		if !strings.Contains(derivationSystemPrompt, goalRoutingSkillMarker) {
			t.Errorf("derivation prompt must contain the active-skill marker %q — only possible if routeAndActivateSkills set WithActiveSkills BEFORE deriveGoal built its prompt", goalRoutingSkillMarker)
		}
	})

	t.Run("derivation prompt contains AGENTS.md/workspace shared prefix", func(t *testing.T) {
		if derivationSystemPrompt == "" {
			t.Fatal("derivation conductor system prompt was not captured")
		}
		if !strings.Contains(derivationSystemPrompt, goalRoutingAgentsMarker) {
			t.Errorf("derivation prompt must contain AGENTS.md content %q — proves buildSpecializedSystemPrompt renders the shared prefix under goal mode", goalRoutingAgentsMarker)
		}
		if !strings.Contains(derivationSystemPrompt, "## Workspace") {
			t.Error("derivation prompt should contain the shared-prefix ## Workspace section")
		}
	})

	t.Run("observed routing is the router's real decision (not placeholder)", func(t *testing.T) {
		mode, domain, complexity, ok := routingCall(emitter)
		if !ok {
			t.Fatal("no Routing event emitted — routeAndActivateSkills did not run in goal mode (old code path emitted no routing)")
		}
		if domain != "code" {
			t.Errorf("routing domain = %q (mode=%q), want code (the router's real decision, not the general placeholder)", domain, mode)
		}
		if complexity != "4" {
			t.Errorf("routing complexity = %q, want 4 (the router's real decision, not the placeholder)", complexity)
		}
	})

	t.Run("turn runner ctx carries domain and complexity", func(t *testing.T) {
		spy.mu.Lock()
		gotCtx := spy.gotCtx
		calls := spy.calls
		spy.mu.Unlock()
		if calls == 0 {
			t.Fatal("spy turn runner was never called — the goal loop did not reach runGoalTurns")
		}
		if gotCtx == nil {
			t.Fatal("spy turn runner captured a nil ctx")
		}
		if got := DomainFromContext(gotCtx); got != "code" {
			t.Errorf("DomainFromContext(turn ctx) = %q, want code", got)
		}
		if got := ComplexityFromContext(gotCtx); got != 4 {
			t.Errorf("ComplexityFromContext(turn ctx) = %d, want 4", got)
		}
	})

	t.Run("goal reached met (termination wiring end-to-end)", func(t *testing.T) {
		spy.mu.Lock()
		calls := spy.calls
		spy.mu.Unlock()
		if calls != 1 {
			t.Errorf("expected spy turn runner called exactly once (met verdict on turn 1 terminates the loop), got %d", calls)
		}
		if result == nil || result.Status != orchestration.ExecutionStatusSuccess {
			got := orchestration.ExecutionStatus("")
			if result != nil {
				got = result.Status
			}
			t.Errorf("HandleResult.Status = %q, want %q (StatusMet maps to success)", got, orchestration.ExecutionStatusSuccess)
		}
	})
}

// ----------------------------------------------------------------------------
// runGoalLoop continuation regression (step_5): goal mode activates on a
// CONTINUATION (Goal=true + TaskID != "") and the restored blackboard (with
// the prior task's inherited facts) is available to the goal loop — derivation
// runs and the turn runner sees the inherited facts.
// ----------------------------------------------------------------------------

const (
	// goalContinuationFactMarker is embedded in the restored task's facts. Its
	// presence in the spy turn runner's blackboard proves the inherited facts
	// reached the goal loop's turns.
	goalContinuationFactMarker = "INHERITED-FACT-FROM-PRIOR-TASK-2b1d"
	// goalContinuationTaskID is the restored prior task whose blackboard
	// carries the inherited fact.
	goalContinuationTaskID = "task-goal-cont-1"
)

// goalContinuationSpyTurnRunner captures the blackboard it receives so the
// test can assert the inherited facts reached the goal loop's turn runner. It
// declares a "met" verdict on turn 1 so runGoalTurns terminates immediately.
type goalContinuationSpyTurnRunner struct {
	mu    sync.Mutex
	calls int
	gotBB orchestration.Blackboard
}

func (s *goalContinuationSpyTurnRunner) run(
	ctx context.Context,
	_ int,
	_ string,
	bb orchestration.Blackboard,
	_ []sdktools.ToolDescriptor,
	_ string,
	_ []llm.Message,
	_ conductorDeps,
) (int, *orchestration.ExecutionResult, error) {
	s.mu.Lock()
	s.calls++
	s.gotBB = bb
	s.mu.Unlock()
	if sink := tools.GoalStatusSinkFrom(ctx); sink != nil {
		sink.Declare(goal.Verdict{
			Status:     "met",
			Reason:     "continuation goal loop saw the inherited blackboard",
			DeclaredAt: time.Now(),
			Evidence: []goal.GoalEvidence{{
				Type:    goal.EvidenceTypeFile,
				Ref:     "core/orchestrator_goal_test.go",
				Summary: "turn runner received the restored blackboard with inherited facts",
			}},
		})
	}
	// Report a non-zero tool-call count so the anti-spin guard sees a
	// productive turn (the verdict already terminates the loop).
	return 2, &orchestration.ExecutionResult{}, nil
}

// TestRunGoalLoop_ContinuationInheritsRestoredBlackboard verifies that goal
// mode activates on a continuation (Goal=true + TaskID != "") and the restored
// blackboard (with the prior task's facts) is available to the goal loop. It
// drives the full HandleMessage → setupBlackboard (restore) → runGoalLoop path
// with a mock task store carrying an inherited fact, an auto-approving
// proposer, and a spy turn runner, then asserts:
//
//  1. setupBlackboard restored the prior task's blackboard — a MemoryRead event
//     is emitted (setupBlackboard emits it when the restored blackboard has facts).
//  2. The goal loop ran — the spy turn runner was called (on the OLD code Goal
//     && TaskID != "" fell through to route→Conductor and the spy is never hit).
//  3. The inherited fact reached the turn runner — bb.GetFacts() contains the
//     marker (derivation + every turn inherit the SAME restored blackboard).
//  4. The goal reached StatusMet end-to-end (termination wiring).
//
// On the OLD code (gate: opts.Goal && opts.TaskID == ""), HandleMessage with
// Goal=true + TaskID != "" would fall through to the normal route→Conductor
// flow, the spy turn runner would never be called, and no goal verdict would
// be declared.
func TestRunGoalLoop_ContinuationInheritsRestoredBlackboard(t *testing.T) {
	// --- restored prior task state carrying an inherited fact ---
	mockStore := &mockTaskStore{taskState: &TaskState{
		TaskID:          goalContinuationTaskID,
		SessionID:       "session-goal-cont",
		OriginalRequest: "prior task that stored a fact",
		Status:          "completed",
		Plan: &orchestration.Plan{
			Steps: []orchestration.PlanStep{
				{ID: "step_1", Description: "prior step"},
			},
		},
		Facts: []orchestration.Fact{
			{Keywords: []string{"inherited", "fact"}, Content: goalContinuationFactMarker, Author: "step_1"},
		},
	}}

	// --- mock LLM: router (call 1), derivation propose_goal (call 2), finish (>=3) ---
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // router classification — routeOrContinue runs the full router
				// because the restored blackboard's Routing() is nil.
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // derivation conductor turn 1 → propose_goal
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:    "pg1",
							Name:  "propose_goal",
							Input: json.RawMessage(`{"condition":"continue toward the goal on the restored blackboard","verify":"turn runner sees inherited facts"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			default: // derivation conductor turn >= 2 → finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:    "fn1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer":"goal derived on continuation"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			}
		},
	}

	registry := createTestRegistry()
	registry.Register(tools.NewProposeGoalTool())

	emitter := &spyEmitter{}

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		Emitter:        emitter,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	orchestrator.SetTaskStore(mockStore)
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())
	// Auto-approving proposer so derivation completes without real I/O.
	orchestrator.SetGoalProposer(&mockProposer{
		response: tools.GoalProposalResponse{
			Decision:  "approve",
			Condition: "continue on restored blackboard",
			Verify:    "turn runner sees inherited facts",
		},
	})
	spy := &goalContinuationSpyTurnRunner{}
	orchestrator.goalTurnRunner = spy.run
	// Inject a confirming verifier so the spy's met verdict passes the
	// independent-verification gate and terminates the loop on turn 1. This test
	// exercises blackboard-restore/continuation wiring, not verification.
	orchestrator.goalVerifier = confirmingVerifierFn

	ctx := sdktools.WithWorkspacePath(context.Background(), t.TempDir())
	result, err := orchestrator.HandleMessage(ctx, "achieve the next goal", "session-goal-cont", HandleOptions{
		Goal:   true,
		TaskID: goalContinuationTaskID,
	})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	t.Run("setupBlackboard restored the prior task's facts (MemoryRead emitted)", func(t *testing.T) {
		for _, c := range emitter.calls {
			if c.method == "MemoryRead" {
				return
			}
		}
		t.Error("expected a MemoryRead event — setupBlackboard emits it when the restored blackboard has facts")
	})

	t.Run("goal loop ran (spy turn runner was called)", func(t *testing.T) {
		spy.mu.Lock()
		calls := spy.calls
		spy.mu.Unlock()
		if calls == 0 {
			t.Fatal("spy turn runner was never called — the goal loop did not run on the continuation (on old code Goal+TaskID falls through to route→Conductor)")
		}
	})

	t.Run("inherited facts reached the turn runner", func(t *testing.T) {
		spy.mu.Lock()
		gotBB := spy.gotBB
		spy.mu.Unlock()
		if gotBB == nil {
			t.Fatal("spy turn runner captured a nil blackboard")
		}
		for _, f := range gotBB.GetFacts() {
			if strings.Contains(f.Content, goalContinuationFactMarker) {
				return
			}
		}
		t.Errorf("inherited fact marker %q not found in the turn runner's blackboard facts — the restored blackboard did not reach the goal loop's turns", goalContinuationFactMarker)
	})

	t.Run("goal reached met (termination wiring end-to-end)", func(t *testing.T) {
		spy.mu.Lock()
		calls := spy.calls
		spy.mu.Unlock()
		if calls != 1 {
			t.Errorf("expected spy turn runner called exactly once (met verdict on turn 1 terminates the loop), got %d", calls)
		}
		got := orchestration.ExecutionStatus("")
		if result != nil {
			got = result.Status
		}
		if got != orchestration.ExecutionStatusSuccess {
			t.Errorf("HandleResult.Status = %q, want %q (StatusMet maps to success)", got, orchestration.ExecutionStatusSuccess)
		}
	})
}
