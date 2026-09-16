package tools

import (
	"context"
	"testing"
)

// stubWorkflowPublisher is a stubPlanPublisher that also carries the optional
// planAbandoner capability (mirrors the core conductorPublisher). It records
// every AbandonPlan call so the tests can assert whether declare_plan released
// the plan workflow.
type stubWorkflowPublisher struct {
	stubPlanPublisher
	abandonCalls int
}

func (p *stubWorkflowPublisher) AbandonPlan() {
	p.abandonCalls++
}

// TestDeclarePlan_Abandon_ReleasesPlanWorkflow: an abandoned plan must release
// the run's plan workflow. Publishing the plan for review already marked it
// active, so on decision "abandon" declare_plan calls AbandonPlan() on a
// publisher that carries the optional capability. The plan is still published
// (it stays on the blackboard as a historical artifact) and the result is an
// error telling the model not to proceed.
func TestDeclarePlan_Abandon_ReleasesPlanWorkflow(t *testing.T) {
	publisher := &stubWorkflowPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	res, err := NewDeclarePlanTool(approveFunc("abandon")).Execute(ctx, marshalAwaitApprovalInput(t, validTask("step_1")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Errorf("abandon should surface as an error result, got: %+v", res)
	}
	if publisher.abandonCalls != 1 {
		t.Fatalf("abandon must call AbandonPlan() exactly once, got %d", publisher.abandonCalls)
	}
	if publisher.publishCalls != 1 {
		t.Errorf("the abandoned plan must still be published for review, got %d publishes", publisher.publishCalls)
	}
}

// TestDeclarePlan_ApproveAndRequestChanges_PreservePlanWorkflow: approve and
// request_changes leave the workflow active. Publish already marked it, and on
// request_changes the model re-declares (so delegate must stay disabled) —
// neither decision may call AbandonPlan.
func TestDeclarePlan_ApproveAndRequestChanges_PreservePlanWorkflow(t *testing.T) {
	for _, decision := range []string{"approve", "request_changes"} {
		t.Run(decision, func(t *testing.T) {
			publisher := &stubWorkflowPublisher{}
			ctx := WithPlanPublisher(context.Background(), publisher)

			res, err := NewDeclarePlanTool(approveFunc(decision)).Execute(ctx, marshalAwaitApprovalInput(t, validTask("step_1")))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.IsError {
				t.Fatalf("%s must be a non-error result, got: %+v", decision, res)
			}
			if publisher.abandonCalls != 0 {
				t.Errorf("%s must leave the plan workflow active, got %d AbandonPlan calls", decision, publisher.abandonCalls)
			}
		})
	}
}

// TestDeclarePlan_Present_PreservesPlanWorkflow: present mode returns before the
// approval callback, and Publish marks the workflow active — the capability must
// not be invoked.
func TestDeclarePlan_Present_PreservesPlanWorkflow(t *testing.T) {
	publisher := &stubWorkflowPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	if _, err := NewDeclarePlanTool(nil).Execute(ctx, validDeclarePlanInput(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if publisher.abandonCalls != 0 {
		t.Errorf("present must leave the plan workflow active, got %d AbandonPlan calls", publisher.abandonCalls)
	}
}

// TestDeclarePlan_Abandon_PublisherWithoutCapabilityIsHarmless: a publisher that
// does not implement planAbandoner (the plain stub) must be left as-is on
// abandon — the type assertion simply fails and the abandon result is unchanged.
func TestDeclarePlan_Abandon_PublisherWithoutCapabilityIsHarmless(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	res, err := NewDeclarePlanTool(approveFunc("abandon")).Execute(ctx, marshalAwaitApprovalInput(t, validTask("step_1")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Errorf("abandon should surface as an error result, got: %+v", res)
	}
	if publisher.publishCalls != 1 {
		t.Errorf("expected exactly 1 publish, got %d", publisher.publishCalls)
	}
}
