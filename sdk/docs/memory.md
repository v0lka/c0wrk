# Memory Management

The `memory` package provides working-memory management for agent sessions: a managed representation of the LLM context window, three pluggable compaction strategies, selective tool-output pruning, and history mutation that reduces the O(n²) replay cost of long conversations.

```go
import "github.com/v0lka/sp4rk/memory"
```

## ContextWindow

`ContextWindow` is the managed representation of the LLM context window. It holds the system prompt, task content, plan content, prior conversation, and the running step history, and assembles them into the message slice that is sent to the model. It tracks token usage, reports fill status, and compacts history when the window fills up.

### NewContextWindow

```go
func NewContextWindow(cfg ContextWindowConfig) *ContextWindow
```

`ContextWindowConfig` fields (zero values fall back to sensible defaults):

| Field | Description |
| --- | --- |
| `SystemPrompt` | The base system prompt. May contain a `CacheBreakMarker` to split it into cacheable and dynamic system messages. |
| `ModelMeta` | Model metadata (`ContextWindow`, `OutputLimit`). |
| `Tracker` | Token tracker for fill accounting. If `nil`, a discard tracker is used and token accounting is silently disabled. |
| `Thresholds` | Fill percentages that trigger compaction. |
| `Strategy` | The compaction strategy (sliding window, summarization, or hierarchical). May be `nil` (compaction disabled). |
| `SafetyMarginPercent` | Percentage of the context window reserved as a safety margin. Defaults to `5` when `0` or negative. |
| `InjectionDefenseEnabled` | When `true`, untrusted tool outputs are wrapped in `<untrusted-content>` tags (see [Security](security.md)). |
| `Pruning` | `ToolOutputPruning` config. Zero value leaves pruning disabled with a default placeholder text. |

#### Fallback behavior

If `ModelMeta.ContextWindow` is `0` (unknown model), a fallback of **128000** is used. A zero context window would otherwise disable compaction entirely (`EffectiveMax` returns `0`, `CheckFill` returns `"ok"`), causing unbounded conversation growth until the API rejects the request. Likewise, a zero `OutputLimit` falls back to **4096**.

```go
cw := memory.NewContextWindow(memory.ContextWindowConfig{
    SystemPrompt:            systemPrompt,
    ModelMeta:               llm.ModelMetadata{ContextWindow: 200000, OutputLimit: 8192},
    Tracker:                 tracker,
    Thresholds:              memory.DefaultCompactionThresholds(),
    Strategy:                memory.NewCompactionStrategy("sliding_window", memory.CompactionConfig{}, memory.CompactionDeps{}),
    SafetyMarginPercent:     5,    // 5% safety margin
    InjectionDefenseEnabled: true, // wrap untrusted tool outputs
    Pruning:                 memory.DefaultToolOutputPruning(),
})
```

### Methods

| Method | Description |
| --- | --- |
| `BuildPrompt() []llm.Message` | Assembles the full prompt in priority order: system message(s) → prior conversation → task content → plan content → step history. |
| `AddStep(step)` | Appends a step to the history and updates the token tracker with an approximate delta. |
| `Compact(ctx) *CompactionResult` | Compresses the step history using the configured strategy. Returns before/after fill percentages, or `nil` if no compaction occurred. |
| `SetStrategy(s)` | Changes the compaction strategy. |
| `CheckFill() FillCheck` | Returns the current fill status: `"ok"`, `"compact"`, `"warning"`, `"emergency"`, or `"reject"`. |
| `CorrectTokenCount(apiInputTokens)` | Updates the tracker with the actual API-reported input token count so estimates converge over time. |
| `FillPercent() float64` | Current fill percentage of the effective context window. |
| `AvailableTokens() int` | Tokens remaining in the context window (never negative). |
| `OutputLimit() int` | The model's maximum output token limit. |
| `VulnerableOutputs() []VulnerableOutput` | Tool outputs that will be pruned on the next pruning cycle. Returns `nil` when pruning is inactive. |
| `SetTask(task)` | Sets the formatted task content (the user message). |
| `SetPlan(planText)` | Sets the formatted plan content (a system message). |
| `SetPriorConversation(msgs)` | Sets messages from previous exchanges that appear between the system message(s) and the current task, giving the agent dialogue context for follow-ups. |
| `SetHistoryMutation(m)` | Configures regular history mutation. Must be called before the first `BuildPrompt`. |
| `EffectiveMax() int` | Effective maximum token count: `ContextWindow - OutputLimit - safetyMargin`. |
| `Tracker() *llm.ContextTokenTracker` | Returns the underlying token tracker. |

