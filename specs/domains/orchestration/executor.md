# Executor

## Role

The ReAct loop primitive (Thought → Action → Observation) is a **sp4rk engine** component. c0wrk does not reimplement it — `core/` launches `github.com/v0lka/sp4rk/agent.Executor.Run` in two roles: as the **Conductor** (top-level task owner) and as **subagents** (isolated loops launched by the `delegate` tool). The full loop semantics — circuit breakers, mutation/checklist gates, implicit-finish detection, two-stage truncation, `ToolResultCache`, `batch` interception — are documented canonically in [the sp4rk executor spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md).

## Key Files

- `core/conductor.go` — Conductor entry point: builds system prompt + tool set, injects the Delegation Registry, launches an `Executor.Run` as the Conductor
- `core/tools/delegate.go` — `delegate` tool: builds and launches `Executor.Run` instances as subagents via `github.com/v0lka/sp4rk/agent.RunSubAgent`
- `core/conductor.go` — `mandatorySubagentTools` / `resolveTaskTools` (read-only built-ins + MCP tools always unioned into a subagent's toolset so exploration/MCP capability is never dropped)

Engine files (`github.com/v0lka/sp4rk/agent/executor.go`, `executor_run.go`, `subagent.go`, `events.go`, `github.com/v0lka/sp4rk/tools/builtins/batch.go`) are documented in [the sp4rk executor spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md).

## c0wrk Integration

### Callers

```
Conductor (core/conductor.go)
  └─ Executor.Run(ctx, conductorTools, cm)         // owns the task
       └─ tool_call: delegate({ tasks: [...] })
            └─ RunSubAgent(ctx, ...)                // per task
                 └─ Executor.Run(ctx, subagentTools, cm)  // isolated ReAct loop
```

The Executor is the same primitive in both cases. The difference is the tool set, system prompt, and context — assembled by the caller (Conductor entry point or `delegate` tool), not by the Executor itself. See [conductor.md](conductor.md) and [delegation.md](delegation.md) for how callers configure the Executor.

### Non-Cacheable Meta-Tools (`AddNonCacheableTools`)

c0wrk registers consumer-specific meta-tools (`delegate`, `cancel_delegation`, `declare_plan`, `execute_plan`, `reflect`, `declare_step_complete`, `ask_user`) as non-cacheable via `Executor.AddNonCacheableTools`. These tools bypass `ToolResultCache` and Stage-1 truncation (their results are not large outputs worth fragmenting). The names are also passed to the sp4rk Conductor's `ConductorConfig.NonCacheableTools`. The default non-cacheable set (`tool_result_read`, `finish`, `batch`, etc.) is an engine concern — see the sp4rk executor spec.

### Critical Always-Allowed Tools

`mandatorySubagentTools` in `core/conductor.go` (applied in `resolveTaskTools`) unions a mandatory base into every subagent's toolset whenever the Conductor grants an explicit tool list (the delegation `tools` array) or selects `"read-only"` mode, so exploration and MCP capability are never dropped. The base is every MCP-sourced tool plus `subagentReadOnlyToolNames`:

- **Read-only exploration**: `read_file`, `list_directory`, `glob`, `ripgrep`, `semantic_search`, `web_search`, `web_fetch`, `read_skill_resource`
- **Internal/meta**: `finish`, `store_fact`, `search_facts`, `read_step_output`, `list_step_outputs`, `read_final_result`, `tool_result_read`, `update_checklist`, `declare_step_complete`, `ask_user`

Mode-disabled tools (e.g. `glob`/`ripgrep`/`semantic_search` in No-Project/CHAT mode) are filtered out at selection time. The Conductor-only tools (`delegate`, `cancel_delegation`, `declare_plan`, `execute_plan`, `reflect`) are stripped from subagents via `stripConductorOnlyTools`; `delegate`/`cancel_delegation` are re-added only when `allow_redelegate` is true (depth-capped).

### Small-LLM Loop Hardening (c0wrk threshold override)

When the Small-LLM profile's `loop_hardening` variant is active (`small_llm.enabled && small_llm.loop_hardening.enabled` — see [../small-llm.md](../small-llm.md)), `core/builder.go` `applyLoopHardening` overrides the executor's circuit-breaker `CircuitBreakerConfig` with tighter-or-equal thresholds (`repeat_nudge_threshold`, `parse_error_abort_threshold`, `fruitless_nudge_threshold`, `fruitless_abort_threshold`, `same_tool_repeat_nudge_threshold` — four strictly tighter, `parse_error_abort_threshold` equal to the baseline) so a small model that repeats itself or stalls is nudged/aborted sooner. Only the thresholds present in the profile are overridden; all others keep their baseline. The override is applied once at builder construction and is inert when the profile is off.

## Engine Behavior (canonical in sp4rk)

The following are sp4rk engine primitives, documented in [the sp4rk executor spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md) — do not duplicate here:

- ReAct loop iteration, step limit, `HITLHandler.OnStepLimit` (AllowOnce/AllowAlways/Deny)
- Circuit breakers (repeat, truncation, parse-error, fruitless, same-tool)
- Mutation gate (coder steps, `mutationRequired`) and checklist gate (missing/unchecked sub-gates)
- Implicit-finish detection and `DetectToolCallSyntaxInContent` failure-mode handling
- `ToolResultCache` (short-hash keying, file-backed entries, coherence checks, TTL) and two-stage truncation
- `batch` meta-tool interception (`processBatchTool`)
- `Step.IsUntrusted` propagation and `<untrusted-content>` wrapping (see [../../architecture/security-model.md](../../architecture/security-model.md) for c0wrk's session-root/auto-approval layer)

## Error Handling

- Step failure (executor returns error): result stored with Error field set; the orchestrator records the failure on the blackboard.
- Context cancelled: propagates immediately; pending async delegations are cancelled via context-tree cancellation.
- Step limit reached without finish: `Finished: false`; the orchestrator records `Status: partial` (resumable).

## Invariants

- `finish` is ALWAYS available in every executor instance (never filtered out).
- Consumer meta-tools registered via `AddNonCacheableTools` are excluded from caching and Stage-1 truncation.
- `mandatorySubagentTools` (c0wrk, in `core/conductor.go`) are unioned into any explicit subagent tool list so read-only/MCP tools are never dropped.
- Each executor instance has its own `ContextManager` (isolated memory); the Conductor context never shares with subagent contexts.
- Untrusted tool output is wrapped in `<untrusted-content>` before reaching the LLM (engine behavior; c0wrk's session-root/auto-approval gating runs in the registry, see [../../architecture/security-model.md](../../architecture/security-model.md)).

## Related Specs

- [sp4rk executor](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md) — canonical ReAct loop, circuit breakers, gates, truncation, caching, batch interception
- [conductor.md](conductor.md) — Conductor (top-level Executor caller)
- [delegation.md](delegation.md) — subagent launch via the `delegate` tool
- [../memory/compaction.md](../memory/compaction.md) — compaction strategies
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement and injection defense
