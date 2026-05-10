# Memory

## Purpose

Manages the agent's context window during step execution: tracks token usage, triggers compaction when the window fills, and provides strategy-based compression of conversation history.

## Key Files

- `sdk/memory/context.go` — ContextWindow struct (token tracking, compaction orchestration)
- `sdk/memory/compaction_sliding.go` — sliding window strategy
- `sdk/memory/compaction_summary.go` — summarization strategy
- `sdk/memory/compaction_hierarchy.go` — hierarchical strategy
- `sdk/memory/steps.go` — step message collection for compaction

## Core Types

```go
// ContextWindow manages token budget during step execution
type ContextWindow struct {
    systemPrompt    string
    modelMeta       llm.ModelMetadata  // context window size, output limit
    tokenTracker    *ContextTokenTracker
    thresholds      CompactionThresholds
    strategy        CompactionStrategy
    toolOutputPrune ToolOutputPruning
}

// CompactionStrategy interface
type CompactionStrategy interface {
    Compact(messages []llm.Message, tokenBudget int) ([]llm.Message, CompactionResult)
}

// CompactionResult
type CompactionResult struct {
    BeforeFill float64
    AfterFill  float64
    Strategy   string
}
```

## Flow

```
Executor calls LLM
  → response tokens counted → update ContextWindow
  → check fill percentage:
      ├─ < predictive threshold (85%): continue normally
      ├─ > predictive threshold: trigger tool output pruning
      ├─ > warning threshold (92%): trigger compaction strategy
      └─ > emergency threshold (98%): aggressive compaction
```

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

## Configuration

From `config.yaml`:

| Parameter                         | Default | Description                        |
| --------------------------------- | ------- | ---------------------------------- |
| `compaction.predictive_threshold` | 0.85    | Tool output pruning trigger        |
| `compaction.warning_threshold`    | 0.92    | Strategy compaction trigger        |
| `compaction.emergency_threshold`  | 0.98    | Aggressive compaction trigger      |
| `compaction.keep_first`           | 3       | Messages to always retain at start |
| `compaction.keep_last`            | 10      | Messages to always retain at end   |

## Related Specs

- [compaction.md](compaction.md) — strategy details
- [blackboard.md](blackboard.md) — inter-step state
- [../orchestration/executor.md](../orchestration/executor.md) — executor drives compaction
