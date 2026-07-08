# Subagents

The SDK supports parallel plan execution through **subagents** — goroutine-based wrappers around an `Executor` that run independent plan steps concurrently. This document covers the `SubAgentTask` bundle, the `RunSubAgent` launcher, parallel execution, context injection, event emission, and the `TrajectoryStore` used to capture executor steps.

```go
import (
    "context"

    "github.com/v0lka/sp4rk/agent"
    "github.com/v0lka/sp4rk/tools"
)
```

## Overview

A plan is a DAG of steps. Steps with no dependencies between them can run in parallel to reduce total latency. The SDK achieves this by giving each independent step its own `Executor` (since an `Executor` is not safe for concurrent use on a single instance) and launching it in a goroutine via `RunSubAgent`. Each subagent runs to completion and reports its result on a channel.

## SubAgentTask

`SubAgentTask` bundles everything a subagent needs to run a step: the executor, its context manager, its task tools, a description, an event emitter, and an optional checklist callback.

```go
type SubAgentTask struct {
    StepID         string
    Executor       *Executor
    CM             ContextManager
    TaskTools      []tools.ToolDescriptor
    TaskDesc       string             // task description (for SubAgentLaunch event)
    Emitter        Events        // event emitter (nil-safe)
    TodoUpdateFunc StepTodoUpdateFunc // optional callback for update_checklist tool
}
```

| Field | Description |
|-------|-------------|
| `StepID` | The plan-step identifier. Injected into the context so context-aware tools know the current step. |
| `Executor` | A fresh `Executor` for this step. Must not be shared across concurrent subagents. |
| `CM` | The `ContextManager` owning this step's prompt history and compaction. |
| `TaskTools` | The tools available to this step. The `finish` tool is appended automatically. |
| `TaskDesc` | A human-readable task description, emitted in the `SubAgentLaunch` event. |
| `Emitter` | The `Events` sink for this subagent. `nil` is replaced with `NoopEvents`. |
| `TodoUpdateFunc` | Optional callback invoked when the step's checklist is updated. Wired into the context for the `update_checklist` tool. |

## RunSubAgent

`RunSubAgent` starts the executor in a goroutine and returns a channel for the result. It is the primary entry point for launching a single subagent.

```go
func RunSubAgent(
    ctx context.Context,
    stepID string,
    executor *Executor,
    cm ContextManager,
    taskTools []tools.ToolDescriptor,
    taskDesc string,
    emitter Events,
    todoUpdateFunc StepTodoUpdateFunc,
) (resultCh <-chan SubAgentResult)
```

The returned channel is buffered (capacity 1) and closed when the goroutine finishes. Read exactly one `SubAgentResult` from it.

### What RunSubAgent does

