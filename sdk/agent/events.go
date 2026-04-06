package agent

import "time"

// AgentEvents defines universal agent lifecycle events.
// Any agent system (not just c0wrk) can implement this interface.
type AgentEvents interface {
	StepStart(stepNum int)
	Thought(stepNum int, content, reasoning string)
	ToolCall(stepNum int, toolName, argsPreview string)
	ToolResult(stepNum int, resultLen int, preview string)
	StepComplete(stepNum int, duration time.Duration)
	SubAgentLaunch(stepID, description string)
	SubAgentComplete(stepID string, success bool, duration time.Duration)
	AssistantChunk(content string)
	AssistantDone(content string, inputTokens, outputTokens int)
	TokensUsed(inputTokens, outputTokens int)
	ContextFill(fillPercent float64, usedTokens, maxTokens int, status string, stepID string)
}

// NoopEvents is a no-op implementation of AgentEvents.
type NoopEvents struct{}

var _ AgentEvents = (*NoopEvents)(nil)

func (n *NoopEvents) StepStart(_ int)                                    {}
func (n *NoopEvents) Thought(_ int, _, _ string)                         {}
func (n *NoopEvents) ToolCall(_ int, _, _ string)                        {}
func (n *NoopEvents) ToolResult(_, _ int, _ string)                      {}
func (n *NoopEvents) StepComplete(_ int, _ time.Duration)                {}
func (n *NoopEvents) SubAgentLaunch(_, _ string)                         {}
func (n *NoopEvents) SubAgentComplete(_ string, _ bool, _ time.Duration) {}
func (n *NoopEvents) AssistantChunk(_ string)                            {}
func (n *NoopEvents) AssistantDone(_ string, _, _ int)                   {}
func (n *NoopEvents) TokensUsed(_, _ int)                                {}
func (n *NoopEvents) ContextFill(_ float64, _, _ int, _, _ string) {}
