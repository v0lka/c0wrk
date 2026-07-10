# Executor

## Role

The ReAct loop primitive (Thought → Action → Observation) with circuit breakers, mutation/checklist gates, two-stage tool-result truncation, and implicit-finish detection. It is the load-bearing primitive shared by two callers: the [Conductor](conductor.md) (a top-level `Executor.Run` that owns a task end-to-end) and **subagents** (isolated `Executor.Run` instances launched in goroutines). The Executor is agnostic to which caller invoked it.

## Key Files

- `github.com/v0lka/sp4rk/agent` — `Executor`, `NewExecutor`, `Executor.Run`, `ExecutorResult`, `Step`, `FinishTool`, `CircuitBreakerConfig`, `ToolResultBudget`, `ToolResultCache`, `DetectToolCallSyntaxInContent`, configuration setters (`Set*`), context helpers
- `github.com/v0lka/sp4rk/agent` (executor internals) — single-call dispatch, batch meta-tool interception, implicit-finish handling, mutation/checklist gate logic
- `github.com/v0lka/sp4rk/agent` — `ContextManager` / `CompactionStrategy` / `FillCheck` interfaces, `LLMCaller`, `ToolExecutor`, `Events`, `HITLHandler`
- `github.com/v0lka/sp4rk/tools/builtins` — `batch` meta-tool descriptor (intercepted at the executor, never executed directly)

## Behavior

The Executor is **not safe for concurrent use on a single instance** — `Run` must be called one at a time. Callers that need parallelism create a fresh `Executor` per step (see [subagents.md](subagents.md)).

### NewExecutor

```go
func NewExecutor(llmRouter LLMCaller, toolRegistry ToolExecutor, maxSteps int, opts ...Option) *Executor
```

The event emitter and the HITL handler are **nil-safe** — `nil` is replaced with `NoopEvents` and `NoopHITLHandler`. Options: `WithTokenCounter`, `WithEvents`, `WithSuppressAssistantEvents` (hides streaming events for sub-steps), `WithToolResultBudget` (defaults to `DefaultToolResultBudget()`), `WithCircuitBreaker` (defaults to `DefaultCircuitBreakerConfig()`), `WithHITL`.

### Run

```go
func (e *Executor) Run(ctx context.Context, taskTools []tools.ToolDescriptor, cw ContextManager) (*ExecutorResult, error)
```

`ctx` carries cancellation, workspace path, trajectory store, and other injected dependencies. `taskTools` are the tools available for this run; the `finish` tool is appended automatically if absent. A non-nil error indicates a fatal failure (LLM error, context cancellation). A `nil` error with `Finished == false` means the budget was exhausted or a circuit breaker aborted the loop.

### ReAct loop lifecycle (per iteration)

1. **Trajectory sync** — if a `TrajectoryStore` is in `ctx`, the current step history is synced so tools (e.g. a reflector) can read it.
2. **Step-limit boundary** — if the budget is reached, `HITLHandler.OnStepLimit` decides whether to grant more steps.
3. **LLM call** — the prompt is built from the context manager and sent. If the provider reports a context-window-exceeded error, reactive compaction runs and the call is retried.
4. **Implicit-finish check** — if the model returns no tool calls, the executor decides whether this is a legitimate finish or a failure mode (printed tool-call syntax); a nudge may force an explicit `finish`.
5. **Truncation detection** — `max_tokens` with tool calls present ⇒ nudge injected, truncation counter checked against the circuit breaker.
6. **Tool execution** — each call runs after HITL approval; results are truncated in two stages, cached if applicable, recorded as observations.
7. **Compaction** — context fill is checked; compaction runs and `ContextFill`/`ContextCompaction` events fire when thresholds cross.

The loop terminates when `finish` is called (`Finished: true`) or the budget is exhausted (`Finished: false`).

### Circuit breakers

`CircuitBreakerConfig` holds thresholds that protect the executor from unproductive loops. When a threshold is crossed, a nudge is injected and, if the behavior persists, the loop aborts with `Finished: false`.

| Detection | Trigger | Abort action |
| --------- | ------- | ------------ |
| Repeat | Consecutive identical tool calls (name + args) | `HITLHandler.OnStepLimit` |
| Truncation | Consecutive `max_tokens`-truncated responses with tool calls | `HITLHandler.OnStepLimit` |
| Parse error | Consecutive parse errors on the same tool | `HITLHandler.OnStepLimit` |
| Fruitless | Consecutive minimal-result calls | `HITLHandler.OnStepLimit` |
| Same tool | Same tool with varied args but similar results | `HITLHandler.OnStepLimit` |

On an abort, `HITLHandler.OnStepLimit` is invoked with the trigger reason and the same three options as the step limit: **AllowOnce** (reset + nudge), **AllowAlways** (disable that breaker + nudge), **Deny** (stop). If `HITLHandler` is nil, the executor aborts immediately (headless/test behavior).

