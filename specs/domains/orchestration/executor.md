# Executor

## Role

The ReAct loop primitive (Thought → Action → Observation) is a **sp4rk engine** component. c0wrk does not reimplement it — `core/` launches `github.com/v0lka/sp4rk/agent.Executor.Run` in two roles: as the **Conductor** (top-level task owner) and as **subagents** (isolated loops launched by the `delegate` tool). The full loop semantics — circuit breakers, mutation/checklist gates, implicit-finish detection, two-stage truncation, `ToolResultCache`, `batch` interception — are documented canonically in [the sp4rk executor spec](../../../sdk/specs/domains/orchestration/executor.md).

## Key Files

- `core/conductor.go` — Conductor entry point: builds system prompt + tool set, injects the Delegation Registry, launches an `Executor.Run` as the Conductor
- `core/tools/delegate.go` — `delegate` tool: builds and launches `Executor.Run` instances as subagents via `github.com/v0lka/sp4rk/agent.RunSubAgent`
- `core/stepconfig.go` — `criticalAlwaysAllowedTools` set (c0wrk tools that survive an `AllowedTools` filter)

Engine files (`github.com/v0lka/sp4rk/agent/executor.go`, `executor_run.go`, `subagent.go`, `events.go`, `github.com/v0lka/sp4rk/tools/builtins/batch.go`) are documented in [the sp4rk executor spec](../../../sdk/specs/domains/orchestration/executor.md).

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

c0wrk registers consumer-specific meta-tools (`delegate`, `declare_plan`, `reflect`, `cancel_delegation`, `declare_step_complete`, `ask_user`) as non-cacheable via `Executor.AddNonCacheableTools`. These tools bypass `ToolResultCache` and Stage-1 truncation (their results are not large outputs worth fragmenting). The names are also passed to the sp4rk Conductor's `ConductorConfig.NonCacheableTools`. The default non-cacheable set (`tool_result_read`, `finish`, `batch`, etc.) is an engine concern — see the sp4rk executor spec.

### Critical Always-Allowed Tools

These c0wrk tools are always available regardless of a step's `AllowedTools` filter:

- `finish` — end execution
- `store_fact` — save findings to blackboard
- `search_facts` — retrieve stored facts
- `ask_user` — prompt user for information
- `update_checklist` — update checklist for current step or standalone
- `declare_step_complete` — signal inline plan step completion
- `read_step_output` — read a specific step's output
- `tool_result_read` — read cached tool result fragments by hash

The set is enforced in `core/stepconfig.go` `criticalAlwaysAllowedTools` and unioned into the filtered list whenever `AllowedTools` is non-empty.

## Engine Behavior (canonical in sp4rk)

The following are sp4rk engine primitives, documented in [the sp4rk executor spec](../../../sdk/specs/domains/orchestration/executor.md) — do not duplicate here:

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
- `criticalAlwaysAllowedTools` (c0wrk) are unioned into any non-empty `AllowedTools` filter.
- Each executor instance has its own `ContextManager` (isolated memory); the Conductor context never shares with subagent contexts.
- Untrusted tool output is wrapped in `<untrusted-content>` before reaching the LLM (engine behavior; c0wrk's session-root/auto-approval gating runs in the registry, see [../../architecture/security-model.md](../../architecture/security-model.md)).

## Related Specs

- [sp4rk executor](../../../sdk/specs/domains/orchestration/executor.md) — canonical ReAct loop, circuit breakers, gates, truncation, caching, batch interception
- [conductor.md](conductor.md) — Conductor (top-level Executor caller)
- [delegation.md](delegation.md) — subagent launch via the `delegate` tool
- [../memory/compaction.md](../memory/compaction.md) — compaction strategies
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement and injection defense
