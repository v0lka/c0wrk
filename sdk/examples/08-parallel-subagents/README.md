# Example 08 — Parallel Subagents

The SDK exposes a low-level **parallel-execution primitive** that no other
example touches: `agent.RunSubAgent` / `agent.RunSubAgentsParallel`. (The
`Conductor` and `fw.TaskF` execute plan steps **sequentially** — the
`PlanStep.Parallelible` flag is defined but never consumed by the engine.)

This example demonstrates that primitive directly: two genuinely independent
file-generation tasks run **concurrently**, each in its own goroutine.

This example ships in **two variants**:

| Variant     | File            | Command                 | Notes                                            |
|-------------|-----------------|-------------------------|--------------------------------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` | Fluent assembly (`NewF`) + classic `RunSubAgentsParallel` escape |
| **Classic** | `main.go`       | `go run .`              | Low-level: `RunSubAgent` per goroutine, results in completion order |

See [`docs/subagents.md`](../../docs/subagents.md).

## What you will learn

- Why an `Executor` cannot be shared across goroutines (and why each subagent needs its own)
- How to build a per-subagent `Executor` + `ContextManager` via `fw.ContextFactory()`
- `agent.RunSubAgent` (one subagent, returns a result **channel**) vs. `agent.RunSubAgentsParallel` (many, blocks, input-order results)
- The `SubAgentResult` contract (`StepID`, `Output`, `Error`, `Steps`)

## The model

```
shared: Framework (Router, ToolRegistry, ContextManagerFactory)
        │
        ├── goroutine A: fresh Executor + fresh CM → RunSubAgent("go-app", …) → chA
        └── goroutine B: fresh Executor + fresh CM → RunSubAgent("py-app", …) → chB
                                                                  │
                          read <-chA / <-chB, gather results ─────┘
```

Each `RunSubAgent` call:

1. emits `SubAgentLaunch(stepID, taskDesc)`,
2. injects the task description + step ID into the context,
3. runs `executor.Run` in the goroutine,
4. emits `SubAgentComplete(stepID, success, duration)`,
5. sends one `SubAgentResult` on a buffered (cap 1) channel and closes it.

The result channel is **always closed** (via `defer`), so receivers never block
forever — even after context cancellation.

### Manual vs. parallel collection

- **`RunSubAgent`** (classic variant) returns one channel per subagent. Reading
  them yourself lets you observe results in **completion order**.
- **`RunSubAgentsParallel`** (fluent variant) collects all results and returns
  them in **input order**: a slow agent blocks later results from being returned.

## Run it

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
cd sdk/examples/08-parallel-subagents
go run -tags fluent .   # or: go run .
```

Each subagent builds a self-contained mini-project in its own subdirectory
(`go-app/`, `py-app/`), reads the file back to verify, and calls `finish`.
Running the two concurrently roughly halves the wall-clock vs. doing them
sequentially — the example prints the elapsed time so the win is visible.

## Trajectory capture

To inspect *what* a subagent did, attach a `TrajectoryStore` to its context
before launching — the executor syncs its step history to it each iteration:

```go
store := &myTrajectoryStore{}                 // implements agent.TrajectoryStore
stepCtx := agent.WithTrajectoryStore(ctx, store)
ch := agent.RunSubAgent(stepCtx, …)
// after <-ch: store.Steps() holds the full ReAct trajectory
```

This is the same mechanism the Reflector uses to analyze a failed step.
