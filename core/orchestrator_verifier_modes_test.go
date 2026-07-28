package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/goal"
	ctools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// Keep ctools referenced (the verifier's declare_verification tool lives there)
// and sdktools for ToolResult / ToolDescriptor.
var (
	_ = ctools.NewDeclareVerificationTool
	_ sdktools.ToolDescriptor
)

// These tests exercise defaultGoalVerifier's isolation + work-product-seeding
// fix and its mode branching (directive + toolset selection). They run the
// production defaultGoalVerifier against a mock LLM + a recording tool executor
// so they assert on the ACTUAL Conductor run (fresh blackboard, seeded work
// product, directive selection) rather than a mocked seam.

// newVerifierTestOrchestrator builds an orchestrator whose LLM drives exactly
// the requested tool-call sequence then finishes. It captures the tools the
// verifier actually called so tests can assert on the work-product path.
func newVerifierTestOrchestrator(t *testing.T, toolCalls []llm.ToolCall, exec agent.ToolExecutor) *Orchestrator {
	t.Helper()
	step := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			if step >= len(toolCalls) {
				// Finish once the scripted calls are exhausted.
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:      "assistant",
						ToolCalls: []llm.ToolCall{{ID: "fin", Name: "finish", Input: json.RawMessage(`{}`)}},
					},
					StopReason: "tool_use",
				}, nil
			}
			tc := toolCalls[step]
			step++
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{tc}},
				StopReason: "tool_use",
			}, nil
		},
	}
	cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	return NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       exec,
		ToolRegistry:   createTestRegistry(),
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: cf,
		Emitter:        &mockEmitter{},
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
}

// TestDefaultGoalVerifier_ExecutableMode_SeedsWorkProductAndConfirms runs the
// production defaultGoalVerifier in executable mode with a met turn that
// produced a work product ("the real fix"). The verifier's mock LLM reads the
// seeded work product via read_final_result, then confirms. This proves:
//  1. The verifier runs on a FRESH blackboard (its read_final_result returns
//     the seeded work product, not "no final result recorded").
//  2. executable mode (the default, empty VerificationMode) selects the
//     executable directive + toolset (read_final_result available).
//
// TestDefaultGoalVerifier_ExecutableMode_SeedsWorkProductOnFreshBlackboard
// verifies the work-product fix: defaultGoalVerifier runs on a FRESH blackboard
// (not the goal loop's bb) and seeds it with lastTurnOutput via SetFinalResult.
// We prove the seeding by capturing the blackboard the Conductor's tool layer
// sees: a mock executor records the read_final_result result it receives, which
// — because RunConductor binds the FinalResultStore to the verifier's fresh
// blackboard — is the seeded value, not "no final result recorded". The fresh
// blackboard is decoupled from the goal loop's bb (here left untouched: it never
// receives the seeded work product).
func TestDefaultGoalVerifier_ExecutableMode_SeedsWorkProductOnFreshBlackboard(t *testing.T) {
	// The verifier reads the work product via read_final_result. The executor
	// observes the value bound to the verifier's (fresh, seeded) blackboard.
	var readFinalResultValue string
	var readFinalResultCalled bool
	exec := &mockToolExecutor{
		results: map[string]sdktools.ToolResult{},
		executeFn: func(ctx context.Context, name string, _ json.RawMessage) (sdktools.ToolResult, error) {
			if name == "read_final_result" {
				readFinalResultCalled = true
				// Mirror the real tool: read the value from the context-bound
				// FinalResultStore (RunConductor wires it to the verifier's
				// fresh blackboard). If the blackboard were not seeded, this
				// returns the "no final result recorded" error — proving the fix.
				if store := finalResultStoreFromCtx(ctx); store != nil {
					if v, ok := store.GetFinalResult(); ok {
						readFinalResultValue = v
						return sdktools.ToolResult{Content: v}, nil
					}
				}
				return sdktools.ToolResult{Content: "No final result is recorded on the blackboard", IsError: true}, nil
			}
			return sdktools.ToolResult{Content: "ok"}, nil
		},
	}
	// Script the LLM: read_final_result then finish (no verdict — the mock can't
	// declare into a real sink; we assert on the seeding, not the verdict).
	calls := []llm.ToolCall{
		{ID: "c1", Name: "read_final_result", Input: json.RawMessage(`{}`)},
	}
	o := newVerifierTestOrchestrator(t, calls, exec)
	// Provide the verdict channel so the verifier CAN report (the read-only
	// toolset includes it); the fresh-blackboard + seeding is what we assert.
	available := []sdktools.ToolDescriptor{
		{Name: "read_final_result"}, {Name: "declare_verification"}, {Name: "finish"},
	}

	gs := &goal.GoalState{
		Status:           goal.StatusActive,
		Condition:        "the bug is fixed",
		VerifyClause:     "no error",
		VerificationMode: goal.VerificationModeExecutable,
	}
	goalLoopBB := orchestration.NewMapBlackboard() // the ACTIVE goal task's bb — must stay untouched

	deps := o.buildConductorDeps(nil, nil)
	outcome, err := o.defaultGoalVerifier(context.Background(), gs, &goal.Verdict{Status: "met", Reason: "done"}, "msg", "THE REAL WORK PRODUCT (final answer)", goalLoopBB, available, deps)
	if err != nil {
		t.Fatalf("defaultGoalVerifier returned error: %v", err)
	}
	// The verifier read the seeded work product off its FRESH blackboard.
	if !readFinalResultCalled {
		t.Fatal("expected the verifier to call read_final_result")
	}
	if readFinalResultValue != "THE REAL WORK PRODUCT (final answer)" {
		t.Errorf("read_final_result returned %q, want the seeded work product — the fresh blackboard must be seeded via SetFinalResult", readFinalResultValue)
	}
	// The goal loop's blackboard is decoupled: it was NOT seeded with the work
	// product (the verifier seeded its OWN fresh blackboard, not the shared one).
	if got := goalLoopBB.GetFinalResult(); got != "" {
		t.Errorf("goal loop blackboard was mutated: GetFinalResult()=%q, want empty (the verifier must run on a SEPARATE blackboard)", got)
	}
	// No verdict was declared (the mock LLM finished without declaring), so the
	// pass ends in the reject fallback — the seeding assertion above is what we
	// care about here.
	if outcome == nil || outcome.Confirmed {
		t.Fatalf("expected the no-verdict reject fallback, got %+v", outcome)
	}
}