`BuildPrompt` splits the system prompt on `CacheBreakMarker` into multiple system messages so that providers can apply prompt caching to the stable parts (see [Prompt Building](prompt-building.md)).

## CompactionThresholds

`CompactionThresholds` configures when context compaction triggers, expressed as context-fill percentages.

```go
type CompactionThresholds struct {
    PredictivePercent int // triggers predictive compaction ("compact")
    WarningPercent    int // triggers warning-level compaction ("warning")
    EmergencyPercent  int // triggers emergency compaction ("emergency")
}
```

`CheckFill` maps the current fill percentage to a status:

| Fill % | Status |
| --- | --- |
| `>= 100` | `reject` |
| `>= EmergencyPercent` | `emergency` |
| `>= WarningPercent` | `warning` |
| `>= PredictivePercent` | `compact` |
| otherwise | `ok` |

### DefaultCompactionThresholds

```go
func DefaultCompactionThresholds() CompactionThresholds
```

Returns sensible defaults:

```go
memory.CompactionThresholds{
    PredictivePercent: 85,
    WarningPercent:    92,
    EmergencyPercent:  98,
}
```

## Compaction Strategies

Three strategies implement the `agent.CompactionStrategy` interface. Each transforms a slice of steps into a smaller slice of messages.

### 1. Sliding Window

`SlidingWindowStrategy` keeps the first `KeepFirst` and last `KeepLast` steps, dropping the middle. A system message `[... N steps omitted ...]` marks the gap. This is the simplest and fastest strategy — no LLM calls are required.

```go
strategy := memory.NewSlidingWindowStrategy(keepFirst, keepLast)
```

If the total step count does not exceed `KeepFirst + KeepLast`, all steps are kept verbatim.

### 2. Summarization

`SummarizationStrategy` groups the oldest steps into blocks and uses an LLM (via a `Summarize` callback) to summarize each block into a compact system message. The most recent `KeepLast` steps are preserved verbatim.

```go
strategy := memory.NewSummarizationStrategy(
    blockSize,           // steps per summary block (default 10)
    keepLast,            // recent steps kept verbatim (default 5)
    observationTruncate, // max chars for observations in blocks (default 500)
    summarizeFn,         // func(ctx, text) (string, error)
    tokenCounter,        // optional; truncates blocks to maxSummarizeTokens
    maxSummarizeTokens,  // default 16000
)
```

Each block's text is truncated to `maxSummarizeTokens` before being sent to the summarizer. If summarization fails, a fallback indicator message is emitted instead. If no summarizer is provided, a placeholder is used.

### 3. Hierarchical

`HierarchicalStrategy` divides steps into three zones with different compression levels:

| Zone | Default ratio | Treatment |
| --- | --- | --- |
| **Distant** (oldest) | `0.4` | Aggressive summarization — large blocks of ~15 steps, observations truncated to 60% of the base value. |
| **Middle** | `0.3` | Moderate summarization — smaller blocks of ~5 steps. |
| **Recent** | `0.3` | Kept verbatim. |

```go
strategy := memory.NewHierarchicalStrategy(
    distantRatio,        // default 0.4
    middleRatio,         // default 0.3
    recentRatio,         // default 0.3
    observationTruncate, // default 500
    summarizeFn,
    tokenCounter,
    maxSummarizeTokens,  // default 16000
)
```

Ratios are normalized to sum to `1.0` if they do not already. For very small step counts (≤ 5), all steps are returned verbatim.

### NewCompactionStrategy

