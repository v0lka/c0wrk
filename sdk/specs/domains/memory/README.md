# Memory

## Purpose

Manages the agent's working memory during execution: a managed representation of the LLM context window, three pluggable compaction strategies, selective tool-output pruning, and regular history mutation that reduces the O(n²) replay cost of long conversations. Each [Conductor](../orchestration/conductor.md) run and each [subagent](../orchestration/subagents.md) gets its own isolated `ContextManager`.

## Key Files

- `github.com/v0lka/sp4rk/memory` — `ContextWindow`, `ContextWindowConfig`, `NewContextWindow`, `BuildPrompt`/`AddStep`/`Compact`/`CheckFill`
- `github.com/v0lka/sp4rk/memory` (compaction) — `CompactionStrategy` implementations (`SlidingWindowStrategy`, `SummarizationStrategy`, `HierarchicalStrategy`), `NewCompactionStrategy` factory, `CompactionConfig`, `CompactionDeps`
- `github.com/v0lka/sp4rk/memory` — `CompactionThresholds`, `ToolOutputPruning`, `HistoryMutation`
- `github.com/v0lka/sp4rk/security` — untrusted-content wrapping for tool output
- `github.com/v0lka/sp4rk/llm` — `TokenCounter`, `ContextTokenTracker`, `ModelMetadata`, `Message`
- `github.com/v0lka/sp4rk/prompt` — `CacheBreakMarker` / `SplitCacheBreak`

## Core Types

```go
// Managed representation of the LLM context window.
type ContextWindow struct { /* unexported */ }

type ContextWindowConfig struct {
    SystemPrompt            string             // may contain a CacheBreakMarker
    ModelMeta               llm.ModelMetadata
    Tracker                 *llm.ContextTokenTracker // nil ⇒ discard tracker (accounting disabled)
    Thresholds              CompactionThresholds
    Strategy                agent.CompactionStrategy  // nil ⇒ compaction disabled
    SafetyMarginPercent     int                       // default 5
    InjectionDefenseEnabled bool
    Pruning                 ToolOutputPruning
}

type CompactionStrategy interface {
    Compact(ctx context.Context, steps []agent.Step, budgetTokens int) []llm.Message
}

type CompactionThresholds struct {
    PredictivePercent int // triggers "compact"
    WarningPercent    int // triggers "warning"
    EmergencyPercent  int // triggers "emergency"
}
```

## Flow

```
Executor.Run iteration
  │
  ├─ BuildPrompt()        → system message(s) → prior conversation → task → plan → step history
  ├─ LLM call             → response tokens counted → tracker updated
  └─ CheckFill()          → status drives the response:
        ├─ "ok"          continue normally
        ├─ "compact"     tool-output pruning runs (predictive)
        ├─ "warning"     strategy compaction runs
        ├─ "emergency"   aggressive strategy compaction
        └─ "reject"      context too full even after compaction
```

`CheckFill` maps the current fill percentage to a status: `>= 100` ⇒ `reject`; `>= EmergencyPercent` ⇒ `emergency`; `>= WarningPercent` ⇒ `warning`; `>= PredictivePercent` ⇒ `compact`; otherwise `ok`.

History mutation and pruning run on **every** `BuildPrompt` (not fill-triggered) to keep replay cost low, while information is preserved via the `agent.ToolResultCache` (evicted content is recoverable via a `tool_result_read` tool).

### Content wrapping for untrusted tools

When `InjectionDefenseEnabled` is `true`, tool outputs from untrusted sources (flagged via the step's `IsUntrusted`) are wrapped in `<untrusted-content>` tags before becoming an LLM message. Wrapping happens after pruning/mutation and before message construction — the last point before content reaches the LLM API.

## Invariants

- The context window never exceeds the model's limit (compaction prevents overflow).
- The system prompt is always preserved (never compacted away); `BuildPrompt` splits it on `CacheBreakMarker` into multiple system messages for provider-level prompt caching.
- Compaction is triggered proactively (before overflow, not after).
- History mutation and pruning run first in message construction; outputs already replaced by a cache reference or step-status eviction text are not overwritten by the pruning placeholder.
- Entire response groups are protected if any step in the group is protected (prevents malformed assistant/tool message pairs).
- Each step/subagent has its own `ContextManager` (no sharing between parallel steps).

## Configuration

`ContextWindowConfig` is the single source of memory tuning. Defaults:

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `SafetyMarginPercent` | `5` | Context window fraction reserved for counting inaccuracy. |
| `Thresholds.PredictivePercent` | `85` | Tool-output pruning trigger (%). |
| `Thresholds.WarningPercent` | `92` | Strategy compaction trigger (%). |
| `Thresholds.EmergencyPercent` | `98` | Aggressive compaction trigger (%). |
| `Pruning.KeepLastN` | `3` | Recent tool results always kept. |
| `Pruning.ThresholdPercent` | `50` | Fill % below which pruning is skipped entirely. |
| `ModelMeta.ContextWindow` (when 0) | `128000` | Fallback so compaction still works for unknown models. |
| `ModelMeta.OutputLimit` (when 0) | `4096` | Fallback output limit. |

`HistoryMutation` (set via `SetHistoryMutation`, before the first `BuildPrompt`):

| Field | Description |
| ----- | ----------- |
| `ToolResultEvictionStep` | Steps after which a tool result is replaced with a cache reference (`0` disables). |
| `EvictStepStatus` | Immediately evict `update_checklist` (bookkeeping) results. |
| `DedupRepeatedReads` | Replace duplicate file reads (same path + mtime → same cache hash) with a reference. |

## Extension Points

- **New compaction strategy**: implement `agent.CompactionStrategy` and register it in the `NewCompactionStrategy` factory (or pass it directly to `ContextWindowConfig.Strategy`).
- **Custom thresholds / pruning**: override compaction trigger percentages and `KeepLastN`/`ProtectedTools`.
- **Alternative token counter**: swap the `llm.TokenCounter` implementation; the `ContextTokenTracker` corrects estimates with API-reported actuals.
- **Per-step pruning overrides**: `PruningOverride` (carried through `ContextManagerFactory`) lets a step supply its own `KeepLastN`/`ProtectedTools`.
- **Optional `ContextManager` capabilities**: `TaskAware` (`SetTask`), `ConversationAware` (`SetPriorConversation`), `TrackerProvider` (`ContextTracker`) are type-asserted by the Conductor and wired when implemented.

## Related Specs

- [compaction.md](compaction.md) — strategy details, pruning, history mutation
- [blackboard.md](blackboard.md) — inter-step shared state
- [../orchestration/executor.md](../orchestration/executor.md) — the executor drives compaction
- [../orchestration/conductor.md](../orchestration/conductor.md) — wires the ContextManager per run
- [../prompt-building.md](../prompt-building.md) — `CacheBreakMarker` and provider-level prompt caching
