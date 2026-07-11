# Memory

## Purpose

c0wrk wires sp4rk's context-management engine into the orchestration cycle: it selects a compaction strategy per routing domain, configures fill thresholds, and persists blackboard state across sessions. The `ContextWindow`, compaction strategies, tool-output pruning, history mutation, and untrusted-content wrapping are **sp4rk engine** primitives — see [the sp4rk memory spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/README.md) and [the sp4rk compaction spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md).

## Key Files

- `core/builder.go` / `core/orchestrator.go` — `ContextManagerFactory` closure: constructs a `github.com/v0lka/sp4rk/agent.ContextManager` (backed by a sp4rk `ContextWindow`) per executor, selecting the compaction strategy from `routing.Domain`
- `core/orchestrator.go` — `plannerHistory()` trims conversation history to a token budget before passing it to the router (uses sp4rk `CompactConversationHistory`)
- `backend/session/persistent_blackboard.go` — `PersistentBlackboard` (SQLite-backed Blackboard for c0wrk persistence/restore) — see [blackboard.md](blackboard.md)

Engine files (`github.com/v0lka/sp4rk/memory/context.go`, `compaction*.go`, `steps.go`) and the `ContextWindow` struct are documented in [the sp4rk memory spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/README.md).

## Domain → Strategy Mapping (c0wrk consumption)

c0wrk's `ContextManagerFactory` selects the compaction strategy from the router's `routing.Domain`:

| Domain (from Router)        | Strategy       | Rationale                            |
| --------------------------- | -------------- | ------------------------------------ |
| `code`                      | sliding_window | Keeps recent file edits visible      |
| `research`                  | summarization  | Condenses findings into key points   |
| `general` (complexity < 4)  | sliding_window | Default safe choice                  |
| `general` (complexity >= 4) | hierarchical   | Balanced retention for complex tasks |

Unrecognized domains fall back to `sliding_window`.

## Flow

The fill-check ladder is engine behavior (see the sp4rk memory spec); c0wrk only configures the thresholds:

```
Executor calls LLM → response tokens counted → ContextWindow checks fill %:
  ├─ < predictive threshold (85%): continue normally
  ├─ >= predictive threshold (85%): trigger tool output pruning
  ├─ >= warning threshold (92%): trigger compaction strategy
  └─ >= emergency threshold (98%): aggressive compaction
```

Untrusted tool output is wrapped in `&lt;untrusted-content>` tags by the sp4rk context builder — see [../../architecture/security-model.md](../../architecture/security-model.md) for c0wrk's session-root/auto-approval layer on top of that wrapping.

## Invariants

- The context window NEVER exceeds the model's limit (compaction prevents overflow)
- The system prompt is ALWAYS preserved (never compacted away)
- Compaction is triggered proactively (before overflow, not after)
- Each executor instance has its own `ContextManager` (no sharing between Conductor and subagents)

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
| `executor.historyMutation.evictStepStatus`          | false   | Evict update_checklist results immediately |
| `executor.historyMutation.dedupRepeatedReads`       | false   | Replace duplicate file reads with cache reference |

## Extension Points

- **Custom compaction strategy**: implement `github.com/v0lka/sp4rk/agent.CompactionStrategy` and register it in the sp4rk factory; c0wrk selects it via the domain→strategy mapping.
- **Custom thresholds**: override compaction trigger percentages in `config.yaml`.
- **Alternative token counter**: swap the `github.com/v0lka/sp4rk/llm.TokenCounter` implementation for different model families.

## Related Specs

- [sp4rk memory overview](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/README.md) — canonical `ContextWindow`, fill statuses, content wrapping
- [sp4rk compaction](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md) — canonical strategy implementations, tool-output pruning, history mutation
- [compaction.md](compaction.md) — c0wrk strategy selection and config
- [blackboard.md](blackboard.md) — c0wrk blackboard persistence/restore
- [../orchestration/executor.md](../orchestration/executor.md) — executor drives compaction