`NewCompactionStrategy` is a factory that creates a strategy by name. It is the recommended entry point when configuration is loaded from a config file.

```go
func NewCompactionStrategy(name string, cfg CompactionConfig, deps CompactionDeps) sdkagent.CompactionStrategy
```

Recognized names:

| Name | Strategy |
| --- | --- |
| `"sliding_window"` | `SlidingWindowStrategy` |
| `"summarization"` | `SummarizationStrategy` |
| `"hierarchical"` | `HierarchicalStrategy` |
| any other | Falls back to `SlidingWindowStrategy` |

```go
strategy := memory.NewCompactionStrategy(
    "hierarchical",
    memory.CompactionConfig{
        Hierarchical: struct {
            DistantRatio float64
            MiddleRatio  float64
            RecentRatio  float64
        }{DistantRatio: 0.5, MiddleRatio: 0.3, RecentRatio: 0.2},
    },
    memory.CompactionDeps{
        TokenCounter:       counter,
        Summarize:          summarizeFn,
        MaxSummarizeTokens: 12000,
    },
)
```

### CompactionConfig

`CompactionConfig` holds configuration for all three strategies in one struct. Only the fields relevant to the selected strategy are used.

```go
type CompactionConfig struct {
    SlidingWindow struct {
        KeepFirst int
        KeepLast  int
    }
    Summarization struct {
        BlockSize           int
        KeepLast            int
        ObservationTruncate int // max chars for observations in summary blocks (default 500)
    }
    Hierarchical struct {
        DistantRatio float64
        MiddleRatio  float64
        RecentRatio  float64
    }
}
```

`NewCompactionStrategy` applies defaults when fields are zero: `BlockSize` → `10`, `KeepLast` → `5`, `ObservationTruncate` → `500`, ratios → `0.4/0.3/0.3`.

### CompactionDeps

`CompactionDeps` holds the external dependencies required by the LLM-based strategies.

```go
type CompactionDeps struct {
    TokenCounter llm.TokenCounter
    // Summarize calls the LLM to summarize a block of text.
    Summarize func(ctx context.Context, text string) (string, error)
    // MaxSummarizeTokens is the maximum token count for text sent to summarization.
    // Defaults to 16000 if zero.
    MaxSummarizeTokens int
}
```

## ToolOutputPruning

`ToolOutputPruning` configures selective pruning of old tool outputs. Unlike compaction (which rewrites the whole history), pruning replaces individual tool result bodies with a placeholder while keeping the surrounding assistant/tool message structure intact.

```go
type ToolOutputPruning struct {
    KeepLastN        int           // number of recent tool results to always keep
    ProtectedTools   []string      // tool names whose outputs are never pruned
    PlaceholderText  string        // text substituted for pruned outputs
    ThresholdPercent float64       // context fill % below which pruning is skipped (default: 50)
    Logger           *slog.Logger  // optional diagnostics logger
}
```

How protection works:

- The last `KeepLastN` tool-result steps are always protected.
- Any step whose tool name is in `ProtectedTools` is protected regardless of position.
- When context fill is below `ThresholdPercent`, **all** tool outputs are preserved (pruning is skipped entirely).
- Entire response groups are protected if any step in the group is protected — this prevents malformed API messages (an assistant message with N tool calls but fewer tool results).

If `PlaceholderText` is empty, a sensible default is applied that instructs the model not to fabricate content it can no longer see.

### DefaultToolOutputPruning

```go
func DefaultToolOutputPruning() ToolOutputPruning
```

```go
memory.ToolOutputPruning{
    KeepLastN:        3,
    ThresholdPercent: 50,
}
```

## HistoryMutation

`HistoryMutation` configures regular (non-emergency) mutation of step history to reduce the O(n²) replay cost of long conversations. Unlike emergency compaction (triggered by fill %), history mutation runs on every `BuildPrompt` call. Crucially, **information is preserved** — evicted content is recoverable via the `ToolResultCache`, so the model can retrieve it through `tool_result_read`.

