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

// marshalPlanInput builds a declare_plan JSON input from raw task objects.
func marshalPlanInput(t *testing.T, tasks ...map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"mode": "present", "tasks": tasks})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return raw
}

// validTask returns a minimal schema-valid task with the given id.
func validTask(id string) map[string]any {
	return map[string]any{"id": id, "summary": "Step " + id, "description": "Do " + id}
}

// TestDeclarePlan_Validation_MissingRequiredFields: a task without id,
// summary, or description is rejected with an IsError result naming the
// 1-based task number and the missing field; nothing is published.
func TestDeclarePlan_Validation_MissingRequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		field string
		task  map[string]any
	}{
		{"missing id", "id", map[string]any{"summary": "A", "description": "Do A"}},
		{"blank id", "id", map[string]any{"id": "   ", "summary": "A", "description": "Do A"}},
		{"missing summary", "summary", map[string]any{"id": "step_2", "description": "Do A"}},
		{"blank summary", "summary", map[string]any{"id": "step_2", "summary": "  ", "description": "Do A"}},
		{"missing description", "description", map[string]any{"id": "step_2", "summary": "A"}},
		{"blank description", "description", map[string]any{"id": "step_2", "summary": "A", "description": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &stubPlanPublisher{}
			ctx := WithPlanPublisher(context.Background(), publisher)

			// Valid task first, invalid second: proves 1-based numbering.
			res, err := NewDeclarePlanTool(nil).Execute(ctx, marshalPlanInput(t, validTask("step_1"), tc.task))
			if err != nil {
				t.Fatalf("validation must be a tool result, not a Go error, got: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected IsError result, got: %+v", res)
			}
			for _, want := range []string{"task 2", `"` + tc.field + `"`, "declare_plan"} {
				if !strings.Contains(res.Content, want) {
					t.Errorf("error should mention %q, got: %q", want, res.Content)
				}
			}
			if publisher.publishCalls != 0 {
				t.Errorf("invalid plan must not be published, got %d publishes", publisher.publishCalls)
			}
		})
	}
}

// TestDeclarePlan_Validation_DuplicateID: two tasks sharing an id are rejected
// with the duplicated id and the 1-based numbers of both occurrences.
func TestDeclarePlan_Validation_DuplicateID(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	res, err := NewDeclarePlanTool(nil).Execute(ctx, marshalPlanInput(t,
		validTask("step_1"),
		validTask("step_2"),
		validTask("step_1"),
	))
	if err != nil {
		t.Fatalf("validation must be a tool result, not a Go error, got: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	for _, want := range []string{"step_1", "tasks 1 and 3", "declare_plan"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("error should mention %q, got: %q", want, res.Content)
		}
	}
	if publisher.publishCalls != 0 {
		t.Errorf("invalid plan must not be published, got %d publishes", publisher.publishCalls)
	}
}

// TestDeclarePlan_Validation_UnknownDependency: depends_on pointing at an id
// that no task declares is rejected with the broken reference and the task
// that carries it.
func TestDeclarePlan_Validation_UnknownDependency(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	second := map[string]any{
		"id":          "step_2",
		"summary":     "B",
		"description": "Do B",
		"depends_on":  []string{"step_1", "step_9"},
	}
	res, err := NewDeclarePlanTool(nil).Execute(ctx, marshalPlanInput(t, validTask("step_1"), second))
	if err != nil {
		t.Fatalf("validation must be a tool result, not a Go error, got: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	for _, want := range []string{"step_9", "task 2", "declare_plan"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("error should mention %q, got: %q", want, res.Content)
		}
	}
	if publisher.publishCalls != 0 {
		t.Errorf("invalid plan must not be published, got %d publishes", publisher.publishCalls)
	}
}

// TestDeclarePlan_Validation_MultipleViolationsTogether: every violation in a
// plan is listed in a single result, not just the first one.
func TestDeclarePlan_Validation_MultipleViolationsTogether(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	tasks := []map[string]any{
		{"id": "step_1", "description": "Do A"}, // missing summary
		validTask("dupe"),                       // first "dupe" occurrence
		{ // duplicate "dupe" + unknown dependency
			"id":          "dupe",
			"summary":     "C",
			"description": "Do C",
			"depends_on":  []string{"step_missing"},
		},
	}
	res, err := NewDeclarePlanTool(nil).Execute(ctx, marshalPlanInput(t, tasks...))
	if err != nil {
		t.Fatalf("validation must be a tool result, not a Go error, got: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	for _, want := range []string{
		"task 1",        // missing summary in task 1
		`"summary"`,     // ...with the field name
		"tasks 2 and 3", // duplicate id spans both occurrences
		"step_missing",  // unknown dependency
		"task 3",        // ...carried by task 3
		"declare_plan",  // actionable fix instruction
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("error should mention %q, got: %q", want, res.Content)
		}
	}
	if publisher.publishCalls != 0 {
		t.Errorf("invalid plan must not be published, got %d publishes", publisher.publishCalls)
	}
}

