# Compaction Strategies

## Role

Compress conversation history when the context window approaches capacity, preserving the most relevant information for continued execution. Also covers tool-output pruning and regular history mutation, which run independently of fill-triggered compaction.

## Key Files

- `github.com/v0lka/sp4rk/memory` — `SlidingWindowStrategy`, `SummarizationStrategy`, `HierarchicalStrategy`, `NewCompactionStrategy`, `CompactionConfig`, `CompactionDeps`
- `github.com/v0lka/sp4rk/memory` — `ContextWindow` (orchestrates pruning + strategy compaction), `CompactionThresholds`
- `github.com/v0lka/sp4rk/memory` — `ToolOutputPruning`, `HistoryMutation`
- `github.com/v0lka/sp4rk/agent` — `CompactionStrategy` interface, `Step`, `ToolResultCache`
- `github.com/v0lka/sp4rk/llm` — `TokenCounter`, `Message`

## Behavior

Three strategies implement the `agent.CompactionStrategy` interface. Each transforms a slice of steps into a smaller slice of messages.

### Sliding window

Keeps the first `KeepFirst` and last `KeepLast` steps, dropping the middle. A system message marks the gap. No LLM calls are required — the simplest and fastest strategy. If the total step count does not exceed `KeepFirst + KeepLast`, all steps are kept verbatim. Best for code tasks (recent edits must stay visible).

```
[sys] [step1] … [stepK] … [stepN-2] [stepN-1] [stepN]
      ├── keep_first ──┤ discard ├── keep_last ────────┤
```

### Summarization

Groups the oldest steps into blocks and uses an LLM (`Summarize` callback) to summarize each block into a compact system message. The most recent `KeepLast` steps are preserved verbatim. Each block's text is truncated to `MaxSummarizeTokens` before being sent; if summarization fails, a fallback indicator message is emitted. Best for research tasks (preserves synthesized findings).

### Hierarchical

Divides steps into three zones with different compression levels:

| Zone | Default ratio | Treatment |
| ---- | ------------- | --------- |
| Distant (oldest) | `0.4` | Aggressive summarization — large blocks (~15 steps), observations truncated to 60% of the base value. |
| Middle | `0.3` | Moderate summarization — smaller blocks (~5 steps). |
| Recent | `0.3` | Kept verbatim. |

Ratios are normalized to sum to `1.0`. For very small step counts (≤ 5), all steps are returned verbatim. Best for long-running complex tasks needing balanced retention.

### NewCompactionStrategy (factory)

```go
func NewCompactionStrategy(name string, cfg CompactionConfig, deps CompactionDeps) agent.CompactionStrategy
```

Recognized names: `"sliding_window"`, `"summarization"`, `"hierarchical"`; any other name falls back to `SlidingWindowStrategy`. `CompactionConfig` holds fields for all three strategies (only the relevant ones are used); `CompactionDeps` carries the `TokenCounter`, the `Summarize` callback, and `MaxSummarizeTokens` (default `16000`) required by the LLM-based strategies. Zero fields are defaulted (`BlockSize`→`10`, `KeepLast`→`5`, `ObservationTruncate`→`500`, ratios→`0.4/0.3/0.3`).

### Tool-output pruning (separate mechanism)

`ToolOutputPruning` replaces individual tool-result bodies with a placeholder while keeping the surrounding assistant/tool message structure intact:

- The last `KeepLastN` tool-result steps are always protected.
- Any step whose tool name is in `ProtectedTools` is protected regardless of position.
- When context fill is below `ThresholdPercent`, **all** tool outputs are preserved (pruning skipped).
- Entire response groups are protected if any step in the group is protected.

This runs at the predictive threshold (default 85%), before strategy compaction (92%).

### Regular history mutation (separate mechanism)

`HistoryMutation` runs on **every** `BuildPrompt` (not fill-triggered) to reduce O(n²) replay cost. Information is preserved — evicted content is recoverable via `agent.ToolResultCache`:

- **Cache reference eviction**: a tool result older than `ToolResultEvictionStep` steps with a `CacheHash` is replaced with a cache-reference nudge (`tool_result_read(hash=…, …)`).
- **Step-status eviction**: `update_checklist` (bookkeeping) results are evicted immediately when `EvictStepStatus` is set.
- **Dedup repeated reads**: if the same file (same path + mtime → same cache hash) was read earlier, the later result is replaced with a cache reference.

Mutation runs **first** in message construction, before pruning and injection defense; pruning is skipped for outputs already replaced by a cache reference (overwriting the placeholder would destroy the cache hash and break recoverability).

### Trigger thresholds

```
Context fill %:
  0%────────85%────────92%──────98%────100%
              │           │        │
              ▼           ▼        ▼
         Tool output   Strategy  Emergency
          pruning     compaction  compaction

Regular history mutation: runs on EVERY BuildPrompt (not fill-triggered)
```

### Per-step pruning overrides

A step may override pruning config via `PruningOverride{KeepLastN, ProtectedTools}` (zero values mean "use the global default"), typically derived from a step profile's role.

## Error Handling

- **Summarization failure**: a fallback indicator message is emitted instead of aborting.
- **Empty history**: compaction is a no-op (returns an empty slice, no error).
- **Unrealistic budget**: if `budgetTokens` is smaller than system prompt + last message tokens, compaction preserves those and returns only them.
- **Unrecognized domain→strategy**: `sliding_window` is the universal fallback.

## Invariants

- Compaction never removes the system prompt or the last message (current LLM turn).
- Tool-output pruning runs before strategy compaction.
- Protected tools are never pruned regardless of `KeepLastN`.
- Mutation runs before pruning; cache-reference placeholders are not overwritten.
- After compaction, fill percentage is below the warning threshold.

## Related Specs

- [README.md](README.md) — context management overview
- [../orchestration/executor.md](../orchestration/executor.md) — per-iteration fill check
- [../orchestration/conductor.md](../orchestration/conductor.md) — compaction strategy selection per run
- [../tool-system/README.md](../tool-system/README.md) — `ToolResultCache` and cache-reference recovery