1. **Emits `SubAgentLaunch`** — `emitter.SubAgentLaunch(stepID, taskDesc)` fires immediately, before the executor starts.
2. **Injects context** — the task description, step ID, and (if provided) the checklist callback are attached to the context (see [Context injection](#context-injection)).
3. **Runs the executor** — `executor.Run(ctx, taskTools, cm)` is called inside the goroutine.
4. **Defense-in-depth check** — even if the executor reports `Finished: true`, the output is scanned for printed tool-call syntax (a failure mode where the model writes a fenced code block with a tool-name tag instead of emitting a `tool_use` block). If detected, the result is treated as a failure.
5. **Emits `SubAgentComplete`** — `emitter.SubAgentComplete(stepID, success, duration)` fires with the outcome and wall-clock duration.
6. **Sends the result** — a `SubAgentResult` is sent on the channel.

### Context cancellation

The goroutine respects context cancellation. Because the executor's LLM calls and tool executions use the same context, cancelling `ctx` causes `executor.Run` to return promptly. The result channel is always closed (via `defer`), so receivers never block forever.

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

resultCh := agent.RunSubAgent(ctx, "step_2", exec, cm, taskTools, "Refactor auth module", events, nil)

// Later, if you need to abort:
cancel()

result := <-resultCh // always returns, even after cancellation
```

## SubAgentResult

`SubAgentResult` is the outcome of a subagent run.

```go
type SubAgentResult struct {
    StepID string
    Output string
    Error  error
    Steps  []Step // actual executor steps (tool calls + observations)
}
```

| Field | Description |
|-------|-------------|
| `StepID` | The plan-step identifier this result corresponds to. |
| `Output` | The finish answer on success, or the abort reason on failure. |
| `Error` | Non-nil if the executor errored, was cancelled, or did not finish. |
| `Steps` | The full sequence of executor steps (tool calls + observations). Empty when the executor returned no result. |

### Success vs. failure semantics

A subagent is considered **successful** only when `executor.Run` returns no error *and* `result.Finished == true`. Two failure cases produce an `Error`:

- **Executor error** — the LLM call or a tool returned a fatal error, or the context was cancelled. `Steps` is populated from the partial result when available.
- **Did not finish** — the step budget was exhausted or a circuit breaker aborted. The executor's `Output` (which contains the specific abort reason, e.g. "circuit breaker", "fruitless abort", "max steps") is wrapped into the error message.

```go
result := <-resultCh
if result.Error != nil {
    log.Printf("step %s failed: %v", result.StepID, result.Error)
    return
}
log.Printf("step %s done: %s", result.StepID, result.Output)
```

## Context injection

`RunSubAgent` attaches several values to the context before calling `executor.Run`. These are read by context-aware tools during execution.

| Helper | Purpose |
|--------|---------|
| `tools.WithTaskContext(ctx, taskDesc)` | Attaches the task description so tools know what the agent is working on. |
| `agent.WithStepID(ctx, stepID)` | Attaches the plan-step ID. Used by the file tracker and other context-aware tools. |
| `agent.WithStepTodoUpdateFunc(ctx, fn)` | Attaches the checklist callback so the `update_checklist` tool can emit to-do updates. |

These are the same helpers used in standalone executor runs (see [Agent Executor](agent-executor.md#context-helpers)); `RunSubAgent` simply wires them automatically.

## Event emission

Subagents emit two events through the provided `Events` sink:

- **`SubAgentLaunch(stepID, description)`** — fired when the goroutine starts, before the executor runs.
- **`SubAgentComplete(stepID, success, duration)`** — fired when the goroutine finishes, with the success flag and wall-clock duration.

If `emitter` is `nil`, `RunSubAgent` substitutes `NoopEvents`, so the events are silently dropped. See [Events](events.md#sub-agent-events) for the full method signatures.

## Defense-in-depth: DetectToolCallSyntaxInContent

A known LLM failure mode is *printing* a tool invocation as prose — writing a fenced code block with a tool-name language tag (e.g. ` ```bash_exec `) instead of emitting a proper `tool_use` block. The executor's implicit-finish detector should catch this and abort with `Finished: false`, but `RunSubAgent` adds a second guard as defense-in-depth:

```go
if success && DetectToolCallSyntaxInContent(result.Output) {
    success = false
    err = errors.New("model printed tool-call syntax as text instead of using tool_use blocks")
}
```

`DetectToolCallSyntaxInContent` reports whether content contains tool-call syntax printed as text. It is an exported function so callers can use it independently:

```go
if agent.DetectToolCallSyntaxInContent(output) {
    // the model wrote a tool call as text — not a real finish
}
```

## Parallel plan execution

`RunSubAgentsParallel` runs multiple `SubAgentTask`s concurrently and collects all results. It is the convenience entry point for executing independent DAG steps in parallel.

```go
func RunSubAgentsParallel(ctx context.Context, agents []SubAgentTask) []SubAgentResult
```

Results are collected in input order (not completion order); a slow agent blocks all subsequent results from being returned. The function blocks until all subagents have reported. If `agents` is empty, it returns `nil`.

```go
tasks := []agent.SubAgentTask{
    {StepID: "step_2", Executor: execA, CM: cmA, TaskTools: toolsA, TaskDesc: "Write tests", Emitter: events},
    {StepID: "step_3", Executor: execB, CM: cmB, TaskTools: toolsB, TaskDesc: "Update docs", Emitter: events},
    {StepID: "step_4", Executor: execC, CM: cmC, TaskTools: toolsC, TaskDesc: "Refactor API", Emitter: events},
}

results := agent.RunSubAgentsParallel(ctx, tasks)
for _, r := range results {
    if r.Error != nil {
        log.Printf("%s failed: %v", r.StepID, r.Error)
    } else {
        log.Printf("%s done: %s", r.StepID, r.Output)
    }
}
```

> **Note:** each `SubAgentTask` must have its own `Executor` and `ContextManager`. Sharing a single `Executor` across concurrent tasks violates the executor's single-execution invariant. The orchestration layer creates a fresh executor and context manager per step.

### Manual parallel execution

For finer control (e.g. streaming results as they arrive, or limiting concurrency), launch subagents individually and select on their channels:

```go
channels := make([]<-chan agent.SubAgentResult, len(tasks))
for i, t := range tasks {
    channels[i] = agent.RunSubAgent(ctx, t.StepID, t.Executor, t.CM,
        t.TaskTools, t.TaskDesc, t.Emitter, t.TodoUpdateFunc)
}

// Collect results as they complete.
for _, ch := range channels {
    r := <-ch
    handleResult(r)
}
```

## TrajectoryStore

`TrajectoryStore` captures the executor's step history so tools (e.g. a reflector) can access the trajectory during execution. The executor syncs its steps to the store at each loop iteration.

```go
type TrajectoryStore interface {
    Sync(steps []Step)
    Steps() []Step
}
```

Inject a store into the context before running a subagent (or any executor):

```go
type trajStore struct {
    mu    sync.Mutex
    steps []agent.Step
}

func (s *trajStore) Sync(steps []agent.Step) { s.mu.Lock(); s.steps = steps; s.mu.Unlock() }
func (s *trajStore) Steps() []agent.Step     { s.mu.Lock(); defer s.mu.Unlock(); return s.steps }

store := &trajStore{}
ctx = agent.WithTrajectoryStore(ctx, store)

resultCh := agent.RunSubAgent(ctx, "step_2", exec, cm, taskTools, "Investigate bug", events, nil)
result := <-resultCh

// The store now holds the full trajectory, synced at each iteration.
trajectory := store.Steps()
```

Retrieve the store from inside a tool via the context:

```go
if ts := agent.TrajectoryStoreFrom(ctx); ts != nil {
    for _, step := range ts.Steps() {
        // inspect thought, action, observation...
    }
}
```

`TrajectoryStore` is particularly useful in plan-and-reflect orchestration: the reflector reads the trajectory to analyze why a step failed and produce corrective insights. See [Events](events.md#orchestrationevents) for the `OnReflected` hook.

## Putting it together

A complete parallel execution with trajectory capture:

```go
// One trajectory store per step (each subagent has its own executor).
storeA, storeB := &trajStore{}, &trajStore{}
ctxA := agent.WithTrajectoryStore(ctx, storeA)
ctxB := agent.WithTrajectoryStore(ctx, storeB)

tasks := []agent.SubAgentTask{
    {
        StepID:    "step_2",
        Executor:  newExecutorForStep("step_2"),
        CM:        newContextManagerForStep("step_2"),
        TaskTools: taskTools,
        TaskDesc:  "Implement feature A",
        Emitter:   events,
    },
    {
        StepID:    "step_3",
        Executor:  newExecutorForStep("step_3"),
        CM:        newContextManagerForStep("step_3"),
        TaskTools: taskTools,
        TaskDesc:  "Implement feature B",
        Emitter:   events,
    },
}

results := agent.RunSubAgentsParallel(ctx, tasks)
for _, r := range results {
    if r.Error != nil {
        log.Printf("step %s failed: %v (output: %s)", r.StepID, r.Error, r.Output)
        continue
    }
    log.Printf("step %s succeeded: %s", r.StepID, r.Output)
}
```