// TestDeclarePlan_Validation_BeforeContinuationGuard: validation runs before
// the continuation guard, so re-declaring an invalid plan on a resumed task
// yields the validation error instead of the soft continue-with-execute_plan
// hint — a malformed plan must never slip past validation on any path.
func TestDeclarePlan_Validation_BeforeContinuationGuard(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)
	ctx = WithPlanChecker(ctx, &stubContinuationChecker{continuation: true})

	res, err := NewDeclarePlanTool(nil).Execute(ctx, marshalPlanInput(t,
		map[string]any{"summary": "A", "description": "Do A"}, // no id
	))
	if err != nil {
		t.Fatalf("validation must be a tool result, not a Go error, got: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected validation IsError even when continuation flag is set, got: %+v", res)
	}
	if !strings.Contains(res.Content, "missing required field") {
		t.Errorf("expected validation error, got: %q", res.Content)
	}
	if publisher.publishCalls != 0 {
		t.Errorf("invalid plan must not be published, got %d publishes", publisher.publishCalls)
	}
}

// TestValidatePlanTasks_ValidPlanPasses: the pure validator returns nil for a
// plan with all fields present, unique ids, and resolvable (including
// transitive) dependencies.
func TestValidatePlanTasks_ValidPlanPasses(t *testing.T) {
	tasks := []PlanTaskInput{
		{ID: "step_1", Summary: "A", Description: "Do A"},
		{ID: "step_2", Summary: "B", Description: "Do B", DependsOn: []string{"step_1"}},
		{ID: "step_3", Summary: "C", Description: "Do C", DependsOn: []string{"step_2"}}, // transitive dep on step_1
	}
	if err := validatePlanTasks(tasks); err != nil {
		t.Fatalf("valid plan must pass validation, got: %v", err)
	}
}

// TestValidatePlanTasks_CyclesRejected pins the DAG check: cyclic and
// self-referencing depends_on graphs pass reference resolution but can never
// be satisfied — they must be rejected at declaration with the offending
// ids named, not surface later as phantom "upstream failures".
func TestValidatePlanTasks_CyclesRejected(t *testing.T) {
	cycle := []PlanTaskInput{
		{ID: "a", Summary: "s", Description: "d", DependsOn: []string{"b"}},
		{ID: "b", Summary: "s", Description: "d", DependsOn: []string{"a"}},
	}
	err := validatePlanTasks(cycle)
	if err == nil {
		t.Fatal("2-cycle accepted")
	}
	msg := err.Error()
	if !strings.Contains(msg, "cycle") {
		t.Errorf("error should name the cycle: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if !strings.Contains(msg, id) {
			t.Errorf("error should name the participating id %q: %v", id, err)
		}
	}

	self := []PlanTaskInput{{ID: "solo", Summary: "s", Description: "d", DependsOn: []string{"solo"}}}
	if err := validatePlanTasks(self); err == nil || !strings.Contains(err.Error(), "depends on itself") {
		t.Errorf("self-dependency rejected incorrectly: %v", err)
	}

	// Longer cycle with an innocent downstream node: all stuck ids named.
	three := []PlanTaskInput{
		{ID: "x", Summary: "s", Description: "d", DependsOn: []string{"z"}},
		{ID: "y", Summary: "s", Description: "d", DependsOn: []string{"x"}},
		{ID: "z", Summary: "s", Description: "d", DependsOn: []string{"y"}},
	}
	if err := validatePlanTasks(three); err == nil {
		t.Fatal("3-cycle accepted")
	}

	valid := []PlanTaskInput{
		{ID: "p", Summary: "s", Description: "d"},
		{ID: "q", Summary: "s", Description: "d", DependsOn: []string{"p"}},
	}
	if err := validatePlanTasks(valid); err != nil {
		t.Errorf("valid DAG rejected: %v", err)
	}
}

// dependsOnTask builds a schema-valid task that depends on the given ids.
func dependsOnTask(id string, deps ...string) map[string]any {
	return map[string]any{
		"id":          id,
		"summary":     "Step " + id,
		"description": "Do " + id,
		"depends_on":  deps,
	}
}

// marshalAwaitApprovalInput builds a declare_plan JSON input in await_approval mode.
func marshalAwaitApprovalInput(t *testing.T, tasks ...map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"mode": "await_approval", "tasks": tasks})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return raw
}

// approveFunc returns an ApprovalFunc that always answers with the given decision.
func approveFunc(decision string) ApprovalFunc {
	return func(_ context.Context, _, _ string) (string, string, error) {
		return decision, "", nil
	}
}

