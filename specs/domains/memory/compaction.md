# Compaction Strategies

## Role

c0wrk does not implement compaction strategies — they are **sp4rk engine** primitives (sliding window, summarization, hierarchical, tool-output pruning, regular history mutation). This spec documents only how c0wrk selects and configures them. The canonical strategy behavior is in [the sp4rk compaction spec](../../../sdk/specs/domains/memory/compaction.md).

## Key Files

- `core/builder.go` / `core/orchestrator.go` — `ContextManagerFactory` selects the strategy from `routing.Domain` (see [README.md](README.md) for the domain → strategy table)
- `core/orchestrator.go` — `plannerHistory()` uses sp4rk `CompactConversationHistory` to trim conversation history to a token budget before passing it to the router (separate from executor step-level compaction; keeps recent messages at the configured ratio)

Engine files (`github.com/v0lka/sp4rk/memory/compaction_sliding.go`, `compaction_summary.go`, `compaction_hierarchy.go`, `compaction_conversation.go`, `context.go`) are documented in [the sp4rk compaction spec](../../../sdk/specs/domains/memory/compaction.md).

## Strategy Overview (canonical in sp4rk)

| Strategy | Best for | Trade-off |
| -------- | -------- | --------- |
| sliding_window | code tasks (recent edits visible) | loses early research/context entirely |
| summarization | research tasks (preserves synthesized findings) | summary may lose details; requires LLM call |
| hierarchical | long-running complex tasks (balanced retention) | more complex logic, harder to predict retained content |

Tool-output pruning (predictive threshold) and regular history mutation (per-`BuildPrompt`, replaces old tool results with `ToolResultCache` references) are engine mechanisms — see the sp4rk compaction spec.

## Per-Step Pruning Overrides (c0wrk consumption)

c0wrk's `coreStepConfigurator` applies role-based pruning defaults via `PruningOverride`:

| Role | KeepLastN |
| ---- | --------- |
| `researcher` | 10 (needs more research context) |
| `coder`, `tester`, `executor` | 5 (recent edits suffice) |

## Trigger Thresholds (c0wrk config)

```
Context fill %:
  0%──────────85%────────92%──────98%────100%
              │           │        │
              ▼           ▼        ▼
         Tool output   Strategy  Emergency
          pruning     compaction  compaction
```

See [README.md](README.md) Configuration for the `config.yaml` keys.

## Invariants

- Compaction never removes the system prompt or the last message (current LLM turn)
- Tool output pruning runs BEFORE strategy compaction
- Protected tools are NEVER pruned regardless of `KeepLastN`
- Unrecognized domain → `sliding_window` fallback

## Related Specs

- [sp4rk compaction](../../../sdk/specs/domains/memory/compaction.md) — canonical strategy implementations, pruning, history mutation
- [README.md](README.md) — c0wrk memory overview and config
- [../orchestration/executor.md](../orchestration/executor.md) — context fill during execution
- [../orchestration/router.md](../orchestration/router.md) — domain → strategy mapping
