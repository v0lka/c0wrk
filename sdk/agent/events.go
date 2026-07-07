package agent

import "time"

// Events defines universal agent lifecycle events.
// Any agent system (not just the host application) can implement this interface.
//
// Concurrency: when sub-agents run in parallel (RunSubAgentsParallel), each
// sub-agent goroutine invokes emitter methods concurrently. Implementations
// MUST be safe for concurrent use (e.g. guard shared state with a mutex).
//
// BREAKING CHANGE (v0.x): Finishing(stepNum int, summary string) was added.
// All implementations of Events (and orchestration.Events, which embeds it)
// MUST implement this method or fail to compile. A no-op stub is provided by
// NoopEvents for struct embedding convenience.
type Events interface {
	StepStart(stepNum int)
	Thought(stepNum int, content, reasoning string)
	ToolCall(stepNum, callIdx int, toolName, argsPreview, source string)
	ToolResult(stepNum, callIdx, resultLen int, preview string, isError bool)
	StepComplete(stepNum int, duration time.Duration)
	SubAgentLaunch(stepID, description string)
	SubAgentComplete(stepID string, success bool, duration time.Duration)
	AssistantChunk(content string)
	AssistantDone(content string, inputTokens, outputTokens int)
	ContextFill(fillPercent float64, usedTokens, maxTokens int, status string, stepID string)
	ContextCompaction(beforePercent, afterPercent float64, stepID string)

	Finishing(stepNum int, summary string)

	// ExecutorDiagnostic reports internal executor lifecycle events (nudges, circuit breakers,
	// truncation, compaction errors, parse errors). The event parameter identifies
	// what happened and details carries structured data.
	ExecutorDiagnostic(stepNum int, event string, details map[string]any)
}

// NoopEvents is a no-op implementation of Events.
type NoopEvents struct{}

var _ Events = (*NoopEvents)(nil)

// StepStart is a no-op.
func (n *NoopEvents) StepStart(_ int) {}

// Thought is a no-op.
func (n *NoopEvents) Thought(_ int, _, _ string) {}

// ToolCall is a no-op.
func (n *NoopEvents) ToolCall(_, _ int, _, _, _ string) {}

// ToolResult is a no-op.
func (n *NoopEvents) ToolResult(_, _, _ int, _ string, _ bool) {}

// StepComplete is a no-op.
func (n *NoopEvents) StepComplete(_ int, _ time.Duration) {}

// SubAgentLaunch is a no-op.
func (n *NoopEvents) SubAgentLaunch(_, _ string) {}

// SubAgentComplete is a no-op.
func (n *NoopEvents) SubAgentComplete(_ string, _ bool, _ time.Duration) {}

// AssistantChunk is a no-op.
func (n *NoopEvents) AssistantChunk(_ string) {}

// AssistantDone is a no-op.
func (n *NoopEvents) AssistantDone(_ string, _, _ int) {}

// ContextFill is a no-op.
func (n *NoopEvents) ContextFill(_ float64, _, _ int, _, _ string) {}

// Finishing is a no-op.
func (n *NoopEvents) Finishing(_ int, _ string) {}

// ContextCompaction is a no-op.
func (n *NoopEvents) ContextCompaction(_, _ float64, _ string) {}

// ExecutorDiagnostic is a no-op.
func (n *NoopEvents) ExecutorDiagnostic(_ int, _ string, _ map[string]any) {}
