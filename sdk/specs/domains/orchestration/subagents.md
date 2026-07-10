# Subagents

## Role

The delegated-execution primitive: goroutine-based wrappers around an `Executor` that run independent steps concurrently. The SDK exposes `RunSubAgent` (launch one isolated `Executor.Run` in a goroutine) and `RunSubAgentsParallel` (run several concurrently and collect results). Subagents power parallel plan execution and any host-defined delegation mechanism. This document describes the **primitive** the SDK exposes — host-built delegation tools (`delegate`, `cancel_delegation`, etc.) are constructed atop it.

## Key Files

- `github.com/v0lka/sp4rk/agent` — `SubAgentTask`, `RunSubAgent`, `RunSubAgentsParallel`, `SubAgentResult`, `DetectToolCallSyntaxInContent`, `StepTodoUpdateFunc`, context helpers (`WithStepID`, `WithStepTodoUpdateFunc`)
- `github.com/v0lka/sp4rk/agent` — `Executor`, `ContextManager`, `Events` (`SubAgentLaunch`/`SubAgentComplete` hooks)
- `github.com/v0lka/sp4rk/tools` — `ToolDescriptor`, `WithTaskContext`
- `github.com/v0lka/sp4rk/orchestration` — `CompletedStep`, DAG utilities (`FindReadySteps`) used to select independent steps

## Behavior

A plan is a DAG of steps. Steps with no dependencies between them can run in parallel to reduce total latency. The SDK achieves this by giving each independent step its own `Executor` (an `Executor` is not safe for concurrent use on a single instance) and launching it in a goroutine via `RunSubAgent`. Each subagent runs to completion and reports its result on a channel.

### SubAgentTask

```go
type SubAgentTask struct {
    StepID         string
    Executor       *Executor          // fresh executor for this step; never shared
    CM             ContextManager     // isolated prompt history + compaction
    TaskTools      []tools.ToolDescriptor
    TaskDesc       string             // emitted in the SubAgentLaunch event
    Emitter        Events             // nil-safe (replaced with NoopEvents)
    TodoUpdateFunc StepTodoUpdateFunc // optional update_checklist callback
}
```

### RunSubAgent

```go
func RunSubAgent(ctx, stepID string, executor *Executor, cm ContextManager,
    taskTools []tools.ToolDescriptor, taskDesc string, emitter Events,
    todoUpdateFunc StepTodoUpdateFunc) (resultCh <-chan SubAgentResult)
```

The returned channel is buffered (capacity 1) and closed when the goroutine finishes. Read exactly one `SubAgentResult`. What it does:

1. **Emits `SubAgentLaunch`** — `emitter.SubAgentLaunch(stepID, taskDesc)` fires before the executor starts.
2. **Injects context** — task description (`WithTaskContext`), step ID (`WithStepID`), and (if provided) the checklist callback (`WithStepTodoUpdateFunc`).
3. **Runs the executor** — `executor.Run(ctx, taskTools, cm)` inside the goroutine.
4. **Defense-in-depth check** — even if the executor reports `Finished: true`, the output is scanned for printed tool-call syntax; if detected the result is treated as a failure.
5. **Emits `SubAgentComplete`** — with the outcome and wall-clock duration.
6. **Sends the result** on the channel.

### Context cancellation

The goroutine respects context cancellation. Because the executor's LLM calls and tool executions use the same context, cancelling `ctx` causes `executor.Run` to return promptly. The result channel is always closed via `defer`, so receivers never block forever. The host expresses delegation cancellation by cancelling the subagent context (typically a child of a parent context).

### SubAgentResult & success semantics

```go
type SubAgentResult struct {
    StepID string
    Output string
    Error  error
    Steps  []Step
}
```

A subagent is **successful** only when `executor.Run` returns no error *and* `result.Finished == true`. Two failure cases produce an `Error`:

- **Executor error** — the LLM call or a tool returned a fatal error, or the context was cancelled. `Steps` is populated from the partial result when available.
- **Did not finish** — the step budget was exhausted or a circuit breaker aborted. The executor's `Output` (carrying the specific abort reason, e.g. "circuit breaker", "fruitless abort", "max steps") is wrapped into the error message.

### RunSubAgentsParallel

```go
func RunSubAgentsParallel(ctx context.Context, agents []SubAgentTask) []SubAgentResult
```

Runs multiple `SubAgentTask`s concurrently and collects all results. Results are collected in **input order** (not completion order); a slow agent blocks subsequent results from being returned. Blocks until all subagents have reported. Returns `nil` for an empty slice. For finer control (streaming results, concurrency limits) launch subagents individually and select on their channels.

### Defense-in-depth

`DetectToolCallSyntaxInContent` reports whether content contains tool-call syntax printed as text (a fenced code block with a tool-name language tag). `RunSubAgent` applies it as a second guard after the executor's own implicit-finish detector:

```go
if success && DetectToolCallSyntaxInContent(result.Output) {
    success = false
    err = errors.New("model printed tool-call syntax as text instead of using tool_use blocks")
}
```

The function is exported so callers can use it independently.

## Error Handling

- **Executor fatal error**: `Error` is set; `Steps` carries the partial trajectory when available.
- **Budget exhausted / circuit-breaker abort**: `Finished == false`; the abort reason is wrapped into `Error`.
- **Context cancelled** (parent cancellation or explicit cancel): the goroutine returns promptly; `Error` reflects the cancellation.

## Invariants

- Each `SubAgentTask` has its own `Executor` and `ContextManager` — sharing a single `Executor` across concurrent tasks violates the executor's single-execution invariant.
- The result channel is always closed; exactly one `SubAgentResult` is sent.
- A subagent never shares its `ContextManager` with its launcher or with other subagents.
- `SubAgentLaunch` always fires before the executor runs; `SubAgentComplete` always fires after, even on failure or cancellation.
- `success` requires `result.Finished && !DetectToolCallSyntaxInContent(result.Output)`.

## Related Specs

- [README.md](README.md) — orchestration overview
- [executor.md](executor.md) — the ReAct loop primitive each subagent runs
- [conductor.md](conductor.md) — the top-level caller that a host's delegation tool launches subagents from
- [../memory/README.md](../memory/README.md) — isolated context per subagent