```go
type HistoryMutation struct {
    // ToolResultEvictionStep is the number of steps after which a tool result
    // is replaced with a cache reference. 0 disables eviction.
    ToolResultEvictionStep int
    // EvictStepStatus enables immediate eviction of update_checklist results
    // (pure bookkeeping, no information loss). Also matches the legacy
    // set_step_status name.
    EvictStepStatus bool
    // DedupRepeatedReads replaces duplicate file-read results (same path +
    // mtime, detected via cache hash) with a reference to the earlier result.
    DedupRepeatedReads bool
    // Logger is an optional diagnostics logger.
    Logger *slog.Logger
}
```

### Cache reference eviction

When a tool result is older than `ToolResultEvictionStep` steps and has a `CacheHash`, its observation is replaced with a cache reference:

```
[Result evicted to cache. Use tool_result_read(hash="abc123", start_line=1, num_lines=N) to retrieve the full content.]
```

The model can call `tool_result_read` with the hash to retrieve the full content on demand. This keeps the prompt compact while preserving recoverability.

### Mutation ordering

History mutation runs **first** in `buildToolMsg`, before pruning and injection defense. Pruning is skipped for outputs already replaced by a cache reference or step-status eviction text — overwriting those compact placeholders with the generic pruning placeholder would destroy the cache hash and break the information-preservation guarantee.

```go
cw := memory.NewContextWindow(/* ... */)
cw.SetHistoryMutation(memory.HistoryMutation{
    ToolResultEvictionStep: 8,
    EvictStepStatus:        true,
    DedupRepeatedReads:     true,
    Logger:                 logger,
})
```

## Prompt Injection Defense Integration

When `InjectionDefenseEnabled` is `true`, tool outputs from tools that return external data (marked via the step's `IsUntrusted` flag) are wrapped in `<untrusted-content>` tags before being added to the prompt. This defends against indirect prompt injection. See [Security](security.md) for details on the wrapping and stripping functions.

## Complete Example

```go
package main

import (
	"context"
	"log/slog"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/memory"
)

func main() {
	tracker := llm.NewContextTokenTracker(myCounter{})

	strategy := memory.NewCompactionStrategy(
		"summarization",
		memory.CompactionConfig{
			Summarization: struct {
				BlockSize           int
				KeepLast            int
				ObservationTruncate int
			}{BlockSize: 8, KeepLast: 4, ObservationTruncate: 400},
		},
		memory.CompactionDeps{
			TokenCounter:       myCounter{},
			Summarize:          summarize,
			MaxSummarizeTokens: 12000,
		},
	)

	cw := memory.NewContextWindow(memory.ContextWindowConfig{
		SystemPrompt:            "You are a helpful coding agent.",
		ModelMeta:               llm.ModelMetadata{ContextWindow: 200000, OutputLimit: 8192},
		Tracker:                 tracker,
		Thresholds:              memory.DefaultCompactionThresholds(),
		Strategy:                strategy,
		SafetyMarginPercent:     5,
		InjectionDefenseEnabled: true,
		Pruning:                 memory.DefaultToolOutputPruning(),
	})
	cw.SetHistoryMutation(memory.HistoryMutation{
		ToolResultEvictionStep: 8,
		EvictStepStatus:        true,
		DedupRepeatedReads:     true,
	})
	cw.SetTask("Refactor the auth module and add tests.")

	// ... run the agent loop, calling AddStep after each step ...

	// When the window fills, compact:
	if result := cw.Compact(context.Background()); result != nil {
		slog.Info("compacted",
			"before", result.BeforePercent, "after", result.AfterPercent)
	}

	// Build the final prompt for the next API call.
	messages := cw.BuildPrompt()
	_ = messages
}

func summarize(ctx context.Context, text string) (string, error) {
	// Call your LLM here to summarize text.
	return "[summary]", nil
}

type myCounter struct{}

func (myCounter) Count(string) int                { return 0 }
func (myCounter) CountMessages([]llm.Message) int { return 0 }

var _ agent.CompactionStrategy = (*memory.SlidingWindowStrategy)(nil)
```
