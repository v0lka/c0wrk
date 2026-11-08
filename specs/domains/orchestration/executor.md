# Executor

## Role

The ReAct loop primitive (Thought → Action → Observation) is a **sp4rk engine** component. c0wrk does not reimplement it — `core/` launches `github.com/v0lka/sp4rk/agent.Executor.Run` in two roles: as the **Conductor** (top-level task owner) and as **subagents** (isolated loops launched by the `delegate` tool). The full loop semantics — circuit breakers, mutation/checklist gates, implicit-finish detection, two-stage truncation, `ToolResultCache`, `batch` interception — are documented canonically in [the sp4rk executor spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md).

## Key Files

- `core/conductor.go` — Conductor entry point: builds system prompt + tool set, injects the Delegation Registry, launches an `Executor.Run` as the Conductor
- `core/tools/delegate.go` — `delegate` tool: validates the batch and hands it to the `DelegationLauncher` in context; the subagent executors are built and launched by `conductorLauncher` (`core/conductor.go`) through `github.com/v0lka/sp4rk/agent.RunSubAgent`
- `core/conductor.go` — `resolveTaskTools` (group-based subagent toolset resolution: `all` | `read-only` | group-token list; the `system` group is always included — ADR-024)

Engine files (`github.com/v0lka/sp4rk/agent/executor.go`, `executor_run.go`, `subagent.go`, `events.go`, `github.com/v0lka/sp4rk/tools/builtins/batch.go`) are documented in [the sp4rk executor spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md).

## c0wrk Integration

### Callers

```
RunConductor (core/conductor.go)
  └─ orchestration.Conductor.Run → Executor.Run(ctx, conductorTools, cm)  // owns the task
       └─ tool_call: delegate({ tasks: [...] })        // core/tools/delegate.go
            └─ conductorLauncher.Launch → runWave
                 ├─ launchAsync | runRegularBlocking | runRedelegBlocking
                 └─ agent.RunSubAgent(ctx, ...)          // per task
                      └─ Executor.Run(ctx, subagentTools, cm)  // isolated ReAct loop
```

The Executor is the same primitive in both cases. The difference is the tool set, system prompt, and context — assembled by the caller (`RunConductor` for the Conductor, `conductorLauncher.buildSubAgentTask` for a subagent), not by the Executor itself. See [conductor.md](conductor.md) and [delegation.md](delegation.md) for how callers configure the Executor.

### Non-Cacheable Meta-Tools (`AddNonCacheableTools`)

c0wrk registers consumer-specific meta-tools (`delegate`, `cancel_delegation`, `declare_plan`, `execute_plan`, `reflect`, `declare_step_complete`, `ask_user`) as non-cacheable via `Executor.AddNonCacheableTools`. Non-cacheable means EAGER caching is skipped (these results are small), not that they escape truncation: a non-cacheable result truncated at Stage 1 (per-tool line/byte limits) or Stage 2 (the token budget) is cached on demand and its hash rides the truncation notice, so the dropped portion stays recoverable via `tool_result_read`. The names are also passed to the sp4rk Conductor's `ConductorConfig.NonCacheableTools`. The default non-cacheable set (`tool_result_read`, `finish`, `update_checklist`, `store_fact`, `search_facts`, `read_step_output`, `list_step_outputs`, `read_final_result`, `read_attachment`, `batch`) is an engine concern — see the sp4rk executor spec.

### Group-Based Subagent Toolsets

`resolveTaskTools` in `core/conductor.go` resolves a delegation's toolset from **capability groups** (ADR-024) — never from tool-name lists:

- `"all"` (default / absent) — full toolset minus Conductor-only tools
- `"read-only"` — `system ∪ local_read ∪ remote_read` groups, **no MCP**
- array of group tokens (`local-read`, `execute`, …; kebab-case, underscores normalized) — the always-included `system` group plus exactly the granted groups

Unknown tokens fail closed (profile parser `ParseError`, delegate-task validation error, or resolver error — the delegation never launches with a silently widened/narrowed toolset).

