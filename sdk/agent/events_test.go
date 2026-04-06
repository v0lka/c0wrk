package agent

import (
	"testing"
	"time"
)

func TestNoopEvents_NoPanic(t *testing.T) {
	n := &NoopEvents{}

	// Verify interface compliance
	var _ AgentEvents = n

	// Call every method — none should panic
	n.StepStart(1)
	n.Thought(1, "thinking", "reasoning")
	n.ToolCall(1, "tool", `{"arg":"val"}`)
	n.ToolResult(1, 42, "preview")
	n.StepComplete(1, 100*time.Millisecond)
	n.SubAgentLaunch("step_1", "do something")
	n.SubAgentComplete("step_1", true, 200*time.Millisecond)
	n.AssistantChunk("partial")
	n.AssistantDone("full", 100, 50)
	n.ContextFill(0.5, 5000, 10000, "ok", "")
}
