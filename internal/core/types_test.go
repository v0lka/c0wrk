package core

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestRoutingDecision_JSONRoundTrip(t *testing.T) {
	original := RoutingDecision{
		Mode:               "plan_execute",
		Domain:             "code",
		Complexity:         4,
		CompactionStrategy: "summarization",
		SuggestedTools:     []string{"read_file", "write_file", "run_tests"},
		NeedsClarification: false,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal RoutingDecision: %v", err)
	}

	var decoded RoutingDecision
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal RoutingDecision: %v", err)
	}

	if decoded.Mode != original.Mode {
		t.Errorf("Mode mismatch: got %q, want %q", decoded.Mode, original.Mode)
	}
	if decoded.Domain != original.Domain {
		t.Errorf("Domain mismatch: got %q, want %q", decoded.Domain, original.Domain)
	}
	if decoded.Complexity != original.Complexity {
		t.Errorf("Complexity mismatch: got %d, want %d", decoded.Complexity, original.Complexity)
	}
	if decoded.CompactionStrategy != original.CompactionStrategy {
		t.Errorf("CompactionStrategy mismatch: got %q, want %q", decoded.CompactionStrategy, original.CompactionStrategy)
	}
	if len(decoded.SuggestedTools) != len(original.SuggestedTools) {
		t.Errorf("SuggestedTools length mismatch: got %d, want %d", len(decoded.SuggestedTools), len(original.SuggestedTools))
	}
	for i, tool := range decoded.SuggestedTools {
		if tool != original.SuggestedTools[i] {
			t.Errorf("SuggestedTools[%d] mismatch: got %q, want %q", i, tool, original.SuggestedTools[i])
		}
	}
	if decoded.NeedsClarification != original.NeedsClarification {
		t.Errorf("NeedsClarification mismatch: got %v, want %v", decoded.NeedsClarification, original.NeedsClarification)
	}
}

func TestPlan_JSONRoundTrip(t *testing.T) {
	original := Plan{
		Steps: []PlanStep{
			{
				ID:             "step_1",
				Description:    "Read and analyze requirements",
				DependsOn:      nil,
				Parallelizable: false,
				EstimatedTools: []string{"read_file"},
				RelevantAC:     []string{"ac_1"},
			},
			{
				ID:             "step_2a",
				Description:    "Implement core logic",
				DependsOn:      []string{"step_1"},
				Parallelizable: true,
				EstimatedTools: []string{"read_file", "write_file"},
				RelevantAC:     []string{"ac_1", "ac_2"},
			},
			{
				ID:             "step_2b",
				Description:    "Write tests",
				DependsOn:      []string{"step_1"},
				Parallelizable: true,
				EstimatedTools: []string{"write_file"},
				RelevantAC:     []string{"ac_3"},
			},
			{
				ID:             "step_3",
				Description:    "Run and verify tests",
				DependsOn:      []string{"step_2a", "step_2b"},
				Parallelizable: false,
				EstimatedTools: []string{"run_tests"},
				RelevantAC:     []string{"ac_1", "ac_2", "ac_3"},
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal Plan: %v", err)
	}

	var decoded Plan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Plan: %v", err)
	}

	if len(decoded.Steps) != len(original.Steps) {
		t.Fatalf("Steps length mismatch: got %d, want %d", len(decoded.Steps), len(original.Steps))
	}

	for i, step := range decoded.Steps {
		orig := original.Steps[i]
		if step.ID != orig.ID {
			t.Errorf("Steps[%d].ID mismatch: got %q, want %q", i, step.ID, orig.ID)
		}
		if step.Description != orig.Description {
			t.Errorf("Steps[%d].Description mismatch: got %q, want %q", i, step.Description, orig.Description)
		}
		if len(step.DependsOn) != len(orig.DependsOn) {
			t.Errorf("Steps[%d].DependsOn length mismatch: got %d, want %d", i, len(step.DependsOn), len(orig.DependsOn))
		}
		for j, dep := range step.DependsOn {
			if dep != orig.DependsOn[j] {
				t.Errorf("Steps[%d].DependsOn[%d] mismatch: got %q, want %q", i, j, dep, orig.DependsOn[j])
			}
		}
		if step.Parallelizable != orig.Parallelizable {
			t.Errorf("Steps[%d].Parallelizable mismatch: got %v, want %v", i, step.Parallelizable, orig.Parallelizable)
		}
	}
}

func TestAcceptanceCriterion_JSONRoundTrip(t *testing.T) {
	original := AcceptanceCriterion{
		ID:          "ac_1",
		Description: "All unit tests pass",
		CheckType:   "programmatic",
		CheckCmd:    "go test ./...",
		StepHint:    "Run tests after implementation",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal AcceptanceCriterion: %v", err)
	}

	var decoded AcceptanceCriterion
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal AcceptanceCriterion: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Description != original.Description {
		t.Errorf("Description mismatch: got %q, want %q", decoded.Description, original.Description)
	}
	if decoded.CheckType != original.CheckType {
		t.Errorf("CheckType mismatch: got %q, want %q", decoded.CheckType, original.CheckType)
	}
	if decoded.CheckCmd != original.CheckCmd {
		t.Errorf("CheckCmd mismatch: got %q, want %q", decoded.CheckCmd, original.CheckCmd)
	}
	if decoded.StepHint != original.StepHint {
		t.Errorf("StepHint mismatch: got %q, want %q", decoded.StepHint, original.StepHint)
	}
}

func TestReflection_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second) // Truncate for JSON precision
	original := Reflection{
		FailureAnalysis: "Test failed due to nil pointer dereference",
		RootCause:       "Missing nil check before accessing struct field",
		ActionPlan:      "Add nil check in function X before accessing field Y",
		Timestamp:       now,
		TaskType:        "code_fix",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal Reflection: %v", err)
	}

	var decoded Reflection
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Reflection: %v", err)
	}

	if decoded.FailureAnalysis != original.FailureAnalysis {
		t.Errorf("FailureAnalysis mismatch: got %q, want %q", decoded.FailureAnalysis, original.FailureAnalysis)
	}
	if decoded.RootCause != original.RootCause {
		t.Errorf("RootCause mismatch: got %q, want %q", decoded.RootCause, original.RootCause)
	}
	if decoded.ActionPlan != original.ActionPlan {
		t.Errorf("ActionPlan mismatch: got %q, want %q", decoded.ActionPlan, original.ActionPlan)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", decoded.Timestamp, original.Timestamp)
	}
	if decoded.TaskType != original.TaskType {
		t.Errorf("TaskType mismatch: got %q, want %q", decoded.TaskType, original.TaskType)
	}
}

