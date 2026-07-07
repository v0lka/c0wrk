package agent

import (
	"testing"
	"time"
)

// Test each NoopEvents method individually — the existing test calls all at once
// in one function. Individual calls exercise the per-method 0% coverage lines.

func TestNoopEvents_StepStart(t *testing.T) {
	n := &NoopEvents{}
	n.StepStart(42)
}

func TestNoopEvents_Thought(t *testing.T) {
	n := &NoopEvents{}
	n.Thought(1, "content", "reasoning")
}

func TestNoopEvents_ToolCall(t *testing.T) {
	n := &NoopEvents{}
	n.ToolCall(1, 0, "tool", `{"arg":"val"}`, "core")
}

func TestNoopEvents_ToolResult(t *testing.T) {
	n := &NoopEvents{}
	n.ToolResult(1, 0, 42, "preview", false)
}

func TestNoopEvents_StepComplete(t *testing.T) {
	n := &NoopEvents{}
	n.StepComplete(1, 100*time.Millisecond)
}

func TestNoopEvents_SubAgentLaunch(t *testing.T) {
	n := &NoopEvents{}
	n.SubAgentLaunch("step_1", "do something")
}

func TestNoopEvents_SubAgentComplete(t *testing.T) {
	n := &NoopEvents{}
	n.SubAgentComplete("step_1", true, 200*time.Millisecond)
}

func TestNoopEvents_AssistantChunk(t *testing.T) {
	n := &NoopEvents{}
	n.AssistantChunk("partial")
}

func TestNoopEvents_AssistantDone(t *testing.T) {
	n := &NoopEvents{}
	n.AssistantDone("full", 100, 50)
}

func TestNoopEvents_ContextFill(t *testing.T) {
	n := &NoopEvents{}
	n.ContextFill(0.5, 5000, 10000, "ok", "")
}

func TestNoopEvents_Finishing(t *testing.T) {
	n := &NoopEvents{}
	n.Finishing(1, "summary text")
}

func TestNoopEvents_ContextCompaction(t *testing.T) {
	n := &NoopEvents{}
	n.ContextCompaction(85.0, 30.0, "step_1")
}

func TestNoopEvents_ExecutorDiagnostic(t *testing.T) {
	n := &NoopEvents{}
	n.ExecutorDiagnostic(1, "test_event", map[string]any{"key": "value"})
}

func TestNoopEvents_AllMethods_NoPanic(t *testing.T) {
	n := &NoopEvents{}
	var _ Events = n

	funcs := []func(){
		func() { n.StepStart(1) },
		func() { n.Thought(1, "thinking", "reasoning") },
		func() { n.ToolCall(1, 0, "tool", `{"arg":"val"}`, "core") },
		func() { n.ToolResult(1, 0, 42, "preview", false) },
		func() { n.StepComplete(1, 100*time.Millisecond) },
		func() { n.SubAgentLaunch("step_1", "do") },
		func() { n.SubAgentComplete("step_1", true, 200*time.Millisecond) },
		func() { n.AssistantChunk("partial") },
		func() { n.AssistantDone("full", 100, 50) },
		func() { n.ContextFill(0.5, 5000, 10000, "ok", "") },
		func() { n.Finishing(1, "summary") },
		func() { n.ContextCompaction(85.0, 30.0, "step_1") },
		func() { n.ExecutorDiagnostic(1, "event", map[string]any{"k": "v"}) },
	}
	for i, fn := range funcs {
		fn() // should not panic
		t.Logf("method %d: no panic", i)
	}
}