### Mutation gate

When `SetMutationRequired(true)` is set, the finish call is intercepted before completion. The gate checks whether any mutating tool executed **successfully** during the current step (scanning the trajectory for mutating tool names, excluding rejected/errored calls). No mutation + first attempt → inject a mutation nudge and retry; no mutation + second attempt → return `Finished: false`. Rejected tool calls and ambiguous tools (e.g. shell execution) do **not** count as mutations. The host enables this gate selectively (e.g. for code-modification steps).

### Checklist gate

Enabled by default (`SetChecklistGateEnabled`, default `true`); it activates only when an `update_checklist` tool is present in the run's tool set. Two soft sub-gates, each one nudge attempt before finish is accepted:

- **Missing-checklist**: a non-trivial step (more than a configurable productive-call threshold) with no successful `update_checklist` call → inject a missing-checklist nudge, retry.
- **Unchecked-items**: the last successful checklist has unchecked items → inject an unchecked-items nudge, retry.

### Implicit finish & failure-mode detection

When the LLM returns no tool calls, the executor decides whether to accept an implicit finish, nudge, or abort:

- Up to a small budget of general nudges are injected before accepting an implicit finish. In `suppressAssistantEvents` mode, a finish nudge requires an explicit `finish` call.
- **Failure mode**: `DetectToolCallSyntaxInContent` matches a fenced code block whose language tag looks like a tool name — the model "printed" a tool invocation as prose instead of emitting a `tool_use` block. A dedicated nudge is injected a few times; after that the executor aborts with `Finished: false` (never a silent success).

### Finish guard

`SetFinishGuard(func(ctx) error)` lets a caller block premature completion. A non-nil error rejects `finish` with a nudge; on the second attempt finish is accepted regardless (soft gate). This is how the Conductor's pending-delegations join check is expressed.

### Tool result cache & two-stage truncation

Every cacheable tool result is stored in `ToolResultCache` (keyed by `SHA256(toolName + "\x00" + content)`) before truncation:

- **Stage 1 — per-tool line/byte truncation** (`ToolTruncationConfig`): byte truncation is UTF-8 safe. Defaults ship for `read_file`, `ripgrep`, `glob`, `list_directory`, `web_fetch`, `bash_exec`.
- **Stage 2 — token budget** (`ToolResultBudget`): `HardCapTokens` / `MaxFillFraction` (defaults `30000` / `0.4`). When a result exceeds the budget it is truncated and a fragmentation nudge is appended telling the model how to retrieve fragments via a `tool_result_read` tool using the cache hash.

Cache behaviours: identical content from different tools gets different hashes; dedup of repeated identical calls; file coherence for file-based tools (`read_file`/`write_file`/`edit_file`) via path+mtime+size; MCP-sourced entries expire after a TTL while non-MCP entries never expire; meta-tools (`finish`, `tool_result_read`, `store_fact`, …) are excluded by default and additional names can be added via `AddNonCacheableTools`.

### Batch meta-tool

The `batch` tool lets the model dispatch multiple tool calls in one turn. It is intercepted by the executor before reaching the registry; its own `Execute()` returns an error. Sub-calls go through the full policy + truncation + caching pipeline, are emitted with a `(batched)` suffix, and per-sub-call errors do not abort the batch.

## Error Handling

- **Fatal LLM/tool error**: `Run` returns a non-nil error.
- **Context cancelled**: propagated immediately, no retry.
- **Budget exhausted without finish**: `Finished: false`, treated as incomplete (not an error).
- **Tool not found / parse failure**: surfaced as `ToolResult{IsError: true}`, not a Go error.

## Invariants

- The `finish` tool is always available in every run (appended automatically if absent).
- A single `Executor` instance is never used concurrently — parallel callers create one per step.
- When `mutationRequired` is set, finish without a prior successful mutating tool is rejected (nudge then `Finished: false`).
- Both checklist sub-gates are soft: after one nudge attempt, finish is accepted regardless.
- Tool results from untrusted sources are wrapped in `<untrusted-content>` tags before becoming an LLM message (when injection defense is enabled on the `ContextManager`).
- Every `Step` carries `IsUntrusted` (set after tool execution via `tool.IsUntrusted()` or MCP source check) and `CacheHash` (empty for non-cacheable tools).
- `batch` is intercepted before the registry; its sub-calls are cached individually.

## Related Specs

- [README.md](README.md) — orchestration overview
- [conductor.md](conductor.md) — the top-level Executor caller
- [subagents.md](subagents.md) — isolated Executor instances in goroutines
- [reflector.md](reflector.md) — reads the trajectory via `TrajectoryStore`
- [../memory/compaction.md](../memory/compaction.md) — compaction strategies driving the per-iteration fill check
- [../tool-system/README.md](../tool-system/README.md) — tool execution pipeline and trust classification