// TestPlanExecutionWaves pins the Kahn layering and its rendering: independent
// steps share one wave in declaration order, a chain yields one wave per step,
// and a diamond layers the independent middle siblings together.
func TestPlanExecutionWaves(t *testing.T) {
	t.Run("independent steps share one wave in declaration order", func(t *testing.T) {
		waves := planExecutionWaves([]PlanTaskInput{
			{ID: "step_1", Summary: "s", Description: "d"},
			{ID: "step_3", Summary: "s", Description: "d"},
			{ID: "step_2", Summary: "s", Description: "d"},
		})
		if got, want := formatExecutionWaves(waves), "Execution waves: 1=[step_1, step_3, step_2]"; got != want {
			t.Errorf("echo = %q, want %q", got, want)
		}
	})

	t.Run("a chain yields one wave per step", func(t *testing.T) {
		waves := planExecutionWaves([]PlanTaskInput{
			{ID: "step_1", Summary: "s", Description: "d"},
			{ID: "step_2", Summary: "s", Description: "d", DependsOn: []string{"step_1"}},
			{ID: "step_3", Summary: "s", Description: "d", DependsOn: []string{"step_2"}},
		})
		if got, want := formatExecutionWaves(waves), "Execution waves: 1=[step_1] · 2=[step_2] · 3=[step_3]"; got != want {
			t.Errorf("echo = %q, want %q", got, want)
		}
	})

	t.Run("diamond layers independent siblings together", func(t *testing.T) {
		waves := planExecutionWaves([]PlanTaskInput{
			{ID: "a", Summary: "s", Description: "d"},
			{ID: "b", Summary: "s", Description: "d", DependsOn: []string{"a"}},
			{ID: "c", Summary: "s", Description: "d", DependsOn: []string{"a"}},
			{ID: "d", Summary: "s", Description: "d", DependsOn: []string{"b", "c"}},
		})
		if got, want := formatExecutionWaves(waves), "Execution waves: 1=[a] · 2=[b, c] · 3=[d]"; got != want {
			t.Errorf("echo = %q, want %q", got, want)
		}
	})
}

// TestDeclarePlan_SingleWaveEchoAndHint: a multi-step plan with no dependencies
// collapses into one wave — the result carries the wave echo plus a non-blocking
// hint (IsError=false) warning that every step will run concurrently.
func TestDeclarePlan_SingleWaveEchoAndHint(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	res, err := NewDeclarePlanTool(nil).Execute(ctx, marshalPlanInput(t,
		validTask("step_1"), validTask("step_2"), validTask("step_3"),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("single-wave hint must be non-blocking (IsError=false), got: %+v", res)
	}
	if !strings.Contains(res.Content, "Execution waves: 1=[step_1, step_2, step_3]") {
		t.Errorf("result should echo the single wave, got: %q", res.Content)
	}
	if !strings.Contains(res.Content, "single parallel wave") {
		t.Errorf("result should carry the single-wave hint, got: %q", res.Content)
	}
	if publisher.publishCalls != 1 {
		t.Errorf("expected exactly 1 publish, got %d", publisher.publishCalls)
	}
}

// TestDeclarePlan_MultiWaveEchoNoHint: a chain renders one wave per step and
// must NOT carry the single-wave hint.
func TestDeclarePlan_MultiWaveEchoNoHint(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	res, err := NewDeclarePlanTool(nil).Execute(ctx, marshalPlanInput(t,
		validTask("step_1"),
		dependsOnTask("step_2", "step_1"),
		dependsOnTask("step_3", "step_2"),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if !strings.Contains(res.Content, "Execution waves: 1=[step_1] · 2=[step_2] · 3=[step_3]") {
		t.Errorf("result should echo all three waves, got: %q", res.Content)
	}
	if strings.Contains(res.Content, "single parallel wave") {
		t.Errorf("multi-wave plan must not carry the single-wave hint, got: %q", res.Content)
	}
}

// TestDeclarePlan_SingleStepNoHint: a one-step plan is trivially one wave; the
// hint must not fire (nothing to run concurrently).
func TestDeclarePlan_SingleStepNoHint(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	res, err := NewDeclarePlanTool(nil).Execute(ctx, validDeclarePlanInput(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if !strings.Contains(res.Content, "Execution waves: 1=[step_1]") {
		t.Errorf("result should echo the single-step wave, got: %q", res.Content)
	}
	if strings.Contains(res.Content, "single parallel wave") {
		t.Errorf("single-step plan must not carry the hint, got: %q", res.Content)
	}
}

// TestDeclarePlan_AwaitApprovalNoHint: in await_approval mode the single-wave
// hint is suppressed (the user is already reviewing the plan), while the wave
// echo may still be surfaced on approval.
func TestDeclarePlan_AwaitApprovalNoHint(t *testing.T) {
	publisher := &stubPlanPublisher{}
	ctx := WithPlanPublisher(context.Background(), publisher)

	res, err := NewDeclarePlanTool(approveFunc("approve")).Execute(ctx, marshalAwaitApprovalInput(t,
		validTask("step_1"), validTask("step_2"), validTask("step_3"),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("approve must be a non-error result, got: %+v", res)
	}
	if strings.Contains(res.Content, "single parallel wave") {
		t.Errorf("await_approval must not surface the single-wave hint, got: %q", res.Content)
	}
	if !strings.Contains(res.Content, "Plan approved by user") {
		t.Errorf("approve result missing approval confirmation, got: %q", res.Content)
	}
	if !strings.Contains(res.Content, "Execution waves: 1=[step_1, step_2, step_3]") {
		t.Errorf("wave echo should be present on approval, got: %q", res.Content)
	}
	if publisher.publishCalls != 1 {
		t.Errorf("expected exactly 1 publish, got %d", publisher.publishCalls)
	}
}
