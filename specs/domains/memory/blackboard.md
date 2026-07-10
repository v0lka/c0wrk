# Blackboard

## Role

c0wrk persists and restores task state — plan, step results, reflections, and facts — across sessions on top of the sp4rk `Blackboard` abstraction. The `Blackboard` interface, the in-memory `MapBlackboard`, and the step-output / fact / final-result store adapters are **sp4rk engine** primitives; c0wrk adds SQLite-backed persistence and restore. See [the sp4rk blackboard spec](../../../sdk/specs/domains/memory/blackboard.md) for the canonical interface and adapters.

## Key Files

- `backend/session/persistent_blackboard.go` — `PersistentBlackboard` (wraps sp4rk `MapBlackboard` + SQLite store; auto-saves on writes, supports restore)
- `core/persistent_blackboard.go` — `PersistableBlackboard` interface + `TaskPersistence` store interface (persistence contract types the orchestrator uses for BB restoration)
- `core/orchestrator.go` / `core/orchestrator_handle.go` — Blackboard lifecycle: create per first message, restore for continuations (`opts.TaskID != ""`)

Engine files (`github.com/v0lka/sp4rk/orchestration/blackboard.go` `MapBlackboard`, `interfaces.go` `Blackboard`, `stepoutput_adapter.go` / `factstore_adapter.go` / `finalresult_adapter.go`) are documented in [the sp4rk blackboard spec](../../../sdk/specs/domains/memory/blackboard.md).

## c0wrk Persistence & Restore

### PersistentBlackboard

Wraps sp4rk `MapBlackboard` with SQLite persistence:

- Automatically saves state on `SetStepResult`, `StoreFact`, `SetFinalResult`
- Supports `RestoreBlackboard()` for task resumption — recreates the full in-memory state from SQLite
- Tracks task lifecycle: `ReactivateTask()`, `CompleteTask()`, `FailTask()`, `CancelTask()`
- Emits warnings via `Emitter` if persistence fails (non-fatal)

### Restore Flow

```
HandleMessage(ctx, message, opts)
│
├─ opts.TaskID == "" → create new Blackboard via bbFactory (fresh PersistentBlackboard)
└─ opts.TaskID != "" → restore Blackboard from persistence
   ├─ RestoreBlackboard() → in-memory MapBlackboard populated from SQLite
   ├─ ReactivateTask() → mark task in_progress
   └─ existing routing decision reused (continuation fast-path; see ../orchestration/router.md)
```

### Persistence Contract (core)

`core/persistent_blackboard.go` defines the contract types the orchestrator relies on for restoration:

- `PersistableBlackboard` — the persistence-capable Blackboard interface
- `TaskPersistence` — the store interface (SQLite-backed) for task lifecycle + blackboard state
- `BlackboardRestoreFunc` / `BlackboardFactory` — injected into the orchestrator at build time

## c0wrk Usage of Blackboard State

c0wrk exposes Blackboard state to the agent through internal tools (in `core/tools/`) backed by sp4rk store adapters:

| Tool | Backing adapter (sp4rk) | Purpose |
| ---- | ----------------------- | ------- |
| `store_fact` / `search_facts` | `FactStore` | Inter-step/inter-delegation fact memory |
| `read_step_output` / `list_step_outputs` | `StepOutputStore` | Read completed delegation outputs |
| `read_final_result` | `FinalResultStore` | Recover a prior task's outcome (e.g. after restart, or when too large to inject verbatim) |

### Fact Memory

Facts are the primary inter-step communication mechanism. A researcher delegation discovers an insight and calls `store_fact`; a dependent coder delegation calls `search_facts` to retrieve it. Facts accumulate monotonically (no deletion during a task).

### Final Result

The final result is set via `SetFinalResult(output)` after the Conductor finishes, then persisted to the `tasks.final_output` column by `CompleteTask` (called separately from `SetFinalResult`). For inline tasks (no delegations), the final result is the only blackboard state besides the plan and facts.

## Error Handling

- Persistence failure → logged + emitted as service warning, execution continues
- Missing step result for a dependency → empty context (not an error)
- Fact search with no matches → empty slice returned

## Invariants

- A Blackboard is created once per task (first message) and restored for continuations; it is never shared across tasks
- All Blackboard methods are safe for concurrent use (sp4rk `MapBlackboard` uses `sync.RWMutex`)
- Step results are immutable once written (no overwrite)
- Facts accumulate monotonically (no deletion during a task)
- `PersistentBlackboard` persists synchronously on each write
- `RestoreBlackboard` recreates the full in-memory state from SQLite

## Related Specs

- [sp4rk blackboard](../../../sdk/specs/domains/memory/blackboard.md) — canonical `Blackboard` interface, `MapBlackboard`, store adapters, `Checkpointer`
- [README.md](README.md) — memory overview
- [../orchestration/conductor.md](../orchestration/conductor.md) — how the Conductor reads blackboard state
- [../orchestration/delegation.md](../orchestration/delegation.md) — how `delegate` writes subagent results
- [../../architecture/data-flow.md](../../architecture/data-flow.md) — blackboard flow diagram
