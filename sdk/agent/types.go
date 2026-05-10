package agent

import (
	"context"
	"encoding/json"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

// Step — single iteration of the ReAct loop.
type Step struct {
	Thought          string       `json:"thought"`
	ReasoningContent string       `json:"reasoning_content,omitempty"` // chain-of-thought from reasoning models (DeepSeek)
	Action           llm.ToolCall `json:"action"`
	Observation      string       `json:"observation"`
	TokensUsed       int          `json:"tokens_used"`
	// UserNudge is an optional user message injected into the context (e.g., step limit nudges).
	// When set, this is added as a user message after the step's normal messages.
	UserNudge string `json:"user_nudge,omitempty"`
	// ResponseGroup links steps from the same LLM response when multiple tool calls were returned.
	// Steps with the same non-zero ResponseGroup value came from one response and should be
	// rendered as one assistant message with multiple tool_calls in BuildPrompt().
	// Zero means standalone step (single tool call).
	ResponseGroup int64 `json:"response_group,omitempty"`
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

// CircuitBreakerConfig — circuit breaker thresholds for executor protection.
type CircuitBreakerConfig struct {
	RepeatNudgeThreshold     int // consecutive identical tool calls before nudge
	RepeatAbortThreshold     int // consecutive identical tool calls before abort
	TruncationAbortThreshold int // consecutive truncated responses before abort
	ParseErrorAbortThreshold int // consecutive parse errors on same tool before abort

	// Fruitless result detector: catches consecutive minimal-result calls
	FruitlessNudgeThreshold int // consecutive minimal-result calls before nudge (default: 5)
	FruitlessAbortThreshold int // consecutive minimal-result calls before abort (default: 8)
	FruitlessMaxResultLen   int // result length at or below which a call is "fruitless" (default: 32)

	// Same-tool repetition detector: catches same tool with varied args but similar results
	SameToolRepeatNudgeThreshold int // same tool with varied args, similar results (default: 8)
	SameToolRepeatAbortThreshold int // abort threshold (default: 12)
	SameToolResultSizeDelta      int // max result length difference to consider "similar" (default: 64)
}

// LLMCaller is the interface Executor needs from the LLM layer.
type LLMCaller interface {
	Call(ctx context.Context, req llm.ChatRequest) (resp *llm.ChatResponse, err error)
}

// ToolExecutor is the interface Executor needs from the tools layer.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, input json.RawMessage) (result tools.ToolResult, err error)
	// GetToolSource returns the source of a tool (e.g., "core", "mcp:<server>").
	// Returns empty string if the tool is not found.
	GetToolSource(name string) string
}

// CompactionStrategy defines an algorithm for compressing step history.
type CompactionStrategy interface {
	Compact(ctx context.Context, steps []Step, budgetTokens int) []llm.Message
}

// CompactionResult holds before/after fill percentages from a compaction operation.
type CompactionResult struct {
	BeforePercent float64
	AfterPercent  float64
}

// ContextManager is the interface Executor needs for context window management.
// NOTE: This is the SDK-level interface WITHOUT SetTask (c0wrk-core adds that).
type ContextManager interface {
	BuildPrompt() []llm.Message
	AddStep(step Step)
	Compact(ctx context.Context) *CompactionResult
	SetStrategy(strategy CompactionStrategy)
	CheckFill() FillCheck
	CorrectTokenCount(apiInputTokens int)
	FillPercent() float64
	AvailableTokens() int
	OutputLimit() int
}

// StepLimitResponse represents the user's decision when the agent's step limit is reached.
type StepLimitResponse string

const (
	// StepLimitAllowOnce grants exactly one additional iteration.
	StepLimitAllowOnce StepLimitResponse = "allow_once"
	// StepLimitAllowAlways removes the step limit for the remainder of this execution.
	StepLimitAllowAlways StepLimitResponse = "allow_always"
	// StepLimitDeny terminates execution (current behavior).
	StepLimitDeny StepLimitResponse = "deny"
)

// StepLimitFunc is called when the agent exhausts its step limit.
// It blocks until the user responds with a decision.
type StepLimitFunc func(ctx context.Context, currentStep int, maxSteps int) (StepLimitResponse, error)
