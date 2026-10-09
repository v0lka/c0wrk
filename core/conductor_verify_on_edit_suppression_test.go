package core

import (
	"context"
	"sync/atomic"
	"testing"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

// #7 regression: verify-on-edit must be suppressed for the subagent executors
// the delegation launcher builds during a specialized pass
// (deps.systemPromptOverride != nil). conductorLauncher holds deps by VALUE,
// so the suppression must nil deps.verifyOnEdit BEFORE the launcher is
// constructed — a suppression after the copy disarmed only the main executor
// while launcher.deps.verifyOnEdit stayed armed, and every delegated subagent
// executed the user-configured verification command.

// countingVerifyRunner is a spy agent.EditVerifyRunner recording invocations.
type countingVerifyRunner struct {
	calls atomic.Int32
}

func (c *countingVerifyRunner) Run(ctx context.Context) agent.EditVerifyResult {
	c.calls.Add(1)
	return agent.EditVerifyResult{}
}

// specializedPromptFactory is any non-nil SystemPromptFactory — its mere
// presence is what marks the run as a specialized pass.
func specializedPromptFactory(ctx context.Context, stepDescription string, modelMeta llm.ModelMetadata) string {
	return "specialized system prompt"
}

// scriptVerifyOnEditDelegation returns the LLM script for one run: the
// conductor delegates once, the subagent performs a successful write_file
// (the verify-on-edit trigger), both finish.
func scriptVerifyOnEditDelegation() *pauseScriptLLM {
	return &pauseScriptLLM{script: []pauseScriptStep{
		{respond: assistantToolCall("c1", "delegate", `{"tasks":[{"id":"del_1","summary":"edit a file","task":"edit the file"}]}`)},
		{respond: assistantToolCall("w1", "write_file", `{"path":"x.txt","content":"hi"}`)},
		{respond: executorFinishResponse("del_1 done")},
		{respond: executorFinishResponse("all done")},
	}}
}

// TestRunConductor_SpecializedPass_SubagentExecutorHasNoVerifyOnEdit: during a
// specialized pass the delegated subagent's successful write_file must NOT run
// the verification hook.
func TestRunConductor_SpecializedPass_SubagentExecutorHasNoVerifyOnEdit(t *testing.T) {
	spy := &countingVerifyRunner{}
	caller := scriptVerifyOnEditDelegation()
	emitter := &launchRecorder{}
	rec := &cmRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, rec)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)

	registry := createTestRegistry()
	registry.Register(coretools.NewDelegateTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-voe-suppression", recStore)
	bb.SetOriginalRequest("specialized pass")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)

	deps := o.buildConductorDeps(nil, nil)
	deps.verifyOnEdit = spy.Run
	deps.verifyOnEditMaxOutputChars = 200
	deps.systemPromptOverride = specializedPromptFactory

	if _, err := RunConductor(ctx, "specialized pass", bb, availableTools, deps, plansDir); err != nil {
		t.Fatalf("RunConductor failed: %v", err)
	}

	if got := spy.calls.Load(); got != 0 {
		t.Fatalf("verify-on-edit ran %d time(s) during a specialized pass — the launcher-built subagent executor is still armed (suppression must nil deps.verifyOnEdit BEFORE the launcher copy)", got)
	}
}

// TestRunConductor_OrdinaryPass_SubagentExecutorRunsVerifyOnEdit is the
// control: WITHOUT a system-prompt override the hook must fire exactly once
// for the subagent's write_file — proving the harness above can observe the
// hook and the suppression test is not passing vacuously.
func TestRunConductor_OrdinaryPass_SubagentExecutorRunsVerifyOnEdit(t *testing.T) {
	spy := &countingVerifyRunner{}
	caller := scriptVerifyOnEditDelegation()
	emitter := &launchRecorder{}
	rec := &cmRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, rec)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)

	registry := createTestRegistry()
	registry.Register(coretools.NewDelegateTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-voe-control", recStore)
	bb.SetOriginalRequest("ordinary pass")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)

	deps := o.buildConductorDeps(nil, nil)
	deps.verifyOnEdit = spy.Run
	deps.verifyOnEditMaxOutputChars = 200

	if _, err := RunConductor(ctx, "ordinary pass", bb, availableTools, deps, plansDir); err != nil {
		t.Fatalf("RunConductor failed: %v", err)
	}

	if got := spy.calls.Load(); got != 1 {
		t.Fatalf("expected the subagent's write_file to run verify-on-edit exactly once in an ordinary pass, got %d", got)
	}
}
