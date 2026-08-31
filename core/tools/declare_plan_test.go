package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubPlanPublisher records Publish calls without touching the filesystem.
type stubPlanPublisher struct {
	publishCalls int
	lastTasks    []PlanTaskInput
}

func (p *stubPlanPublisher) Publish(_ context.Context, tasks []PlanTaskInput) (string, error) {
	p.publishCalls++
	p.lastTasks = tasks
	return "/plans/plan_stub.md", nil
}

func (p *stubPlanPublisher) LastPlanMarkdown() string { return "# stub plan" }

// stubPlanChecker is a fixed PlanChecker.
type stubPlanChecker struct {
	declared bool
}

func (c *stubPlanChecker) HasDeclaredPlan() bool { return c.declared }

// stubContinuationChecker is a PlanChecker that also carries the optional
// PlanContinuation capability (mirrors the core conductorLauncher).
type stubContinuationChecker struct {
	stubPlanChecker
	continuation bool
}

func (c *stubContinuationChecker) PlanContinuation() bool { return c.continuation }

func validDeclarePlanInput(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"mode": "present",
		"tasks": []map[string]any{{
			"id":          "step_1",
			"summary":     "A",
			"description": "Do A",
		}},
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return raw
}

// TestDeclarePlan_ContinuationSoftHint: on a resumed task whose approved plan
// still has unreached steps, declare_plan returns a soft (non-error) hint
// pointing back to execute_plan and does NOT publish a replacement plan.
func TestDeclarePlan_ContinuationSoftHint(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)
	ctx = WithPlanChecker(ctx, &stubContinuationChecker{continuation: true})

	res, err := NewDeclarePlanTool(nil).Execute(ctx, validDeclarePlanInput(t))
	if err != nil {
		t.Fatalf("soft hint must not be a tool error, got: %v", err)
	}
	if res.IsError {
		t.Errorf("hint must be soft (IsError=false), got %+v", res)
	}
	if !strings.Contains(res.Content, "already been declared and approved") {
		t.Errorf("hint should say the plan is already approved, got: %q", res.Content)
	}
	if !strings.Contains(res.Content, "execute_plan") {
		t.Errorf("hint should direct the model to execute_plan, got: %q", res.Content)
	}
	if publisher.publishCalls != 0 {
		t.Errorf("continuation must not publish a replacement plan, got %d publishes", publisher.publishCalls)
	}
}

// TestDeclarePlan_NoContinuation_Publishes: without the continuation flag the
// tool behaves exactly as before — the publisher is invoked.
func TestDeclarePlan_NoContinuation_Publishes(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)
	ctx = WithPlanChecker(ctx, &stubContinuationChecker{continuation: false})

	res, err := NewDeclarePlanTool(nil).Execute(ctx, validDeclarePlanInput(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error result: %+v", res)
	}
	if publisher.publishCalls != 1 {
		t.Fatalf("expected exactly 1 publish, got %d", publisher.publishCalls)
	}
	if !strings.Contains(res.Content, "Plan published to") {
		t.Errorf("expected normal publish result, got: %q", res.Content)
	}
}

// TestDeclarePlan_CheckerWithoutCapability_Publishes: a PlanChecker that does
// not implement PlanContinuation (older embedding, plain stub) must not trip
// the hint — the capability is optional, detected via type assertion.
func TestDeclarePlan_CheckerWithoutCapability_Publishes(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)
	ctx = WithPlanChecker(ctx, &stubPlanChecker{declared: true})

	if _, err := NewDeclarePlanTool(nil).Execute(ctx, validDeclarePlanInput(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if publisher.publishCalls != 1 {
		t.Errorf("expected exactly 1 publish, got %d", publisher.publishCalls)
	}
}
