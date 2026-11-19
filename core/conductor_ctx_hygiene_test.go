package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/goal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/orchestration"
)

// --- #47 (corrected): conductorPublisher.Publish across a symlinked session
// `plans` directory ---

// TestConductorPublisher_Publish_RefusesEscapingSymlinkedPlansDir pins the
// corrected #47 contract: a PRE-EXISTING …/<sid>/plans symlink whose target
// escapes the session directory (the containment boundary) is REFUSED — the
// finding's documented plant-at-<sid>/plans trigger must fail Publish closed
// instead of writing the plan file outside ~/.c0wrk. What still resolves is
// a link INSIDE the boundary (operator intent — see the in-boundary control
// below); a dangling link or a link swapped into a created component fails
// Publish as before.
func TestConductorPublisher_Publish_RefusesEscapingSymlinkedPlansDir(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()

	plansDir := filepath.Join(parent, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans dir: %v", err)
	}
	// Replace the real plans directory with a symlink to the outside target.
	if err := os.Remove(plansDir); err != nil {
		t.Fatalf("remove plans dir: %v", err)
	}
	if err := os.Symlink(outside, plansDir); err != nil {
		t.Fatalf("symlink plans dir: %v", err)
	}

	bb := orchestration.NewMapBlackboard()
	publisher := &conductorPublisher{emitter: &mockEmitter{}, bb: bb, plansDir: plansDir, planState: &planRunState{}}

	_, err := publisher.Publish(context.Background(), []coretools.PlanTaskInput{
		{ID: "s1", Summary: "Do", Description: "Do it"},
	})
	if err == nil {
		t.Fatal("expected Publish to refuse a plans dir symlink escaping the session directory")
	}
	// The plan file must NOT exist inside the link's resolved target.
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatalf("read outside dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("the escaped link target was written to: %v", entries)
	}
	// The plan must NOT be published to the blackboard on refusal.
	if bb.GetPlan() != nil {
		t.Error("expected no plan on the blackboard after the refused write")
	}

	// Control: an in-boundary operator link still resolves as intent.
	inBoundaryTarget := filepath.Join(parent, "plans-intent")
	if err := os.MkdirAll(inBoundaryTarget, 0o755); err != nil {
		t.Fatalf("mkdir in-boundary target: %v", err)
	}
	intentDir := filepath.Join(parent, "plans-link")
	if err := os.Symlink(inBoundaryTarget, intentDir); err != nil {
		t.Fatalf("symlink in-boundary plans dir: %v", err)
	}
	bb2 := orchestration.NewMapBlackboard()
	publisher2 := &conductorPublisher{emitter: &mockEmitter{}, bb: bb2, plansDir: intentDir, planState: &planRunState{}}
	if _, err := publisher2.Publish(context.Background(), []coretools.PlanTaskInput{
		{ID: "s1", Summary: "Do", Description: "Do it"},
	}); err != nil {
		t.Fatalf("expected Publish to resolve an in-boundary symlinked plans dir, got: %v", err)
	}
	entries2, readErr := os.ReadDir(inBoundaryTarget)
	if readErr != nil || len(entries2) == 0 {
		t.Errorf("expected the plan file inside the in-boundary link's target (err=%v)", readErr)
	}
	if bb2.GetPlan() == nil {
		t.Error("expected the plan on the blackboard after the in-boundary write")
	}
}

// --- #132: a subagent executor must never sync into the parent's trajectory
// holder (the parent holder backs the persisted task checkpoint) ---

// TestSubagentCtx_DetachesTrajectoryStore: the parent's holder is replaced by
// a fresh per-subagent holder, and syncing subagent steps into it can never
// touch the parent's trajectory.
func TestSubagentCtx_DetachesTrajectoryStore(t *testing.T) {
	parent := &trajectoryHolder{}
	parent.Sync(trajSteps("p1", "p2", "p3"))

	ctx := agent.WithTrajectoryStore(context.Background(), parent)
	sub := subagentCtx(ctx)

	store := agent.TrajectoryStoreFromContext(sub)
	if store == nil {
		t.Fatal("expected the subagent ctx to carry a trajectory store")
	}
	detached, ok := store.(*trajectoryHolder)
	if !ok {
		t.Fatalf("expected a *trajectoryHolder, got %T", store)
	}
	if detached == parent {
		t.Fatal("expected the subagent ctx to carry a FRESH holder, not the parent's")
	}

	// The subagent executor syncs its own (longer) step list — with the shared
	// holder this would overwrite the parent's checkpoint and suppress the
	// parent's later syncs.
	store.Sync(trajSteps("s1", "s2", "s3", "s4", "s5"))

	if got := len(parent.Steps()); got != 3 {
		t.Errorf("parent trajectory was clobbered by the subagent sync: %d steps", got)
	}
	// And the parent's own (shorter) sync still lands afterwards.
	parent.Sync(trajSteps("p1", "p2", "p3", "p4"))
	if got := len(parent.Steps()); got != 4 {
		t.Errorf("parent sync after delegation lost: %d steps", got)
	}
}

