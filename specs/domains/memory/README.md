# Memory

## Purpose

Manages the agent's context window during step execution: tracks token usage, triggers compaction when the window fills, and provides strategy-based compression of conversation history.

## Key Files

- `sdk/memory/context.go` — ContextWindow struct (token tracking, compaction orchestration, untrusted content wrapping)
- `sdk/memory/compaction.go` — CompactionStrategy factory (NewCompactionStrategy)
- `sdk/memory/compaction_sliding.go` — sliding window strategy
- `sdk/memory/compaction_summary.go` — summarization strategy
- `sdk/memory/compaction_hierarchy.go` — hierarchical strategy
- `sdk/memory/steps.go` — step message collection for compaction

## Core Types

```go
// ContextWindow manages token budget during step execution
type ContextWindow struct {
    systemPrompt    string
    taskContent     string
    planContent     string
    steps           []agent.Step
    strategy        CompactionStrategy
    tracker         *llm.ContextTokenTracker
    modelMeta       llm.ModelMetadata  // context window size, output limit
    thresholds      CompactionThresholds
    pruning         ToolOutputPruning
    safetyMargin    int

    // injectionDefenseEnabled gates <untrusted-content> wrapping for untrusted tool output
    injectionDefenseEnabled bool
    // compactedMessages stores the frozen prefix from the last Compact() call
    compactedMessages []llm.Message
    // compactedThroughIndex marks where the frozen prefix ends in the steps slice
    compactedThroughIndex int
}

// CompactionStrategy interface
type CompactionStrategy interface {
    Compact(ctx context.Context, steps []agent.Step, budgetTokens int) []Message
}

// CompactionResult
type CompactionResult struct {
    BeforePercent float64
    AfterPercent  float64
}
```

## Flow

```
Executor calls LLM
  → response tokens counted → update ContextWindow
  → check fill percentage:
      ├─ < predictive threshold (85%): continue normally
      ├─ >= predictive threshold (85%): trigger tool output pruning
      ├─ >= warning threshold (92%): trigger compaction strategy
      └─ >= emergency threshold (98%): aggressive compaction
```

### Content Wrapping for Untrusted Tools

Before observations are added to the LLM context, `buildStepMessages()` checks `Step.IsUntrusted`. If `true`, the observation content is wrapped in `<untrusted-content>` XML tags (via `sdk/security.WrapUntrustedContent()`). Wrapping happens after pruning and empty-check, but before the observation is turned into an LLM message. This is the last point before content reaches the LLM API.

## Domain → Strategy Mapping

| Domain (from Router)        | Strategy       | Rationale                            |
| --------------------------- | -------------- | ------------------------------------ |
| `code`                      | sliding_window | Keeps recent file edits visible      |
| `research`                  | summarization  | Condenses findings into key points   |
| `general` (complexity < 4)  | sliding_window | Default safe choice                  |
| `general` (complexity >= 4) | hierarchical   | Balanced retention for complex tasks |

## Invariants

- Context window NEVER exceeds model's limit (compaction prevents overflow)
- System prompt is ALWAYS preserved (never compacted away)
- Compaction is triggered proactively (before overflow, not after)
- Each step has its own ContextWindow (no sharing between parallel steps)
- Tool output from untrusted sources is wrapped in `<untrusted-content>` tags before becoming an LLM message; wrapping happens after pruning and before message construction

## Configuration

From `config.yaml` (values are percentages, not fractions):

| Parameter                                           | Default | Description                        |
| --------------------------------------------------- | ------- | ---------------------------------- |
| `executor.compaction.thresholds.predictive_percent` | 85      | Tool output pruning trigger (%)    |
| `executor.compaction.thresholds.warning_percent`    | 92      | Strategy compaction trigger (%)    |
| `executor.compaction.thresholds.emergency_percent`  | 98      | Aggressive compaction trigger (%)  |
| `executor.compaction.sliding_window.keep_first`     | 3       | Messages to always retain at start |
| `executor.compaction.sliding_window.keep_last`      | 10      | Messages to always retain at end   |
| `executor.historyMutation.toolResultEvictionStep`   | 10      | Evict tool results to cache refs after N steps (0 = disabled) |
| `executor.historyMutation.evictStepStatus`          | false   | Evict set_step_status results immediately |
| `executor.historyMutation.dedupRepeatedReads`       | false   | Replace duplicate file reads with cache reference |

## Extension Points

- **New compaction strategy**: implement `CompactionStrategy` interface and register in `NewCompactionStrategy` factory
- **Custom thresholds**: override compaction trigger percentages in config.yaml per execution mode
- **Alternative token counter**: swap `TokenCounter` implementation for different model families
- **Compaction event hooks**: add custom logic on `ContextWindow.compact()` for logging or metrics

## Related Specs

- [compaction.md](compaction.md) — strategy details
- [blackboard.md](blackboard.md) — inter-step state
- [../orchestration/executor.md](../orchestration/executor.md) — executor drives compaction
