package session

import (
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
)

// TestEventEmitterImplementsInterface verifies EventEmitter satisfies core.Emitter at compile time.
func TestEventEmitterImplementsInterface(t *testing.T) {
	var _ core.Emitter = (*EventEmitter)(nil)
}

// TestEventEmitterRouting verifies Routing emits correct event.
func TestEventEmitterRouting(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.Routing("react", "code", "3")

	if received.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", received.SessionID)
	}
	if received.Type != "routing" {
		t.Errorf("expected type 'routing', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if data["mode"] != "react" {
		t.Errorf("expected mode 'react', got %q", data["mode"])
	}
	if data["domain"] != "code" {
		t.Errorf("expected domain 'code', got %q", data["domain"])
	}
	if data["complexity"] != "3" {
		t.Errorf("expected complexity '3', got %q", data["complexity"])
	}
}

// TestEventEmitterPlanGenerated verifies PlanGenerated emits correct event.
func TestEventEmitterPlanGenerated(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.PlanGenerated(5, []orchestration.PlanStepEvent{
		{Description: "Step 1", Status: "pending"},
	})

	if received.Type != "plan_generated" {
		t.Errorf("expected type 'plan_generated', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step_count"] != 5 {
		t.Errorf("expected step_count 5, got %v", data["step_count"])
	}
}

// TestEventEmitterStepStart verifies StepStart emits correct event.
func TestEventEmitterStepStart(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.StepStart(1)

	if received.Type != "step_start" {
		t.Errorf("expected type 'step_start', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step_num"] != 1 {
		t.Errorf("expected step_num 1, got %d", data["step_num"])
	}
}

// TestEventEmitterThought verifies Thought emits correct event.
func TestEventEmitterThought(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.Thought(1, "I need to analyze this problem", "deep reasoning here")

	if received.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", received.SessionID)
	}
	if received.Type != "thought" {
		t.Errorf("expected type 'thought', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step_num"] != 1 {
		t.Errorf("expected step_num 1, got %v", data["step_num"])
	}
	if data["content"] != "I need to analyze this problem" {
		t.Errorf("expected content 'I need to analyze this problem', got %v", data["content"])
	}
	if data["reasoning"] != "deep reasoning here" {
		t.Errorf("expected reasoning 'deep reasoning here', got %v", data["reasoning"])
	}
}

// TestEventEmitterToolCall verifies ToolCall emits correct event.
func TestEventEmitterToolCall(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.ToolCall(2, 0, "bash", "ls -la", "core")

	if received.Type != "tool_call" {
		t.Errorf("expected type 'tool_call', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step"] != 2 {
		t.Errorf("expected step 2, got %v", data["step"])
	}
	if data["tool"] != "bash" {
		t.Errorf("expected tool 'bash', got %v", data["tool"])
	}
	if data["args"] != "ls -la" {
		t.Errorf("expected args 'ls -la', got %v", data["args"])
	}
}

// TestEventEmitterToolResult verifies ToolResult emits correct event.
func TestEventEmitterToolResult(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.ToolResult(2, 0, 1024, "preview content", false)

	if received.Type != "tool_result" {
		t.Errorf("expected type 'tool_result', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step"] != 2 {
		t.Errorf("expected step 2, got %v", data["step"])
	}
	if data["result_len"] != 1024 {
		t.Errorf("expected result_len 1024, got %v", data["result_len"])
	}
	if data["result"] != "preview content" {
		t.Errorf("expected result 'preview content', got %v", data["result"])
	}
}

// TestEventEmitterStepComplete verifies StepComplete emits correct event.
func TestEventEmitterStepComplete(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	duration := 1500 * time.Millisecond
	emitter.StepComplete(1, duration)

	if received.Type != "step_complete" {
		t.Errorf("expected type 'step_complete', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step_num"] != 1 {
		t.Errorf("expected step_num 1, got %v", data["step_num"])
	}
	// Duration should be in milliseconds
	if data["duration"] != int64(1500) {
		t.Errorf("expected duration 1500, got %v", data["duration"])
	}
}

// TestEventEmitterSubAgentLaunch verifies SubAgentLaunch emits correct event.
func TestEventEmitterSubAgentLaunch(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.SubAgentLaunch("step_1", "Install dependencies")

	if received.Type != "subagent_launch" {
		t.Errorf("expected type 'subagent_launch', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string data, got %T", received.Data)
	}
	if data["step_id"] != "step_1" {
		t.Errorf("expected step_id 'step_1', got %q", data["step_id"])
	}
	if data["description"] != "Install dependencies" {
		t.Errorf("expected description 'Install dependencies', got %q", data["description"])
	}
}

// TestEventEmitterSubAgentComplete verifies SubAgentComplete emits correct event.
func TestEventEmitterSubAgentComplete(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	duration := 5 * time.Second
	emitter.SubAgentComplete("step_1", true, duration)

	if received.Type != "subagent_complete" {
		t.Errorf("expected type 'subagent_complete', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step_id"] != "step_1" {
		t.Errorf("expected step_id 'step_1', got %v", data["step_id"])
	}
	if data["success"] != true {
		t.Errorf("expected success true, got %v", data["success"])
	}
	if data["duration"] != int64(5000) {
		t.Errorf("expected duration 5000, got %v", data["duration"])
	}
}

// TestEventEmitterReflection verifies Reflection emits correct event.
func TestEventEmitterReflection(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.Reflection(&orchestration.Reflection{Summary: "Test failed due to missing dependency", Hypotheses: []string{"Missing import"}}, 1, 3)

	if received.Type != "reflection" {
		t.Errorf("expected type 'reflection', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["summary"] != "Test failed due to missing dependency" {
		t.Errorf("expected summary 'Test failed due to missing dependency', got %v", data["summary"])
	}
}

// TestEventEmitterRetry verifies Retry emits correct event.
func TestEventEmitterRetry(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.Retry(2, 3)

	if received.Type != "retry" {
		t.Errorf("expected type 'retry', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]int)
	if !ok {
		t.Fatalf("expected map[string]int data, got %T", received.Data)
	}
	if data["attempt"] != 2 {
		t.Errorf("expected attempt 2, got %d", data["attempt"])
	}
	if data["max_attempts"] != 3 {
		t.Errorf("expected max_attempts 3, got %d", data["max_attempts"])
	}
}

// TestEventEmitterContextFill verifies ContextFill emits correct event.
func TestEventEmitterContextFill(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.ContextFill(75.5, 75500, 100000, "compact", "step_1")

	if received.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", received.SessionID)
	}
	if received.Type != "context_fill" {
		t.Errorf("expected type 'context_fill', got %q", received.Type)
	}

	data, ok := received.Data.(ContextFillEventData)
	if !ok {
		t.Fatalf("expected ContextFillEventData data, got %T", received.Data)
	}
	if data.FillPercent != 75.5 {
		t.Errorf("expected FillPercent 75.5, got %v", data.FillPercent)
	}
	if data.UsedTokens != 75500 {
		t.Errorf("expected UsedTokens 75500, got %v", data.UsedTokens)
	}
	if data.MaxTokens != 100000 {
		t.Errorf("expected MaxTokens 100000, got %v", data.MaxTokens)
	}
	if data.Status != "compact" {
		t.Errorf("expected Status 'compact', got %v", data.Status)
	}
	if data.PlanStepID != "step_1" {
		t.Errorf("expected PlanStepID 'step_1', got %v", data.PlanStepID)
	}
}

// TestEventEmitterEmitSessionTokensForwardFill verifies that EmitSessionTokens
// forwards the cached context-window fill (percent + used/max tokens) populated
// by a prior session-root ContextFill, so the status bar can render "N of M".
func TestEventEmitterEmitSessionTokensForwardFill(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	// Populate the session-root fill cache.
	emitter.ContextFill(75.5, 75500, 100000, "compact", "")
	// EmitSessionTokens should forward the cached fill alongside token totals.
	emitter.EmitSessionTokens(1000, 500, "gpt-4", "openai")

	if received.Type != "session_tokens" {
		t.Fatalf("expected type 'session_tokens', got %q", received.Type)
	}

	data, ok := received.Data.(SessionTokensEventData)
	if !ok {
		t.Fatalf("expected SessionTokensEventData data, got %T", received.Data)
	}
	if data.FillPercent != 75.5 {
		t.Errorf("expected FillPercent 75.5, got %v", data.FillPercent)
	}
	if data.UsedTokens != 75500 {
		t.Errorf("expected UsedTokens 75500, got %v", data.UsedTokens)
	}
	if data.MaxTokens != 100000 {
		t.Errorf("expected MaxTokens 100000, got %v", data.MaxTokens)
	}
	if data.SessionInputTokens != 1000 {
		t.Errorf("expected SessionInputTokens 1000, got %v", data.SessionInputTokens)
	}
	if data.SessionOutputTokens != 500 {
		t.Errorf("expected SessionOutputTokens 500, got %v", data.SessionOutputTokens)
	}
}

// TestEventEmitterThreadSafety verifies concurrent calls don't panic.
func TestEventEmitterThreadSafety(t *testing.T) {
	var events []Event
	var mu sync.Mutex
	emit := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}

	emitter := NewEventEmitter("test-session", emit)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(step int) {
			defer wg.Done()
			emitter.StepStart(step)
			emitter.ToolCall(step, 0, "bash", "echo test", "core")
			emitter.StepComplete(step, time.Second)
		}(i)
	}
	wg.Wait()

	mu.Lock()
	if len(events) != 300 {
		t.Errorf("expected 300 events, got %d", len(events))
	}
	mu.Unlock()
}

// TestEventEmitterAllMethods verifies all interface methods can be called.
func TestEventEmitterAllMethods(t *testing.T) {
	var events []Event
	var mu sync.Mutex
	emit := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}

	emitter := NewEventEmitter("test-session", emit)

	// Call all methods
	emitter.Routing("react", "code", "3")
	emitter.PlanGenerated(5, []orchestration.PlanStepEvent{{Description: "Step 1", Status: "pending"}})
	emitter.StepStart(1)
	emitter.Thought(1, "I need to think about this", "")
	emitter.ToolCall(1, 0, "bash", "ls", "core")
	emitter.ToolResult(1, 0, 100, "", false)
	emitter.StepComplete(1, time.Second)
	emitter.SubAgentLaunch("step_1", "Do something")
	emitter.SubAgentComplete("step_1", true, time.Second)
	emitter.Reflection(&orchestration.Reflection{Summary: "Something went wrong", Hypotheses: []string{"Issue found"}}, 1, 3)
	emitter.Retry(2, 3)
	emitter.ContextFill(75.5, 75500, 100000, "compact", "step_1")

	mu.Lock()
	if len(events) != 12 {
		t.Errorf("expected 12 events, got %d", len(events))
	}

	// Verify all event types
	expectedTypes := map[string]bool{
		"routing":           false,
		"plan_generated":    false,
		"step_start":        false,
		"thought":           false,
		"tool_call":         false,
		"tool_result":       false,
		"step_complete":     false,
		"subagent_launch":   false,
		"subagent_complete": false,
		"reflection":        false,
		"retry":             false,
		"context_fill":      false,
	}

	for _, e := range events {
		if _, ok := expectedTypes[e.Type]; ok {
			expectedTypes[e.Type] = true
		}
	}

	for typ, found := range expectedTypes {
		if !found {
			t.Errorf("event type %q not found", typ)
		}
	}
	mu.Unlock()
}

// TestEventEmitterWithPlanStepID verifies WithPlanStepID returns a scoped emitter
// that injects plan_step_id into map[string]any event data.
func TestEventEmitterWithPlanStepID(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	base := NewEventEmitter("test-session", emit)
	scoped := base.WithPlanStepID("step-42")

	// Use the scoped emitter (type-assert to *EventEmitter for method access)
	scopedEmitter, ok := scoped.(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithPlanStepID")
	}

	// PlanGenerated emits map[string]any so plan_step_id should be injected
	scopedEmitter.PlanGenerated(2, []orchestration.PlanStepEvent{
		{Description: "Do stuff", Status: "pending"},
	})

	if received.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", received.SessionID)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["plan_step_id"] != "step-42" {
		t.Errorf("expected plan_step_id 'step-42', got %v", data["plan_step_id"])
	}
}

// TestEventEmitterWithPlanStepID_NonMapData verifies that when Data is not
// map[string]any, plan_step_id injection is skipped without error.
func TestEventEmitterWithPlanStepID_NonMapData(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	base := NewEventEmitter("test-session", emit)
	scoped := base.WithPlanStepID("step-99")

	// SubAgentLaunch emits map[string]string (not map[string]any),
	// so plan_step_id injection should be skipped.
	scopedEmitter, ok := scoped.(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithPlanStepID")
	}
	scopedEmitter.SubAgentLaunch("step_1", "Install dependencies")

	data, ok := received.Data.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string data, got %T", received.Data)
	}
	// plan_step_id should NOT be injected because data is map[string]string
	if _, exists := data["plan_step_id"]; exists {
		t.Error("plan_step_id should not be injected into map[string]string data")
	}
}

// TestEventEmitterSetCurrentStepID verifies that SetCurrentStepID dynamically
// injects plan_step_id into subsequent events emitted by the same receiver,
// and that clearing with an empty string stops injection. This is used by the
// inline Conductor execution path to scope executor events to the current
// plan step without creating emitter copies.
func TestEventEmitterSetCurrentStepID(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	base := NewEventEmitter("test-session", emit)

	// Before scoping — no plan_step_id.
	base.Thought(1, "thinking", "")
	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if _, exists := data["plan_step_id"]; exists {
		t.Error("plan_step_id should NOT be injected before SetCurrentStepID")
	}

	// After scoping — plan_step_id injected.
	base.SetCurrentStepID("step-inline")
	base.Thought(2, "working", "")
	data, ok = received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if data["plan_step_id"] != "step-inline" {
		t.Errorf("expected plan_step_id 'step-inline', got %v", data["plan_step_id"])
	}

	// After clearing — no plan_step_id.
	base.SetCurrentStepID("")
	base.Thought(3, "done", "")
	data, ok = received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if _, exists := data["plan_step_id"]; exists {
		t.Error("plan_step_id should NOT be injected after SetCurrentStepID('')")
	}
}

// TestEventEmitterSetCurrentStepID_DoesNotAffectScopedCopies verifies that
// SetCurrentStepID on the original emitter does not change the planStepID of
// copies created by WithPlanStepID. Subagent emitters have their own fixed
// scope and must not be affected by the Conductor's dynamic inline scoping.
func TestEventEmitterSetCurrentStepID_DoesNotAffectScopedCopies(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	base := NewEventEmitter("test-session", emit)
	subScoped := base.WithPlanStepID("step-subagent")
	subEmitter, ok := subScoped.(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithPlanStepID")
	}

	// Dynamically scope the original (Conductor inline path).
	base.SetCurrentStepID("step-inline")

	// The subagent copy must still inject its own fixed plan_step_id.
	subEmitter.Thought(1, "subagent work", "")
	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if data["plan_step_id"] != "step-subagent" {
		t.Errorf("subagent copy should keep its own plan_step_id 'step-subagent', got %v", data["plan_step_id"])
	}

	// The original should use the dynamically-set plan_step_id.
	base.Thought(2, "conductor work", "")
	data, ok = received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if data["plan_step_id"] != "step-inline" {
		t.Errorf("original should use dynamic plan_step_id 'step-inline', got %v", data["plan_step_id"])
	}
}

// TestEventEmitterPlanStepStart verifies PlanStepStart emits correct event with progress.
func TestEventEmitterPlanStepStart(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.PlanStepStart("step-1", "Install dependencies", "Install deps")

	if received.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", received.SessionID)
	}
	if received.Type != "plan_step_start" {
		t.Errorf("expected type 'plan_step_start', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step_id"] != "step-1" {
		t.Errorf("expected step_id 'step-1', got %v", data["step_id"])
	}
	if data["description"] != "Install dependencies" {
		t.Errorf("expected description 'Install dependencies', got %v", data["description"])
	}
	// Progress fields should be present
	if data["completed_count"] != 0 {
		t.Errorf("expected completed_count 0, got %v", data["completed_count"])
	}
	if data["current_step_index"] != 0 {
		t.Errorf("expected current_step_index 0, got %v", data["current_step_index"])
	}
}

// TestEventEmitterPlanStepComplete verifies PlanStepComplete emits correct event with progress.
func TestEventEmitterPlanStepComplete(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	duration := 2500 * time.Millisecond
	emitter.PlanStepComplete("step-1", true, duration, "")

	if received.Type != "plan_step_complete" {
		t.Errorf("expected type 'plan_step_complete', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["step_id"] != "step-1" {
		t.Errorf("expected step_id 'step-1', got %v", data["step_id"])
	}
	if data["success"] != true {
		t.Errorf("expected success true, got %v", data["success"])
	}
	if data["duration"] != int64(2500) {
		t.Errorf("expected duration 2500, got %v", data["duration"])
	}
	// Progress fields should be present
	if data["completed_count"] != 1 {
		t.Errorf("expected completed_count 1, got %v", data["completed_count"])
	}
	if data["current_step_index"] != -1 {
		t.Errorf("expected current_step_index -1, got %v", data["current_step_index"])
	}
}

// TestEventEmitterPlanStepStartDuplicateSuppressed verifies that calling
// PlanStepStart twice for the same step only emits one event.
func TestEventEmitterPlanStepStartDuplicateSuppressed(t *testing.T) {
	var events []Event
	emit := func(e Event) {
		events = append(events, e)
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.PlanStepStart("step-1", "Do thing", "Do")
	emitter.PlanStepStart("step-1", "Do thing again", "Do2")

	planStarts := 0
	for _, e := range events {
		if e.Type == "plan_step_start" {
			planStarts++
		}
	}
	if planStarts != 1 {
		t.Errorf("expected 1 plan_step_start event, got %d", planStarts)
	}
}

// TestEventEmitterPlanStepStartAfterPlanGenerated verifies that PlanGenerated
// resets the started-set so steps can start again after a new plan.
func TestEventEmitterPlanStepStartAfterPlanGenerated(t *testing.T) {
	var events []Event
	emit := func(e Event) {
		events = append(events, e)
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.PlanStepStart("step-1", "First", "F")
	emitter.PlanGenerated(1, nil)
	emitter.PlanStepStart("step-1", "Second", "S")

	planStarts := 0
	for _, e := range events {
		if e.Type == "plan_step_start" {
			planStarts++
		}
	}
	if planStarts != 2 {
		t.Errorf("expected 2 plan_step_start events (before and after PlanGenerated), got %d", planStarts)
	}
}

// TestEventEmitterAssistantChunk verifies AssistantChunk emits correct event.
func TestEventEmitterAssistantChunk(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.AssistantChunk("Hello, how can")

	if received.Type != "assistant_chunk" {
		t.Errorf("expected type 'assistant_chunk', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["content"] != "Hello, how can" {
		t.Errorf("expected content 'Hello, how can', got %v", data["content"])
	}
}

// TestEventEmitterAssistantDone verifies AssistantDone emits correct event.
func TestEventEmitterAssistantDone(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.AssistantDone("Full response content", 150, 200)

	if received.Type != "assistant_done" {
		t.Errorf("expected type 'assistant_done', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["content"] != "Full response content" {
		t.Errorf("expected content 'Full response content', got %v", data["content"])
	}
	if data["input_tokens"] != 150 {
		t.Errorf("expected input_tokens 150, got %v", data["input_tokens"])
	}
	if data["output_tokens"] != 200 {
		t.Errorf("expected output_tokens 200, got %v", data["output_tokens"])
	}
}

// TestEventEmitterService verifies Service emits correct event.
func TestEventEmitterService(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.Service("Context compaction triggered")

	if received.Type != "service" {
		t.Errorf("expected type 'service', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["content"] != "Context compaction triggered" {
		t.Errorf("expected content 'Context compaction triggered', got %v", data["content"])
	}
}

// TestEventEmitterServiceWithMeta verifies ServiceWithMeta emits correct event with metadata.
func TestEventEmitterServiceWithMeta(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	meta := map[string]any{
		"compaction_type": "sliding",
		"tokens_freed":    5000,
	}
	emitter.ServiceWithMeta("Compaction complete", meta)

	if received.Type != "service" {
		t.Errorf("expected type 'service', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["content"] != "Compaction complete" {
		t.Errorf("expected content 'Compaction complete', got %v", data["content"])
	}
	if data["compaction_type"] != "sliding" {
		t.Errorf("expected compaction_type 'sliding', got %v", data["compaction_type"])
	}
	if data["tokens_freed"] != 5000 {
		t.Errorf("expected tokens_freed 5000, got %v", data["tokens_freed"])
	}
}

// TestEventEmitterServiceWithMeta_NilMeta verifies ServiceWithMeta works with nil metadata.
func TestEventEmitterServiceWithMeta_NilMeta(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.ServiceWithMeta("Simple service msg", nil)

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["content"] != "Simple service msg" {
		t.Errorf("expected content 'Simple service msg', got %v", data["content"])
	}
}

// TestEventEmitterWithRetryAttempt verifies WithRetryAttempt returns a scoped emitter
// that injects retry_attempt into map[string]any event data.
func TestEventEmitterWithRetryAttempt(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	base := NewEventEmitter("test-session", emit)
	scoped := base.WithRetryAttempt(2)

	scopedEmitter, ok := scoped.(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithRetryAttempt")
	}

	// ToolCall emits map[string]any so retry_attempt should be injected
	scopedEmitter.ToolCall(1, 0, "bash", "echo test", "core")

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if data["retry_attempt"] != 2 {
		t.Errorf("expected retry_attempt 2, got %v", data["retry_attempt"])
	}
}

// TestEventEmitterWithRetryAttempt_ZeroOmitted verifies retry_attempt is not injected when 0.
func TestEventEmitterWithRetryAttempt_ZeroOmitted(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	base := NewEventEmitter("test-session", emit)
	// No WithRetryAttempt call; retryAttempt defaults to 0
	base.ToolCall(1, 0, "bash", "echo test", "core")

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if _, exists := data["retry_attempt"]; exists {
		t.Error("retry_attempt should not be present when retryAttempt is 0")
	}
}

// TestEventEmitterToolCallID verifies ToolCall events contain tool_call_id and
// ToolResult events contain the matching tool_call_id.
func TestEventEmitterToolCallID(t *testing.T) {
	var events []Event
	emit := func(e Event) {
		events = append(events, e)
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.ToolCall(1, 0, "bash", "ls -la", "core")
	emitter.ToolResult(1, 0, 100, "file listing", false)

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Verify ToolCall has tool_call_id
	callData, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", events[0].Data)
	}
	toolCallID, ok := callData["tool_call_id"].(string)
	if !ok || toolCallID == "" {
		t.Fatal("expected non-empty tool_call_id in tool_call event")
	}

	// Verify ToolResult has matching tool_call_id
	resultData, ok := events[1].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", events[1].Data)
	}
	resultToolCallID, ok := resultData["tool_call_id"].(string)
	if !ok || resultToolCallID == "" {
		t.Fatal("expected non-empty tool_call_id in tool_result event")
	}
	if resultToolCallID != toolCallID {
		t.Errorf("tool_call_id mismatch: call=%q result=%q", toolCallID, resultToolCallID)
	}
}

// TestEventEmitterToolCallID_UniquePerCall verifies different (stepNum, callIdx)
// pairs get different tool_call_id values.
func TestEventEmitterToolCallID_UniquePerCall(t *testing.T) {
	var events []Event
	emit := func(e Event) {
		events = append(events, e)
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.ToolCall(1, 0, "bash", "ls", "core")
	emitter.ToolCall(1, 1, "bash", "pwd", "core")
	emitter.ToolCall(2, 0, "bash", "cat", "core")

	ids := make(map[string]bool)
	for _, evt := range events {
		data, ok := evt.Data.(map[string]any)
		if !ok {
			t.Fatal("expected Data to be map[string]any")
		}
		id, ok := data["tool_call_id"].(string)
		if !ok {
			t.Fatal("expected tool_call_id to be string")
		}
		if ids[id] {
			t.Errorf("duplicate tool_call_id: %q", id)
		}
		ids[id] = true
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 unique IDs, got %d", len(ids))
	}
}

// TestEventEmitterToolCallID_SharedAcrossScopes verifies scoped copies share the
// same counter so IDs are globally unique within a session.
func TestEventEmitterToolCallID_SharedAcrossScopes(t *testing.T) {
	var events []Event
	var mu sync.Mutex
	emit := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}

	base := NewEventEmitter("test-session", emit)

	// Create scoped copies
	stepScoped, ok := base.WithPlanStepID("step-1").(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithPlanStepID")
	}
	retryScoped, ok := base.WithRetryAttempt(2).(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithRetryAttempt")
	}

	// Emit from different scopes
	base.ToolCall(1, 0, "bash", "ls", "core")
	stepScoped.ToolCall(1, 0, "bash", "pwd", "core")
	retryScoped.ToolCall(1, 0, "bash", "cat", "core")

	mu.Lock()
	defer mu.Unlock()

	ids := make(map[string]bool)
	for _, evt := range events {
		data, ok := evt.Data.(map[string]any)
		if !ok {
			t.Fatal("expected Data to be map[string]any")
		}
		id, ok := data["tool_call_id"].(string)
		if !ok {
			t.Fatal("expected tool_call_id to be string")
		}
		if ids[id] {
			t.Errorf("duplicate tool_call_id across scopes: %q", id)
		}
		ids[id] = true
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 unique IDs across scopes, got %d", len(ids))
	}
}

// TestEventEmitterToolResult_NoToolCallID verifies ToolResult without prior
// ToolCall omits tool_call_id rather than including empty string.
func TestEventEmitterToolResult_NoToolCallID(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	// Call ToolResult without prior ToolCall for this (step, callIdx)
	emitter.ToolResult(99, 0, 50, "orphan result", false)

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if _, exists := data["tool_call_id"]; exists {
		t.Error("tool_call_id should not be present when no prior ToolCall was made")
	}
}

// TestEventEmitterToolCallIDSink verifies that the post-ToolCall sink receives
// the tool name and the exact tool_call_id emitted in the tool_call event. The
// Manager uses this to let the desktop confirm callback attach the matching
// tool_call_id to the tool_confirm payload.
func TestEventEmitterToolCallIDSink(t *testing.T) {
	var events []Event
	emit := func(e Event) { events = append(events, e) }
	emitter := NewEventEmitter("test-session", emit)

	var sinkTool, sinkID string
	emitter.SetToolCallIDSink(func(tool, toolCallID string) {
		sinkTool, sinkID = tool, toolCallID
	})

	emitter.ToolCall(1, 0, "bash_exec", `{"command":"ls"}`, "core")

	if sinkTool != "bash_exec" {
		t.Errorf("sink tool = %q, want bash_exec", sinkTool)
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatal("expected map[string]any data")
	}
	eventID, _ := data["tool_call_id"].(string)
	if sinkID == "" {
		t.Fatal("sink did not receive a tool_call_id")
	}
	if sinkID != eventID {
		t.Errorf("sink id %q != event tool_call_id %q", sinkID, eventID)
	}
}

// TestEventEmitterToolCallIDSink_NoSink verifies ToolCall is a no-op (no panic)
// when no sink is registered.
func TestEventEmitterToolCallIDSink_NoSink(t *testing.T) {
	emit := func(Event) {}
	emitter := NewEventEmitter("test-session", emit)
	// No SetToolCallIDSink — must not panic.
	emitter.ToolCall(1, 0, "bash", "ls", "core")
}

// TestEventEmitterWithRetryAttempt_PreservedByWithPlanStepID verifies that
// retryAttempt is preserved when creating a plan-step-scoped copy.
func TestEventEmitterWithRetryAttempt_PreservedByWithPlanStepID(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	base := NewEventEmitter("test-session", emit)
	retryScoped := base.WithRetryAttempt(3)

	// Now scope by plan step — retryAttempt should be preserved
	retryEmitter, ok := retryScoped.(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithRetryAttempt")
	}
	stepScoped := retryEmitter.WithPlanStepID("step-42")

	stepEmitter, ok := stepScoped.(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithPlanStepID")
	}

	stepEmitter.ToolCall(1, 0, "bash", "echo test", "core")

	data, ok := received.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any data, got %T", received.Data)
	}
	if data["plan_step_id"] != "step-42" {
		t.Errorf("expected plan_step_id 'step-42', got %v", data["plan_step_id"])
	}
	if data["retry_attempt"] != 3 {
		t.Errorf("expected retry_attempt 3, got %v", data["retry_attempt"])
	}
}