// TestTrajectoryHolder_RejectsShorterSync: the length-monotonic guard stays as
// defense in depth — a shorter sync (a mis-wired foreign executor) must never
// erase the held trajectory.
func TestTrajectoryHolder_RejectsShorterSync(t *testing.T) {
	h := &trajectoryHolder{}
	h.Sync(trajSteps("p1", "p2", "p3"))
	h.Sync(trajSteps("p1"))
	if got := len(h.Steps()); got != 3 {
		t.Errorf("shorter sync replaced the held trajectory: %d steps", got)
	}
}

// --- #149: a re-delegating subagent's context must not carry the goal-turn
// protocol (buildSystemPrompt would name declare_goal_status — a tool stripped
// from its toolset) ---

// stubGoalStatusSink is a minimal GoalStatusSink for context-presence tests.
type stubGoalStatusSink struct{}

func (stubGoalStatusSink) Declare(v goal.Verdict) {}
func (stubGoalStatusSink) Last() *goal.Verdict    { return nil }

// stubVerificationSink is a minimal VerificationSink for context-presence tests.
type stubVerificationSink struct{}

func (stubVerificationSink) Declare(v coretools.VerificationOutcome) {}
func (stubVerificationSink) Last() *coretools.VerificationOutcome    { return nil }

// TestRedelegTaskCtx_ClearsGoalStateAndRoster: runRedelegBlocking's per-task
// context clears everything a re-delegating subagent must not inherit, while
// keeping the child delegation machinery it re-injects.
func TestRedelegTaskCtx_ClearsGoalStateAndRoster(t *testing.T) {
	l := &conductorLauncher{}

	// A goal-turn conductor context: goal state, roster, sinks, parent
	// trajectory holder — everything the goal loop stamps onto turn ctx.
	parent := &trajectoryHolder{}
	ctx := WithGoalState(context.Background(), &goal.GoalState{Condition: "ship it"})
	ctx = WithAvailableAgents(ctx, []agents.AgentDescriptor{{Name: "a"}})
	ctx = WithUserAgents(ctx, []string{"a"})
	ctx = coretools.WithGoalStatusSink(ctx, stubGoalStatusSink{})
	ctx = coretools.WithVerificationSink(ctx, stubVerificationSink{})
	ctx = agent.WithTrajectoryStore(ctx, parent)

	childReg := coretools.NewDelegationRegistryWithDepth(1)
	taskCtx := l.redelegTaskCtx(ctx, childReg)

	if gs := goalStateFromCtx(taskCtx); gs != nil {
		t.Errorf("expected goal state cleared, got condition %q — the redelegating subagent's prompt would carry the goal-turn protocol naming declare_goal_status", gs.Condition)
	}
	if ags := AvailableAgentsFromContext(taskCtx); len(ags) != 0 {
		t.Errorf("expected the subagent roster cleared, got %d entries", len(ags))
	}
	if uas := UserAgentsFromContext(taskCtx); len(uas) != 0 {
		t.Errorf("expected the requested-agents roster cleared, got %d entries", len(uas))
	}
	if sink := coretools.GoalStatusSinkFrom(taskCtx); sink != nil {
		t.Error("expected the goal-status sink cleared")
	}
	if sink := coretools.VerificationSinkFrom(taskCtx); sink != nil {
		t.Error("expected the verification sink cleared")
	}
	// The child registry IS kept (re-delegation capability).
	if reg := coretools.DelegationRegistryFrom(taskCtx); reg != childReg {
		t.Error("expected the child delegation registry injected")
	}
	// The trajectory store is detached from the parent's.
	if store := agent.TrajectoryStoreFromContext(taskCtx); store == nil {
		t.Error("expected a trajectory store present for the redelegating subagent")
	} else if detached, ok := store.(*trajectoryHolder); !ok || detached == parent {
		t.Error("expected a fresh trajectory holder for the redelegating subagent")
	}
}

// --- execute_plan is conductor-only at dispatch, not just at descriptor level ---

