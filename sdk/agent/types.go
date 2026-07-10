package agent

import (
	"context"
	"encoding/json"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// Step — single iteration of the ReAct loop.
type Step struct {
	Thought          string `json:"thought"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // chain-of-thought from reasoning models (DeepSeek)
	// ReasoningItems carries reasoning output items from the OpenAI Responses API.
	// Each item has an ID required for round-tripping to the API to maintain the
	// reasoning chain across ReAct iterations. Populated only by the Responses API
	// provider; mirrors llm.Message.ReasoningItems so it survives the Step boundary.
	ReasoningItems []llm.ReasoningItem `json:"reasoning_items,omitempty"`
	// Action is the tool call requested by the model in this step.
	Action llm.ToolCall `json:"action"`
	// Observation is the tool result (possibly truncated) fed back to the model.
	Observation string `json:"observation"`
	// IsError is true when the tool execution returned an error result
	// (ToolResult.IsError). Used by the mutation gate to avoid counting failed
	// mutating tools (e.g. a write_file that errored) as successful mutations.
	IsError    bool `json:"is_error,omitempty"`
	TokensUsed int  `json:"tokens_used"`
	// UserNudge is an optional user message injected into the context (e.g., step limit nudges).
	// When set, this is added as a user message after the step's normal messages.
	UserNudge string `json:"user_nudge,omitempty"`
	// ResponseGroup links steps from the same LLM response when multiple tool calls were returned.
	// Steps with the same non-zero ResponseGroup value came from one response and should be
	// rendered as one assistant message with multiple tool_calls in BuildPrompt().
	// Zero means standalone step (single tool call).
	ResponseGroup int64 `json:"response_group,omitempty"`
	// IsUntrusted indicates the Observation came from an untrusted external source
	// (web, MCP, filesystem) and should be wrapped in <untrusted-content> tags
	// before entering the LLM context as a prompt injection defense.
	IsUntrusted bool `json:"is_untrusted,omitempty"`
	// CacheHash holds the short (abbreviated) hash of the full (pre-truncation)
	// tool result stored in ToolResultCache — a git-style prefix of the SHA256,
	// unique within the session. Empty for non-cacheable tools or when the cache
	// is disabled. Used by ContextWindow to replace old tool results with a
	// cache reference (regular history mutation), reducing O(n²) replay cost.
	CacheHash string `json:"cache_hash,omitempty"`
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

// ToolTruncationConfig — per-tool truncation defaults for Stage 1 (line/byte-based).
// Applied before the token-based ToolResultBudget (Stage 2).
type ToolTruncationConfig struct {
	MaxLines int // 0 = no line-based truncation
	MaxBytes int // 0 = no byte-based truncation
}

// CircuitBreakerConfig — circuit breaker thresholds for executor protection.
type CircuitBreakerConfig struct {
	RepeatNudgeThreshold     int // consecutive identical tool calls before nudge
	RepeatAbortThreshold     int // consecutive identical tool calls before abort
	TruncationAbortThreshold int // consecutive truncated responses before abort
	ParseErrorAbortThreshold int // consecutive parse errors on same tool before abort

	// Fruitless result detector: catches consecutive minimal-result calls
	FruitlessNudgeThreshold int // consecutive minimal-result calls before nudge (default: 4)
	FruitlessAbortThreshold int // consecutive minimal-result calls before abort (default: 6)
	FruitlessMaxResultLen   int // result length at or below which a call is "fruitless" (default: 32)

	// Same-tool repetition detector: catches same tool with varied args but similar results
	SameToolRepeatNudgeThreshold int // same tool with varied args, similar results (default: 6)
	SameToolRepeatAbortThreshold int // abort threshold (default: 10)
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
	// IsToolUntrusted reports whether a tool's output is from an untrusted external source.
	// Returns true for MCP-sourced tools and tools with IsUntrusted() == true.
	IsToolUntrusted(name string) bool
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

// VulnerableOutput describes a tool output that will be pruned on the next pruning cycle.
type VulnerableOutput struct {
	ToolName  string // name of the tool that produced the output
	InputHint string // human-readable summary of tool input (file path, pattern, etc.)
}

// ContextManager is the interface Executor needs for context window management.
// NOTE: This is the sp4rk-level interface WITHOUT SetTask (the host application adds that).
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
	VulnerableOutputs() []VulnerableOutput
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

// TrajectoryStore is a mutable holder for the executor's current trajectory.
// The executor syncs its step history to the store at each loop iteration so
// tools (e.g. reflect) can access the trajectory via context.
//
// Concurrency: the executor calls Sync/Steps from the ReAct loop goroutine.
// When sub-agents run in parallel, each has its own executor and thus its own
// store, so cross-goroutine access is uncommon — but tools reading the store
// via TrajectoryStoreFrom may run on a different goroutine. Implementations
// that share state across goroutines MUST be safe for concurrent use.
type TrajectoryStore interface {
	Sync(steps []Step)
	Steps() []Step
}

type trajectoryStoreKey struct{}

// WithTrajectoryStore injects a TrajectoryStore into the context.
func WithTrajectoryStore(ctx context.Context, store TrajectoryStore) context.Context {
	return context.WithValue(ctx, trajectoryStoreKey{}, store)
}

// TrajectoryStoreFrom extracts the TrajectoryStore from the context, or returns nil.
func TrajectoryStoreFrom(ctx context.Context) TrajectoryStore {
	if v, ok := ctx.Value(trajectoryStoreKey{}).(TrajectoryStore); ok {
		return v
	}
	return nil
}
