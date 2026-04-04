package session

import (
	"sync"
	"testing"
	"time"

	"github.com/user/agent/internal/core"
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

	data, ok := received.Data.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string data, got %T", received.Data)
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
	emitter.PlanGenerated(5, []core.PlanStepEvent{
		{Description: "Step 1", Status: "pending"},
	})

	if received.Type != "plan_generated" {
		t.Errorf("expected type 'plan_generated', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
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

	data, ok := received.Data.(map[string]interface{})
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

	data, ok := received.Data.(map[string]interface{})
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
	emitter.ToolCall(2, "bash", "ls -la")

	if received.Type != "tool_call" {
		t.Errorf("expected type 'tool_call', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
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
	emitter.ToolResult(2, 1024, "preview content")

	if received.Type != "tool_result" {
		t.Errorf("expected type 'tool_result', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
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

	data, ok := received.Data.(map[string]interface{})
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

	data, ok := received.Data.(map[string]interface{})
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

// TestEventEmitterEvaluation verifies Evaluation emits correct event.
func TestEventEmitterEvaluation(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.Evaluation(3, 5, []core.EvalCriterionEvent{
		{Name: "ac_1", Description: "Test criterion", Passed: true},
	})

	if received.Type != "evaluation" {
		t.Errorf("expected type 'evaluation', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["passed"] != 3 {
		t.Errorf("expected passed 3, got %v", data["passed"])
	}
	if data["total"] != 5 {
		t.Errorf("expected total 5, got %v", data["total"])
	}
}

// TestEventEmitterReflection verifies Reflection emits correct event.
func TestEventEmitterReflection(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.Reflection("Test failed due to missing dependency", []string{"Missing import"}, 1, 3)

	if received.Type != "reflection" {
		t.Errorf("expected type 'reflection', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
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

// TestEventEmitterEscalation verifies Escalation emits correct event.
func TestEventEmitterEscalation(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.Escalation("direct", "react")

	if received.Type != "escalation" {
		t.Errorf("expected type 'escalation', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string data, got %T", received.Data)
	}
	if data["from_mode"] != "direct" {
		t.Errorf("expected from_mode 'direct', got %q", data["from_mode"])
	}
	if data["to_mode"] != "react" {
		t.Errorf("expected to_mode 'react', got %q", data["to_mode"])
	}
}

// TestEventEmitterACExtracted verifies ACExtracted emits correct event.
func TestEventEmitterACExtracted(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	criteria := []core.EvalCriterionEvent{
		{Name: "ac_1", Description: "First criterion"},
		{Name: "ac_2", Description: "Second criterion"},
		{Name: "ac_3", Description: "Third criterion"},
		{Name: "ac_4", Description: "Fourth criterion"},
	}
	emitter.ACExtracted(4, criteria)

	if received.Type != "ac_extracted" {
		t.Errorf("expected type 'ac_extracted', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	count, ok := data["count"].(int)
	if !ok || count != 4 {
		t.Errorf("expected count 4, got %v", data["count"])
	}
	receivedCriteria, ok := data["criteria"].([]core.EvalCriterionEvent)
	if !ok {
		t.Fatalf("expected criteria slice, got %T", data["criteria"])
	}
	if len(receivedCriteria) != 4 {
		t.Errorf("expected 4 criteria, got %d", len(receivedCriteria))
	}
	if receivedCriteria[0].Name != "ac_1" || receivedCriteria[0].Description != "First criterion" {
		t.Errorf("first criterion mismatch: %+v", receivedCriteria[0])
	}
}

// TestEventEmitterContextFill verifies ContextFill emits correct event.
func TestEventEmitterContextFill(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.ContextFill(75.5, 75500, 100000, "compact")

	if received.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", received.SessionID)
	}
	if received.Type != "context_fill" {
		t.Errorf("expected type 'context_fill', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["fill_percent"] != 75.5 {
		t.Errorf("expected fill_percent 75.5, got %v", data["fill_percent"])
	}
	if data["used_tokens"] != 75500 {
		t.Errorf("expected used_tokens 75500, got %v", data["used_tokens"])
	}
	if data["max_tokens"] != 100000 {
		t.Errorf("expected max_tokens 100000, got %v", data["max_tokens"])
	}
	if data["status"] != "compact" {
		t.Errorf("expected status 'compact', got %v", data["status"])
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
			emitter.ToolCall(step, "bash", "echo test")
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
	emitter.PlanGenerated(5, []core.PlanStepEvent{{Description: "Step 1", Status: "pending"}})
	emitter.StepStart(1)
	emitter.Thought(1, "I need to think about this", "")
	emitter.ToolCall(1, "bash", "ls")
	emitter.ToolResult(1, 100, "")
	emitter.StepComplete(1, time.Second)
	emitter.SubAgentLaunch("step_1", "Do something")
	emitter.SubAgentComplete("step_1", true, time.Second)
	emitter.Evaluation(3, 5, []core.EvalCriterionEvent{{Name: "ac_1", Description: "Test", Passed: true}})
	emitter.Reflection("Something went wrong", []string{"Issue found"}, 1, 3)
	emitter.Retry(2, 3)
	emitter.Escalation("direct", "react")
	emitter.ACExtracted(4, []core.EvalCriterionEvent{{Name: "ac_1", Description: "Test"}})
	emitter.ContextFill(75.5, 75500, 100000, "compact")

	mu.Lock()
	if len(events) != 15 {
		t.Errorf("expected 15 events, got %d", len(events))
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
		"evaluation":        false,
		"reflection":        false,
		"retry":             false,
		"escalation":        false,
		"ac_extracted":      false,
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
// that injects plan_step_id into map[string]interface{} event data.
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

	// PlanGenerated emits map[string]interface{} so plan_step_id should be injected
	scopedEmitter.PlanGenerated(2, []core.PlanStepEvent{
		{Description: "Do stuff", Status: "pending"},
	})

	if received.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", received.SessionID)
	}

	data, ok := received.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["plan_step_id"] != "step-42" {
		t.Errorf("expected plan_step_id 'step-42', got %v", data["plan_step_id"])
	}
}

// TestEventEmitterWithPlanStepID_NonMapData verifies that when Data is not
// map[string]interface{}, plan_step_id injection is skipped without error.
func TestEventEmitterWithPlanStepID_NonMapData(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	base := NewEventEmitter("test-session", emit)
	scoped := base.WithPlanStepID("step-99")

	// Routing emits map[string]string — not map[string]interface{}
	scopedEmitter, ok := scoped.(*EventEmitter)
	if !ok {
		t.Fatal("expected *EventEmitter from WithPlanStepID")
	}
	scopedEmitter.Routing("direct", "general", "1")

	data, ok := received.Data.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string data, got %T", received.Data)
	}
	// plan_step_id should NOT be present in map[string]string
	if _, exists := data["plan_step_id"]; exists {
		t.Error("plan_step_id should not be injected into map[string]string data")
	}
}

// TestEventEmitterPlanStepStart verifies PlanStepStart emits correct event.
func TestEventEmitterPlanStepStart(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	emitter.PlanStepStart("step-1", "Install dependencies")

	if received.SessionID != "test-session" {
		t.Errorf("expected session_id 'test-session', got %q", received.SessionID)
	}
	if received.Type != "plan_step_start" {
		t.Errorf("expected type 'plan_step_start', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string data, got %T", received.Data)
	}
	if data["step_id"] != "step-1" {
		t.Errorf("expected step_id 'step-1', got %q", data["step_id"])
	}
	if data["description"] != "Install dependencies" {
		t.Errorf("expected description 'Install dependencies', got %q", data["description"])
	}
}

// TestEventEmitterPlanStepComplete verifies PlanStepComplete emits correct event.
func TestEventEmitterPlanStepComplete(t *testing.T) {
	var received Event
	emit := func(e Event) {
		received = e
	}

	emitter := NewEventEmitter("test-session", emit)
	duration := 2500 * time.Millisecond
	emitter.PlanStepComplete("step-1", true, duration)

	if received.Type != "plan_step_complete" {
		t.Errorf("expected type 'plan_step_complete', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
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

	data, ok := received.Data.(map[string]interface{})
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

	data, ok := received.Data.(map[string]interface{})
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

	data, ok := received.Data.(map[string]interface{})
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
	meta := map[string]interface{}{
		"compaction_type": "sliding",
		"tokens_freed":    5000,
	}
	emitter.ServiceWithMeta("Compaction complete", meta)

	if received.Type != "service" {
		t.Errorf("expected type 'service', got %q", received.Type)
	}

	data, ok := received.Data.(map[string]interface{})
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

	data, ok := received.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{} data, got %T", received.Data)
	}
	if data["content"] != "Simple service msg" {
		t.Errorf("expected content 'Simple service msg', got %v", data["content"])
	}
}