// TestSubagentCtx_StripsPlanStepExecutor: the PlanStepExecutor handoff is
// conductor-only context, but execute_plan is GroupSystem + PolicyAlwaysAllow,
// so the registry's policy gates never stop a subagent's call — the strip must
// remove the executor itself. With it gone, a delegated child's execute_plan
// call lands in the tool's documented no-Conductor error instead of driving
// the parent task's plan waves (force re-running approved steps, unbounded
// executor recursion through inherited child contexts).
func TestSubagentCtx_StripsPlanStepExecutor(t *testing.T) {
	executor := &stubPlanStepExecutor{}
	ctx := coretools.WithPlanStepExecutor(context.Background(), executor)

	sub := subagentCtx(ctx)
	if got := coretools.PlanStepExecutorFrom(sub); got != nil {
		t.Fatalf("subagentCtx must strip the PlanStepExecutor, got %T", got)
	}

	// The tool call from a subagent context fails closed with the documented
	// error instead of reaching the parent's executor.
	tool := coretools.NewExecutePlanTool()
	raw, err := json.Marshal(map[string]any{"steps": []string{"step_1"}})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	res, err := tool.Execute(sub, raw)
	if err != nil {
		t.Fatalf("execute_plan returned a hard error: %v", err)
	}
	if !strings.Contains(res.Content, "no plan step executor in context") {
		t.Fatalf("execute_plan content = %q, want the no-Conductor error", res.Content)
	}
	if executor.calls != 0 {
		t.Errorf("the parent executor was invoked %d times, want 0", executor.calls)
	}

	// Control: the conductor's own context still reaches the executor.
	if got := coretools.PlanStepExecutorFrom(ctx); got == nil {
		t.Fatal("control: the parent context must still carry the executor")
	}
}

type stubPlanStepExecutor struct{ calls int }

func (s *stubPlanStepExecutor) Execute(ctx context.Context, stepIDs []string) ([]coretools.PlanStepResult, error) {
	s.calls++
	return []coretools.PlanStepResult{{StepID: "stub", Status: "completed"}}, nil
}

// --- declare_step_complete is conductor-only at dispatch, not just at descriptor level ---

// TestSubagentCtx_StripsStepCompleteFunc: the StepCompleteFunc handoff is
// conductor-only context, and declare_step_complete is GroupSystem +
// PolicyAlwaysAllow, so the registry's policy gates never stop a subagent's
// call — the strip must remove the callback itself. With it gone, a delegated
// child's declare_step_complete call lands in the tool's documented
// no-Conductor error instead of terminating steps of the PARENT task's plan
// (skipped waves, a plan finished without its verification steps).
func TestSubagentCtx_StripsStepCompleteFunc(t *testing.T) {
	steps := []string{}
	ctx := coretools.WithStepCompleteFunc(context.Background(), func(stepID string, success bool, errMsg string) {
		steps = append(steps, stepID)
	})

	sub := subagentCtx(ctx)
	if got := coretools.StepCompleteFuncFromContext(sub); got != nil {
		t.Fatalf("subagentCtx must strip the StepCompleteFunc, got %T", got)
	}

	// The tool call from a subagent context fails closed with the documented
	// error instead of reaching the parent's callback.
	tool := coretools.NewDeclareStepCompleteTool()
	raw, err := json.Marshal(map[string]any{"step_id": "step_3", "success": true})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	res, err := tool.Execute(sub, raw)
	if err != nil {
		t.Fatalf("declare_step_complete returned a hard error: %v", err)
	}
	if !strings.Contains(res.Content, "no step-complete callback in context") {
		t.Fatalf("declare_step_complete content = %q, want the no-Conductor error", res.Content)
	}
	if len(steps) != 0 {
		t.Errorf("the parent callback was invoked %d times, want 0", len(steps))
	}

	// Control: the conductor's own context still reaches the callback.
	if got := coretools.StepCompleteFuncFromContext(ctx); got == nil {
		t.Fatal("control: the parent context must still carry the step-complete callback")
	}

	// The goal proposer rides the same strip class as defense in depth:
	// propose_goal is already excluded from subagent tool sets, but the
	// context must not carry the proposer either.
	ctx = coretools.WithGoalProposer(context.Background(), stubGoalProposer{})
	if got := coretools.GoalProposerFrom(subagentCtx(ctx)); got != nil {
		t.Fatalf("subagentCtx must strip the GoalProposer, got %T", got)
	}
}

type stubGoalProposer struct{}

func (stubGoalProposer) Propose(ctx context.Context, proposal coretools.GoalProposal) (coretools.GoalProposalResponse, error) {
	return coretools.GoalProposalResponse{Decision: "cancel"}, nil
}