Mode-disabled tools are filtered out at selection time (`stripDisabled`): in No-Project/CHAT mode that is `semantic_search` alone — `ripgrep` and `glob` stay available. MCP tools gated off for the task are dropped from the same list (`stripGatedMCPTools`, applied over `stripDisabled`'s output). Every branch then ends in `stripSubagentTools`, which removes the Conductor-only tools (`delegate`, `cancel_delegation`, `declare_plan`, `execute_plan`, `reflect`) plus the goal-loop actor tools (`propose_goal`, `declare_goal_status`, `declare_verification`); `runRedelegBlocking` re-adds `delegate`/`cancel_delegation` with their real registry schemas only when `allow_redelegate` is true (depth-capped).

### Tool-Call Ceiling, Result Cache and Truncation Wiring

The engine owns the mechanisms; c0wrk owns the wiring and the config surface:

- **Per-tool-call ceiling** — `timeouts.toolCallTimeout` (seconds; default 300, `0` disables) reaches the main executor as `orchestration.ConductorConfig.ToolCallTimeout` and every subagent executor through `conductorLauncher.configureExecutor` → `Executor.SetToolCallTimeout`. An expiry ends the run with an error naming the tool that `errors.Is` matches against `agent.ErrToolTimeout` (the sp4rk Conductor maps it to `ExecutionStatusFailed`). The same value is mirrored onto the session tool registry so a user-confirmation wait is bounded just under the ceiling instead of tripping it.
- **Exempt set** — `timeouts.toolCallTimeoutExemptTools` replaces the ceiling's exempt tool-NAME set wholesale (`Executor.SetToolCallTimeoutExempt`); leaving it unset keeps sp4rk's `agent.DefaultToolCallTimeoutExemptTools()` (`ask_user`, `declare_plan`, `delegate`, `execute_plan`, `propose_goal`). The E2S loop carries its own deliberately non-configurable exempt set (`ask_user`, `delegate`).
- **Result cache and Stage-1 truncation** — one session-lifetime `agent.ToolResultCache` (TTL from `executor.tool_result_budget.cacheTTLSeconds`) is threaded as `ConductorConfig.ToolCache` and installed on subagents via `SetToolCache`; per-tool Stage-1 limits come from `toolLimits.perToolTruncation.<tool>` (`{maxLines, maxBytes}`) via `ConductorConfig.PerToolTruncation` / `SetPerToolTruncation`; the token budget rides `ConductorConfig.ToolResultBudget`.

See [../tool-system/builtins.md](../tool-system/builtins.md) for the tool-side limits and [../../contracts/core-sp4rk.md](../../contracts/core-sp4rk.md) for the engine surface.

### Model Profiles Loop Hardening (c0wrk threshold override)

When the active model profile's `loop_hardening` variant is on (master `model_profiles.enabled` AND the profile's `loop_hardening.enabled` — see [../model-profiles.md](../model-profiles.md)), `core/builder.go` `applyLoopHardening` overrides the executor's circuit-breaker `CircuitBreakerConfig` with tighter-or-equal thresholds (`repeat_nudge_threshold`, `parse_error_abort_threshold`, `fruitless_nudge_threshold`, `fruitless_abort_threshold`, `same_tool_repeat_nudge_threshold` — four strictly tighter, `parse_error_abort_threshold` equal to the baseline) so a small model that repeats itself or stalls is nudged/aborted sooner. Those five thresholds are replaced by the profile's values; every other `CircuitBreakerConfig` field (`repeatAbortThreshold`, `truncationAbortThreshold`, `fruitlessMaxResultLen`, `sameToolRepeatAbortThreshold`, `sameToolResultSizeDelta`) keeps its baseline. The override is applied once at builder construction and is inert when the profile is off.

### Verify-on-Edit Mechanical Verification

`executor.verify_on_edit` (see `config.example.yaml`) enables a post-write verification hook (engine primitive: sp4rk `agent/verify_on_edit.go`): after every **successful** `write_file` / `edit_file` in a CODE task, the executor runs the configured command (e.g. `go test ./...`, `npm test`) and injects its truncated output back into the agent context as a system observation — "edit → verify → result" instead of self-attested completion.

- The command is ALWAYS taken from config, never from model output (hard security constraint — the model cannot choose what gets executed).
- `ExecuteUnattended` keeps the hard security gates intact; the hook is purely observational.
- CHAT mode and the goal loop are excluded; the hook only applies to the executor's edit path.

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

- Tool-call failure: the step records `IsError: true` with the failure text as its `Observation` (`agent.Step` carries no error value) and the loop continues; only a context error propagates out of the call.
- Run failure (`Executor.Run` returns an error): the sp4rk Conductor maps it to `ExecutionStatusFailed` (`ExecutionStatusCancelled` on context cancellation, `ExecutionStatusPaused` on `agent.ErrPaused`); `persistTaskOutcome` marks the task failed, which stays resumable.
- Context cancelled: propagates immediately; pending async delegations are cancelled via context-tree cancellation.
- Step limit reached without finish: `ExecutorResult.Finished` is false, which the sp4rk Conductor maps to `ExecutionStatusPartial`; `persistTaskOutcome` leaves the task `in_progress` (resumable).

## Invariants

- `finish` is ALWAYS available in every executor instance (never filtered out).
- Consumer meta-tools registered via `AddNonCacheableTools` are excluded from EAGER caching; a result that is truncated anyway is cached on demand and stays recoverable via `tool_result_read`.
- Subagent toolsets resolve from capability groups (`resolveTaskTools`, ADR-024); the `system` group is always included.
- Each executor instance has its own `ContextManager` (isolated memory); the Conductor context never shares with subagent contexts.
- Untrusted tool output is wrapped in `<untrusted-content>` before reaching the LLM (engine behavior; c0wrk's session-root/auto-approval gating runs in the registry, see [../../architecture/security-model.md](../../architecture/security-model.md)).

## Related Specs

- [sp4rk executor](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md) — canonical ReAct loop, circuit breakers, gates, truncation, caching, batch interception
- [conductor.md](conductor.md) — Conductor (top-level Executor caller)
- [delegation.md](delegation.md) — subagent launch via the `delegate` tool
- [../memory/compaction.md](../memory/compaction.md) — compaction strategies
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement and injection defense
