package agent

import (
	"testing"
	"time"
)

func TestNoopEvents_NoPanic(t *testing.T) {
	n := &NoopEvents{}

	// Verify interface compliance
	var _ Events = n

	// Call every method — none should panic
	n.StepStart(1)
	n.Thought(1, "thinking", "reasoning")
	n.ToolCall(1, 0, "tool", `{"arg":"val"}`, "core")
	n.ToolResult(1, 0, 42, "preview", false)
	n.StepComplete(1, 100*time.Millisecond)
	n.SubAgentLaunch("step_1", "do something")
	n.SubAgentComplete("step_1", true, 200*time.Millisecond)
	n.AssistantChunk("partial")
	n.AssistantDone("full", 100, 50)
	n.ContextFill(0.5, 5000, 10000, "ok", "")
	n.ContextCompaction(85.0, 30.0, "step_1")
}