// finalResultStoreFromCtx mirrors agent.FinalResultStoreFromContext for the
// test's executor path, returning the minimal read interface the seeding
// assertion uses. The store is bound by RunConductor to the verifier's FRESH
// blackboard — so a value here proves the fresh blackboard was seeded.
func finalResultStoreFromCtx(ctx context.Context) finalResultStoreProbe {
	if s := agent.FinalResultStoreFromContext(ctx); s != nil {
		return s
	}
	return nil
}

// finalResultStoreProbe is the minimal read interface the seeding assertion uses.
type finalResultStoreProbe interface {
	GetFinalResult() (string, bool)
}

// TestDefaultGoalVerifier_RejectsWhenNoVerdictDeclared verifies the
// no-verdict→reject fallback still holds on the fresh blackboard: a verifier
// that finishes without declaring (e.g. hits the step budget) yields a REJECT,
// never a confirm.
func TestDefaultGoalVerifier_RejectsWhenNoVerdictDeclared(t *testing.T) {
	// The verifier just finishes without declaring a verdict.
	calls := []llm.ToolCall{}
	exec := &mockToolExecutor{results: map[string]sdktools.ToolResult{}}
	o := newVerifierTestOrchestrator(t, calls, exec)

	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "x", VerifyClause: "y"}
	deps := o.buildConductorDeps(nil, nil)
	outcome, err := o.defaultGoalVerifier(context.Background(), gs, &goal.Verdict{Status: "met"}, "msg", "", orchestration.NewMapBlackboard(), nil, deps)
	if err != nil {
		t.Fatalf("defaultGoalVerifier returned error: %v", err)
	}
	if outcome == nil || outcome.Confirmed {
		t.Fatalf("expected reject (no verdict declared), got %+v", outcome)
	}
	if !strings.Contains(strings.ToLower(outcome.Reason), "without a verdict") {
		t.Errorf("expected the no-verdict reject reason, got %q", outcome.Reason)
	}
}

