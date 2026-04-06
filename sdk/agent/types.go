package agent

import (
	"context"
	"encoding/json"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

// Step — single iteration of the ReAct loop.
type Step struct {
	Thought     string       `json:"thought"`
	Action      llm.ToolCall `json:"action"`
	Observation string       `json:"observation"`
	TokensUsed  int          `json:"tokens_used"`
}

// ExecutorResult — result of Executor.Run.
type ExecutorResult struct {
	Output   string `json:"output"`
	Steps    []Step `json:"steps"`
	Finished bool   `json:"finished"` // true if finish action, false if budget exhausted
}

// SubAgentResult — result from a SubAgent.
type SubAgentResult struct {
	StepID string `json:"step_id"`
	Output string `json:"output"`
	Error  error  `json:"-"`
	Steps  []Step `json:"steps,omitempty"` // actual executor steps (tool calls + observations)
}

// FillCheck represents the result of a context window fill check.
type FillCheck struct {
	Percent float64
	Status  string // "ok", "compact", "warning", "emergency", "reject"
	Used    int
	Max     int
}

// ToolResultBudget — tool result truncation config.
type ToolResultBudget struct {
	HardCapTokens   int
	MaxFillFraction float64
}

// LLMCaller is the interface Executor needs from the LLM layer.
type LLMCaller interface {
	Call(ctx context.Context, req llm.ChatRequest) (resp *llm.ChatResponse, err error)
}

// ToolExecutor is the interface Executor needs from the tools layer.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, input json.RawMessage) (result tools.ToolResult, err error)
}

// CompactionStrategy defines an algorithm for compressing step history.
type CompactionStrategy interface {
	Compact(ctx context.Context, steps []Step, budgetTokens int) []llm.Message
}

// ContextManager is the interface Executor needs for context window management.
// NOTE: This is the SDK-level interface WITHOUT SetTask (c0wrk-core adds that).
type ContextManager interface {
	BuildPrompt() []llm.Message
	AddStep(step Step)
	NeedsCompaction() bool
	Compact(ctx context.Context)
	SetStrategy(strategy CompactionStrategy)
	CheckFill() FillCheck
	CorrectTokenCount(apiInputTokens int)
	FillPercent() float64
	AvailableTokens() int
	OutputLimit() int
}
