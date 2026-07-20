# Compaction Strategies

## Role

c0wrk does not implement compaction strategies — they are **sp4rk engine** primitives (sliding window, summarization, hierarchical, tool-output pruning, regular history mutation). This spec documents only how c0wrk selects and configures them. The canonical strategy behavior is in [the sp4rk compaction spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md).

## Key Files

- `core/builder.go` — `buildContextFactory` constructs the `ContextManagerFactory`; the strategy is selected by the caller (`compactionStrategyForDomain`, see below) and passed in as an argument
- `core/conductor.go` — `compactionStrategyForDomain(domain, complexity)` selects the strategy from `routing.Domain` + complexity (see [README.md](README.md) for the domain → strategy table); the context factory also applies optional per-step `PruningOverride` values from `StepConfig` (passed as variadic args)

Engine files (`github.com/v0lka/sp4rk/memory/compaction_sliding.go`, `compaction_summary.go`, `compaction_hierarchy.go`, `compaction_conversation.go`, `context.go`) are documented in [the sp4rk compaction spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md).

## Strategy Overview (canonical in sp4rk)

| Strategy | Best for | Trade-off |
| -------- | -------- | --------- |
| sliding_window | code tasks (recent edits visible) | loses early research/context entirely |
| summarization | research tasks (preserves synthesized findings) | summary may lose details; requires LLM call |
| hierarchical | long-running complex tasks (balanced retention) | more complex logic, harder to predict retained content |

Tool-output pruning (predictive threshold) and regular history mutation (per-`BuildPrompt`, replaces old tool results with `ToolResultCache` references) are engine mechanisms — see the sp4rk compaction spec.

## Tool-Output Pruning Config (c0wrk consumption)

c0wrk configures tool-output pruning centrally in `core/builder.go` (`buildContextFactory`) from `cfg.Executor.ToolOutputPruning`:

- `keepLastN` — how many recent tool results to keep inline before evicting older ones to `ToolResultCache` references
- `protectedTools` — tool names whose outputs are never pruned regardless of `keepLastN`
- `thresholdPercent` — fill percentage that triggers a pruning pass

Per-step pruning overrides are supported via the `PruningOverride` variadic argument on `ContextManagerFactory`; when a step's `StepConfig` carries a positive `KeepLastN`, it replaces the global value for that step's executor.

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

- [sp4rk compaction](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md) — canonical strategy implementations, pruning, history mutation
- [README.md](README.md) — c0wrk memory overview and config
- [../orchestration/executor.md](../orchestration/executor.md) — context fill during execution
- [../orchestration/router.md](../orchestration/router.md) — domain → strategy mapping