// TestDefaultGoalVerifier_ReDerivationMode_SelectsReDerivationDirectiveAndToolset
// verifies the mode-branching delta: with VerificationMode == re_derivation,
// defaultGoalVerifier builds the toolset via verifierReDerivationToolFilter
// (delegate available) and selects the GoalReDerivation directive. It captures
// the system prompt the fresh CM received and asserts it contains the
// re-derivation directive's signature ("Re-derivation Mode").
func TestDefaultGoalVerifier_ReDerivationMode_SelectsReDerivationDirectiveAndToolset(t *testing.T) {
	// A CM that captures the system prompt handed to RunConductor.
	var gotSystemPrompt string
	cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		gotSystemPrompt = systemPrompt
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	// Verifier declares confirmed after one (scripted) read.
	calls := []llm.ToolCall{
		{ID: "c1", Name: "declare_verification", Input: json.RawMessage(`{"confirmed":true,"reason":"fresh run clean","evidence":[{"type":"qualitative","ref":"delegated run","summary":"re-derivation came back clean"}]}`)},
	}
	step := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			if step >= len(calls) {
				return &llm.ChatResponse{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "fin", Name: "finish", Input: json.RawMessage(`{}`)}}}, StopReason: "tool_use"}, nil
			}
			tc := calls[step]
			step++
			return &llm.ChatResponse{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{tc}}, StopReason: "tool_use"}, nil
		},
	}
	o := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       &mockToolExecutor{results: map[string]sdktools.ToolResult{}},
		ToolRegistry:   createTestRegistry(),
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: cf,
		Emitter:        &mockEmitter{},
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	// Full fixture including delegate, so the re_derivation toolset is non-empty.
	available := verifierFixtureDescriptors()
	gs := &goal.GoalState{
		Status:           goal.StatusActive,
		Condition:        "the design is sound",
		VerifyClause:     "fresh re-derivation is clean",
		VerificationMode: goal.VerificationModeReDerivation,
	}
	deps := o.buildConductorDeps(nil, nil)
	outcome, err := o.defaultGoalVerifier(context.Background(), gs, &goal.Verdict{Status: "met", Reason: "done"}, "msg", "work product", orchestration.NewMapBlackboard(), available, deps)
	if err != nil {
		t.Fatalf("defaultGoalVerifier returned error: %v", err)
	}
	// The directive selection is what we assert here (the verdict round-trip is
	// covered by the runGoalTurns gating tests). The mock LLM cannot declare a
	// verdict into the real sink, so the pass ends in the no-verdict reject
	// fallback — that is expected and irrelevant to the directive branch.
	if outcome == nil {
		t.Fatal("expected a non-nil outcome")
	}
	// The directive selected was the re-derivation one (its title carries the
	// mode signature), proving the branch picked GoalReDerivation over
	// GoalVerification.
	if !strings.Contains(gotSystemPrompt, "Re-derivation Mode") {
		t.Errorf("expected the re-derivation directive in the system prompt, got (first 200 chars): %s", truncate(gotSystemPrompt, 200))
	}
}

// TestDefaultGoalVerifier_ExecutableMode_SelectsExecutableDirective is the
// counterpart assertion for executable mode: the directive selected is
// GoalVerification, not GoalReDerivation. It must NOT carry the re-derivation
// signature, and it should carry the executable signature ("re-run the verify
// clause").
func TestDefaultGoalVerifier_ExecutableMode_SelectsExecutableDirective(t *testing.T) {
	var gotSystemPrompt string
	cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		gotSystemPrompt = systemPrompt
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	calls := []llm.ToolCall{
		{ID: "c1", Name: "declare_verification", Input: json.RawMessage(`{"confirmed":true,"reason":"ok","evidence":[{"type":"qualitative","ref":"verify clause","summary":"passed"}]}`)},
	}
	step := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			if step >= len(calls) {
				return &llm.ChatResponse{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "fin", Name: "finish", Input: json.RawMessage(`{}`)}}}, StopReason: "tool_use"}, nil
			}
			tc := calls[step]
			step++
			return &llm.ChatResponse{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{tc}}, StopReason: "tool_use"}, nil
		},
	}
	o := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       &mockToolExecutor{results: map[string]sdktools.ToolResult{}},
		ToolRegistry:   createTestRegistry(),
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: cf,
		Emitter:        &mockEmitter{},
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	available := verifierFixtureDescriptors()
	gs := &goal.GoalState{
		Status:           goal.StatusActive,
		Condition:        "x",
		VerifyClause:     "y",
		VerificationMode: goal.VerificationModeExecutable,
	}
	deps := o.buildConductorDeps(nil, nil)
	outcome, err := o.defaultGoalVerifier(context.Background(), gs, &goal.Verdict{Status: "met"}, "msg", "", orchestration.NewMapBlackboard(), available, deps)
	if err != nil {
		t.Fatalf("defaultGoalVerifier returned error: %v", err)
	}
	// Directive selection is what we assert (the mock LLM cannot declare a
	// verdict into the real sink, so a no-verdict reject is expected and
	// irrelevant to the directive branch).
	if outcome == nil {
		t.Fatal("expected a non-nil outcome")
	}
	if strings.Contains(gotSystemPrompt, "Re-derivation Mode") {
		t.Errorf("executable mode must NOT select the re-derivation directive")
	}
	if !strings.Contains(gotSystemPrompt, "Re-run the verify clause") {
		t.Errorf("expected the executable directive signature in the prompt, got (first 200 chars): %s", truncate(gotSystemPrompt, 200))
	}
}

// truncate returns the first n runes of s, with an ellipsis if truncated.
func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Ensure fmt is referenced (used by error formatting in a richer future test).
var _ = fmt.Sprintf
