# Compaction Strategies

## Role

Compress conversation history when the context window approaches capacity, preserving the most relevant information for continued execution.

## Key Files

- `sdk/memory/compaction_sliding.go` — SlidingWindowStrategy
- `sdk/memory/compaction_summary.go` — SummarizationStrategy
- `sdk/memory/compaction_hierarchy.go` — HierarchicalStrategy
- `sdk/memory/context.go` — ToolOutputPruning (separate from strategy compaction)
- `sdk/memory/compaction_conversation.go` — `CompactConversationHistory` (planner prompt compaction: trims conversation history to a token budget, keeping recent messages at the configured ratio). Used by `core/orchestrator.go` `plannerHistory()` before passing history to the planner — separate from the executor's step-level compaction.

## Behavior

### Sliding Window

Keeps the first N and last M messages, discards the middle.

```
[sys] [msg1] [msg2] ... [msgK] ... [msgN-2] [msgN-1] [msgN]
      ├─ keep_first ─┤   discard   ├── keep_last ──────────┤
```

- **Best for**: code tasks (recent edits must stay visible)
- **Trade-off**: loses early research/context entirely
- Config: `keep_first`, `keep_last`

### Summarization

Groups older messages into chunks and replaces them with LLM-generated summaries.

```
[sys] [summary_of_1-5] [summary_of_6-10] [msg11] [msg12] ... [msgN]
      ├── summarized ──────────────────┤  ├── recent (kept) ──────┤
```

- **Best for**: research tasks (preserves synthesized findings)
- **Trade-off**: summary may lose specific details; requires LLM call
- Config: chunk size, summary model

### Hierarchical

Weights messages by position: distant messages get aggressive compression, middle messages get moderate compression, recent messages stay intact.

```
Weight: [low] [low] [med] [med] [high] [high] [full] [full] [full]
         ├── distant ──┤  ├─ middle ─┤  ├──── recent ────────────┤
```

- **Best for**: long-running complex tasks that need balanced retention
- **Trade-off**: more complex logic, harder to predict what's kept
- Config: distance/middle/recent ratios

### Tool Output Pruning (separate mechanism)

Before triggering a full compaction strategy, the system prunes verbose tool outputs:

- Keep only the last N tool results
- Protected tools' results are never pruned (e.g., store_fact, search_facts)
- Pruned results are replaced with a brief "output pruned" marker

This runs at the predictive threshold (85%), before strategy compaction (92%).

## Trigger Thresholds

```
Context fill %:
  0%──────────85%────────92%──────98%────100%
              │           │        │
              ▼           ▼        ▼
         Tool output   Strategy  Emergency
          pruning     compaction  compaction
```

## Per-Step Pruning Overrides

Steps can override pruning config via `PruningOverride`:

- `KeepLastN` — number of tool results to keep (0 = global default)
- `ProtectedTools` — tools whose results are never pruned (nil = global default)

Role-based defaults:

- `researcher`: KeepLastN=10 (needs more research context)
- `coder`, `tester`, `executor`: KeepLastN=5 (recent edits suffice)

## Invariants

- Compaction never removes the system prompt
- Compaction never removes the last message (current LLM turn)
- Tool output pruning runs BEFORE strategy compaction
- Protected tools are NEVER pruned regardless of KeepLastN
- After compaction, fill percentage must be below warning threshold

## Error Handling

- **Compaction failure**: if a strategy cannot produce a result (e.g., LLM summarization API error), the system falls back to sliding window compaction with conservative defaults
- **Empty history**: compaction on an empty conversation history is a no-op (returns empty slice, no error)
- **Budget overflow**: if `budgetTokens` is unrealistically small (less than system prompt + last message tokens), compaction preserves the system prompt and last message, returning only those
- **Tool output pruning**: if a tool result exceeds the per-result size limit, it is truncated with a marker; protected tools' results are exempt from truncation
- **Strategy selection**: if the domain-to-strategy mapping yields an unrecognized domain, `sliding_window` is used as the universal fallback

## Related Specs

- [README.md](README.md) — memory overview
- [../orchestration/executor.md](../orchestration/executor.md) — context fill during execution
- [../orchestration/router.md](../orchestration/router.md) — domain → strategy mapping