func TestSharedWorkspace_StoreAndGet(t *testing.T) {
	ws := NewSharedWorkspace()

	// Store an artifact
	ws.Store("test_key", "test content", "step_1")

	// Get it back
	artifact, ok := ws.Get("test_key")
	if !ok {
		t.Fatal("expected to find artifact, but got none")
	}

	if artifact.Key != "test_key" {
		t.Errorf("Key mismatch: got %q, want %q", artifact.Key, "test_key")
	}
	if artifact.Content != "test content" {
		t.Errorf("Content mismatch: got %q, want %q", artifact.Content, "test content")
	}
	if artifact.ProducedBy != "step_1" {
		t.Errorf("ProducedBy mismatch: got %q, want %q", artifact.ProducedBy, "step_1")
	}
	if artifact.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func TestSharedWorkspace_GetByProducer(t *testing.T) {
	ws := NewSharedWorkspace()

	// Store multiple artifacts from different producers
	ws.Store("key1", "content1", "step_1")
	ws.Store("key2", "content2", "step_1")
	ws.Store("key3", "content3", "step_2")

	// Get artifacts by producer
	step1Artifacts := ws.GetByProducer("step_1")
	if len(step1Artifacts) != 2 {
		t.Errorf("expected 2 artifacts from step_1, got %d", len(step1Artifacts))
	}

	step2Artifacts := ws.GetByProducer("step_2")
	if len(step2Artifacts) != 1 {
		t.Errorf("expected 1 artifact from step_2, got %d", len(step2Artifacts))
	}

	// Non-existent producer
	noArtifacts := ws.GetByProducer("step_999")
	if len(noArtifacts) != 0 {
		t.Errorf("expected 0 artifacts from non-existent producer, got %d", len(noArtifacts))
	}
}

func TestSharedWorkspace_List(t *testing.T) {
	ws := NewSharedWorkspace()

	// Empty workspace
	if len(ws.List()) != 0 {
		t.Error("expected empty workspace to return empty list")
	}

	// Add artifacts
	ws.Store("key1", "content1", "step_1")
	ws.Store("key2", "content2", "step_1")

	list := ws.List()
	if len(list) != 2 {
		t.Errorf("expected 2 artifacts, got %d", len(list))
	}
}

func TestSharedWorkspace_ConcurrentAccess(t *testing.T) {
	ws := NewSharedWorkspace()
	done := make(chan bool)

	// Concurrent writers
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("key_%d_%d", id, j)
				ws.Store(key, "content", fmt.Sprintf("step_%d", id))
			}
			done <- true
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				ws.List()
				ws.GetByProducer("step_1")
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 15; i++ {
		<-done
	}

	// Verify final state
	list := ws.List()
	if len(list) != 1000 {
		t.Errorf("expected 1000 artifacts, got %d", len(list))
	}
}

func TestDefaultAgentProfile(t *testing.T) {
	profile := DefaultAgentProfile()
	if profile.Role != "executor" {
		t.Errorf("expected role 'executor', got %q", profile.Role)
	}
}

