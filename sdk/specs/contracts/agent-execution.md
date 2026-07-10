# Contract: Agent Execution Surface

> This contract documents the public execution interface an embedding application implements and consumes to drive an sp4rk agent. It is the boundary between the generic ReAct execution engine (`github.com/v0lka/sp4rk/agent`) and the host application that wires it into a larger system.

## Boundary Rule

The host application consumes the agent execution types from `github.com/v0lka/sp4rk/agent` directly. The agent package depends only on sibling sp4rk packages (`llm`, `tools`); it never imports host-application code. An embedder plugs in behavior by **implementing** the interfaces below (`Events`, `HITLHandler`, `ContextManager`, `LLMCaller`, `CompactionStrategy`) and **consuming** the value types (`Step`, `ExecutorResult`) and the executor entry point, rather than subclassing the loop.

## Interfaces

| Interface / Type | Package | Implemented / Consumed By | Purpose |
| --- | --- | --- | --- |
| `Events` | agent | Implemented by host | Universal agent lifecycle callbacks (step start/complete, tool call/result, assistant streaming, context fill/compaction, sub-agent launch/complete, finishing, diagnostics) |
| `NoopEvents` | agent | Provided by SDK | No-op `Events` for struct embedding convenience |
| `HITLHandler` | agent | Implemented by host | Human-in-the-loop hooks: `OnToolCall` (intercept/modify/reject) and `OnStepLimit` (extend/stop) |
| `NoopHITLHandler` | agent | Provided by SDK | Default handler that allows all tool calls and denies step-limit extensions |
| `ToolExecutor` | agent | Implemented by `tools.ToolRegistry` | Tool execution the executor needs: `Execute`, `GetToolSource`, `IsToolUntrusted` |
| `LLMCaller` | agent | Implemented by `llm.Router` | Minimal LLM call interface the executor needs: `Call(ctx, ChatRequest)(*ChatResponse, error)` |
| `ContextManager` | agent | Extended by host | Context window management (prompt building, step addition, compaction, fill tracking). **Note:** the SDK-level interface intentionally omits `SetTask`; the host application adds task-context concerns on top |
| `CompactionStrategy` | agent | Implemented by host / SDK strategies | Compresses step history into a compact `[]llm.Message` within a token budget |
| `Step` | agent | Consumed by host | Single ReAct iteration record (thought, reasoning, action `llm.ToolCall`, observation, error/untrusted flags, cache hash) |
| `ExecutorResult` | agent | Consumed by host | Executor output: final string, the full `[]Step` trajectory, and a `Finished` flag |
| `StepLimitResponse` | agent | Consumed by host | `allow_once` / `allow_always` / `deny` returned from `OnStepLimit` |
| `HITLToolDecision` | agent | Consumed by host | `{Allow bool; ModifiedInput json.RawMessage; Reason string}` returned from `OnToolCall` |
| `CircuitBreakerConfig` | agent | Provided by host config | Circuit-breaker thresholds (repeat/truncation/parse-error/fruitless abort) protecting the loop |

## Initialization

At startup the host constructs the execution surface in this order:

1. Build an `llm.TokenCounter` and an `llm.ModelRegistry`, then create an `llm.Router` (`NewRouter`) which satisfies `LLMCaller`.
2. Build a `tools.ToolRegistry` (it satisfies `agent.ToolExecutor`) and register built-in and MCP tools.
3. Construct a `ContextManager` implementation, injecting a `CompactionStrategy` and the token counter.
4. Construct the executor, wiring `LLMCaller` (the Router), `ToolExecutor` (the registry), `ContextManager`, an `Events` implementation, an `HITLHandler`, and a `CircuitBreakerConfig`.
5. The host invokes the executor's `Run` to start the ReAct loop for a task.

The host supplies concrete `Events` and `HITLHandler` implementations; `NoopEvents` and `NoopHITLHandler` exist for embedders that need only a subset of callbacks.

## Data Flow Across Boundary

- **Host → executor (in):** task/system context (via the `ContextManager`), the configured `LLMCaller`, `ToolExecutor`, `Events`, `HITLHandler`, and circuit-breaker thresholds.
- **executor → LLMCaller:** `llm.ChatRequest` (messages, tools, max tokens, temperature, reasoning effort).
- **LLMCaller → executor:** `llm.ChatResponse` (message, reasoning, usage, stop reason).
- **executor → ToolExecutor:** tool name + `json.RawMessage` input.
- **ToolExecutor → executor:** `tools.ToolResult` (`{Content string; IsError bool}`) plus an error.
- **executor → Events:** lifecycle callbacks carrying step numbers, tool names/args, result previews, fill percentages, and diagnostics.
- **executor → HITLHandler:** pre-execution tool call (`OnToolCall`) and budget-exhaustion events (`OnStepLimit`).
- **executor → host (out):** `ExecutorResult` containing the final output and the full `[]Step` trajectory. Each `Step` carries the `IsUntrusted` flag so the host can treat external-source observations defensively.

Data is plain Go values (structs, slices, `json.RawMessage`). No host-specific types cross this boundary.

## Error Propagation

- A tool that **fails** returns a `tools.ToolResult` with `IsError=true` (a recoverable, in-loop result). This is recorded on the producing `Step` (`IsError`) and fed back as the observation so the model can self-correct; it is **not** propagated as a Go `error` out of the loop.
- A tool execution that errors at the infrastructure level returns a Go `error`, which the executor surfaces per its loop contract.
- `LLMCaller` errors (network, auth, context window) propagate through the executor and out of `Run`. Transient errors (HTTP 429/502/503/529) are retried with exponential backoff inside `llm.Router` before reaching the executor.
- `HITLHandler` methods may return a Go `error`; the executor treats a handler error according to its loop contract (typically aborting the affected step).
- `HITLToolDecision` with `Allow=false` is **not** an error — it is a normal rejection that becomes the tool's observation.
- Circuit-breaker aborts (repeated identical calls, repeated truncation, repeated parse errors, fruitless loops) terminate the loop and are reflected in `ExecutorResult.Finished=false`.

## Breaking Change Checklist

- If you add a method to `Events`, you MUST update **every** `Events` implementation (and any orchestration-level `Events` that embeds `agent.Events`), or fail to compile. `NoopEvents` is the migration stub — embed it to inherit new no-op methods.
- If you change the `HITLHandler` method set, you MUST update `NoopHITLHandler` and all host implementations.
- If you change `ToolExecutor`, you MUST verify `tools.ToolRegistry` still satisfies it.
- If you change `LLMCaller`, you MUST verify `llm.Router` still satisfies it (the executor's call site depends on `Call`).
- If you alter the `ContextManager` interface, you MUST update the host's extended context manager; remember the SDK interface deliberately omits `SetTask`.
- If you change `Step` fields read by the host (e.g. `IsUntrusted`, `CacheHash`, `ResponseGroup`), you MUST update serialization, prompt builders, and any trajectory tooling.
- If you change `ExecutorResult`, you MUST update every `Run` consumer (conductors, sub-agent runners, tests).
- If you add a circuit-breaker dimension to `CircuitBreakerConfig`, you MUST provide a documented default and update host configuration parsing.