func TestPlanStep_WithAgentProfile(t *testing.T) {
	profile := &AgentProfile{
		Role:         "researcher",
		AllowedTools: []string{"web_search", "web_fetch"},
	}

	step := PlanStep{
		ID:           "step_1",
		Description:  "Research topic",
		AgentProfile: profile,
	}

	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("failed to marshal PlanStep: %v", err)
	}

	var decoded PlanStep
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal PlanStep: %v", err)
	}

	if decoded.AgentProfile == nil {
		t.Fatal("expected AgentProfile to be non-nil")
	}
	if decoded.AgentProfile.Role != "researcher" {
		t.Errorf("Role mismatch: got %q, want %q", decoded.AgentProfile.Role, "researcher")
	}
}

func TestPlanStep_WithoutAgentProfile(t *testing.T) {
	step := PlanStep{
		ID:          "step_1",
		Description: "Do something",
	}

	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("failed to marshal PlanStep: %v", err)
	}

	var decoded PlanStep
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal PlanStep: %v", err)
	}

	if decoded.AgentProfile != nil {
		t.Error("expected AgentProfile to be nil when not set")
	}
}

func TestScopeEmitterToStep_WithPlanStepScopable(t *testing.T) {
	base := &scopableMockEmitter{}
	scoped := scopeEmitterToStep(base, "step_42")

	if scoped == base {
		t.Error("expected a new scoped emitter, got the same one")
	}

	sm, ok := scoped.(*scopableMockEmitter)
	if !ok {
		t.Fatal("expected scoped emitter to be *scopableMockEmitter")
	}
	if sm.scopedStepID != "step_42" {
		t.Errorf("expected scopedStepID='step_42', got %q", sm.scopedStepID)
	}
}

func TestScopeEmitterToStep_WithoutPlanStepScopable(t *testing.T) {
	base := &noopEmitter{}
	scoped := scopeEmitterToStep(base, "step_1")
	if scoped != base {
		t.Error("expected same emitter when PlanStepScopable is not implemented")
	}
}

func TestNoopEmitter_AllMethodsAreNoop(t *testing.T) {
	e := &noopEmitter{}
	// Call all methods - none should panic
	e.Routing("direct", "code", "low")
	e.PlanGenerated(3, []PlanStepEvent{{ID: "s1", Description: "d", Status: "pending"}})
	e.PlanStepStart("s1", "desc")
	e.PlanStepComplete("s1", true, time.Second)
	e.StepStart(1)
	e.Thought(1, "content", "reasoning")
	e.ToolCall(1, "tool", "args")
	e.ToolResult(1, 100, "preview")
	e.StepComplete(1, time.Second)
	e.SubAgentLaunch("s1", "desc")
	e.SubAgentComplete("s1", true, time.Second)
	e.Evaluation(2, 3, nil)
	e.Reflection("summary", []string{"insight"}, 1, 3)
	e.Retry(1, 3)
	e.Escalation("direct", "plan_execute")
	e.ACExtracted(2, nil)
	e.AssistantChunk("chunk")
	e.AssistantDone("full", 100, 50)
	e.ContextFill(0.5, 50000, 100000, "ok")
	e.Service("msg")
	e.ServiceWithMeta("msg", map[string]interface{}{"key": "val"})
}

// scopableMockEmitter implements both Emitter and PlanStepScopable for testing.
type scopableMockEmitter struct {
	noopEmitter
	scopedStepID string
}

func (s *scopableMockEmitter) WithPlanStepID(id string) Emitter {
	return &scopableMockEmitter{scopedStepID: id}
}

func TestSharedWorkspace_Get_NotFound(t *testing.T) {
	ws := NewSharedWorkspace()
	_, ok := ws.Get("nonexistent")
	if ok {
		t.Error("expected ok=false for nonexistent key")
	}
}

func TestSharedWorkspace_Store_Overwrite(t *testing.T) {
	ws := NewSharedWorkspace()
	ws.Store("key1", "content1", "step_1")
	ws.Store("key1", "content2", "step_2")

	a, ok := ws.Get("key1")
	if !ok {
		t.Fatal("expected to find artifact")
	}
	if a.Content != "content2" {
		t.Errorf("expected overwritten content 'content2', got %q", a.Content)
	}
	if a.ProducedBy != "step_2" {
		t.Errorf("expected producer 'step_2', got %q", a.ProducedBy)
	}
}

func TestSharedWorkspace_Clear(t *testing.T) {
	ws := NewSharedWorkspace()
	ws.Store("key1", "content1", "step_1")
	ws.Store("key2", "content2", "step_2")

	// Verify artifacts exist
	if len(ws.List()) != 2 {
		t.Fatalf("expected 2 artifacts before clear, got %d", len(ws.List()))
	}

	// Clear
	ws.Clear()

	// Verify empty
	if len(ws.List()) != 0 {
		t.Fatalf("expected 0 artifacts after clear, got %d", len(ws.List()))
	}

	// Verify we can still store after clear
	ws.Store("key3", "content3", "step_3")
	if len(ws.List()) != 1 {
		t.Fatalf("expected 1 artifact after re-store, got %d", len(ws.List()))
	}
}
