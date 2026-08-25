# Blackboard

## Role

c0wrk persists and restores task state — plan, step results, reflections, facts, and attachments — across sessions on top of the sp4rk `Blackboard` abstraction. The `Blackboard` interface, the in-memory `MapBlackboard`, and the step-output / fact / final-result / attachment store adapters are **sp4rk engine** primitives; c0wrk adds SQLite-backed persistence and restore. See [the sp4rk blackboard spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/blackboard.md) for the canonical interface and adapters.

## Key Files

- `backend/session/persistent_blackboard.go` — `PersistentBlackboard` (wraps sp4rk `MapBlackboard` + SQLite store; auto-saves on writes, supports restore)
- `core/persistent_blackboard.go` — `PersistableBlackboard` interface + `TaskPersistence` store interface (persistence contract types the orchestrator uses for BB restoration)
- `core/orchestrator.go` / `core/orchestrator_handle.go` — Blackboard lifecycle: create per first message, restore for continuations (`opts.TaskID != ""`)

Engine files (`github.com/v0lka/sp4rk/orchestration/blackboard.go` `MapBlackboard`, `interfaces.go` `Blackboard`, `stepoutput_adapter.go` / `factstore_adapter.go` / `finalresult_adapter.go` / `attachmentstore_adapter.go`) are documented in [the sp4rk blackboard spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/blackboard.md).

## c0wrk Persistence & Restore

### PersistentBlackboard

Wraps sp4rk `MapBlackboard` with SQLite persistence:

- Non-finalizing write methods are serialized through a single background worker goroutine (`persistSafe`, with a timeout and per-op panic recovery): `SetOriginalRequest`, `SetPlan`, `SetStepResult`, `AddReflection`, `StoreFact`, `AddAttachment`, `RemoveAttachment`, `SetRouting`
- Finalizing writes (`CompleteTask`, `FailTask`, `CancelTask`) run synchronously (`persistSynchronously`) so the status change is guaranteed persisted before the method returns; they also shut down the background worker
- `SetFinalResult` does NOT persist — it only sets the in-memory value; the final result is persisted to the `final_output` column later by `CompleteTask`
- Supports `RestoreBlackboard()` for task resumption — recreates the full in-memory state from SQLite
- Tracks task lifecycle: `ReactivateTask()`, `CompleteTask()`, `FailTask()`, `CancelTask()`, `PauseTask()`
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
- `TaskPersistence` — the store interface (SQLite-backed) for task lifecycle + blackboard state (including `PersistAttachments`, `SaveTrajectory`/`LoadTrajectory`, and `PersistGoalState`/`LoadGoalState` for goal-mode state)
- `BlackboardRestoreFunc` / `BlackboardFactory` — injected into the orchestrator at build time

### Goal State (goal mode)

A task running in goal mode carries a `goal.GoalState` (condition, verify clause, budget, turn/token counts, lifecycle status, last verdict). It is persisted separately from the trajectory so a paused/active goal survives app restart and resumes into the loop:

- `PersistGoalState(taskID, gs)` / `LoadGoalState(taskID)` — added to `TaskPersistence`. The goal state is persisted separately from the blackboard and restored SEPARATELY from `RestoreBlackboard`: the resume path loads it via `adapter.LoadGoalState(taskID)` and passes it to `Orchestrator.Resume`. The blackboard itself carries no goal state (`MapBlackboard` has no goal concept). `nil` for non-goal tasks.
- Persistence is **best-effort**: a missing store/task ID is a no-op, and a persistence failure is logged but never propagates — losing the checkpoint degrades only resumability, not the current run.
- `Orchestrator.Resume` checks `goalState != nil && !goalState.Status.IsTerminal()` and re-enters the goal loop (`resumeGoalLoop`) with the prior trajectory seeded; terminal goals fall through to the normal resume path.

See [../goal-mode.md](../goal-mode.md) for the full goal-mode lifecycle.

## c0wrk Usage of Blackboard State

c0wrk exposes Blackboard state to the agent through built-in tools — sp4rk builtins (`github.com/v0lka/sp4rk/tools/builtins`) registered by `core/tools/builtin_registration.go` (`RegisterBuiltinTools`), each backed by a sp4rk store adapter:

| Tool | Backing adapter (sp4rk) | Purpose |
| ---- | ----------------------- | ------- |
| `store_fact` / `search_facts` | `FactStore` | Inter-step/inter-delegation fact memory |
| `read_step_output` / `list_step_outputs` | `StepOutputStore` | Read completed delegation outputs |
| `read_final_result` | `FinalResultStore` | Recover a prior task's outcome (e.g. after restart, or when too large to inject verbatim) |
| `read_attachment` | `AttachmentStore` | Read the markdown content of a user-attached file by ID |

### Fact Memory

Facts are the primary inter-step communication mechanism. A researcher delegation discovers an insight and calls `store_fact`; a dependent coder delegation calls `search_facts` to retrieve it. Facts accumulate monotonically (no deletion during a task).

### Final Result

The final result is set via `SetFinalResult(output)` after the Conductor finishes, then persisted to the `tasks.final_output` column by `CompleteTask` (called separately from `SetFinalResult`). For inline tasks (no delegations), the final result is the only blackboard state besides the plan and facts.

### Attachments

**Document** attachments are user-attached files converted to markdown and made available to the agent as read-only context. The lifecycle has two phases:

1. **Pending** (session-owned, not yet on the blackboard): staged by `Manager.AttachFiles` (see [../session-lifecycle.md](../session-lifecycle.md)) on `Session.pendingAttachments` as the user picks files. Pending attachments are metadata-only in the UI (the converted markdown never leaves the backend). Removing or sending the message clears the pending list.
2. **Committed** (blackboard-owned): on `SendMessage` the session manager snapshots `pendingAttachments`, clears them, and passes them via `HandleOptions.PendingAttachments`. `Orchestrator.setupBlackboard` flushes them into the blackboard (`bb.AddAttachment`) in both the fresh and restored paths, so they are available to the task and persisted alongside the rest of the blackboard state.

**Image** attachments (png/jpg/jpeg/gif/webp) are a separate kind: they are staged on `Session.pendingImageAttachments`, passed to the LLM as image content blocks via `HandleOptions.PendingImages`, and **do not flow through the blackboard** (the blackboard is markdown/text-only). See [../session-lifecycle.md](../session-lifecycle.md) for the image attach/restore lifecycle.

The Conductor's task message is augmented with an "Attached files" section listing attachment IDs (`Orchestrator.augmentWithAttachments`) so the model knows which files are available and can request their content via the `read_attachment` tool. Only the Conductor sees this section — the router and conversation history keep the clean message, so prior turns don't accumulate repeated attachment listings. On continuation turns the restored blackboard already carries all session attachments, so every turn sees the full current set.

Committed attachments survive app restart: `PersistentBlackboard.AddAttachment` / `RemoveAttachment` persist the full list via `TaskPersistence.PersistAttachments`, and `RestoreBlackboard` rehydrates them from `TaskState.Attachments`.

## Error Handling

- Persistence failure → logged + emitted as service warning, execution continues
- Missing step result for a dependency → empty context (not an error)
- Fact search with no matches → empty slice returned

## Invariants

- A Blackboard is created once per task (first message) and restored for continuations; it is never shared across tasks
- All Blackboard methods are safe for concurrent use (sp4rk `MapBlackboard` uses `sync.RWMutex`)
- Step results use replace semantics: writing a step ID that already has a result overwrites it (summary regenerated, trajectory replaced), and persistence upserts in lockstep (`SaveTaskStep` → `INSERT OR REPLACE INTO task_steps`). Re-execution paths — retry after failure, resume from a paused checkpoint or restart, re-run plan waves — rely on the latest execution's result winning
- Facts accumulate monotonically (no deletion during a task)
- Non-finalizing writes are serialized through a single background worker goroutine (with timeout + panic recovery); finalizing writes (`CompleteTask`/`FailTask`/`CancelTask`) run synchronously so the status change is persisted before returning
- The worker's enqueue is lossless: when the channel buffer is full, `persistSafe` waits for queue space (bounded by the persistence timeout) instead of dropping the write — a dropped fact/step write has no later re-flush, so it would stay missing from the database (and the Blackboard panel) until the next full-list rewrite
- `blackboard_updated` change notifications fire only after the write has landed in SQLite (gated on persistence success): the frontend panel refetches the database, so announcing un-persisted state would fetch state without the just-stored fact right after `store_fact` reported success
- `RestoreBlackboard` recreates the full in-memory state from SQLite
- Attachments are staged as pending on the session and flushed into the blackboard exactly once on the next `SendMessage` (the pending list is cleared after the snapshot)
- `GoalState` persistence is best-effort: a missing store/task ID is a no-op and a failure is logged but never propagates (degrades only resumability); a non-terminal restored goal re-enters the goal loop on `Resume`

## Related Specs

- [sp4rk blackboard](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/blackboard.md) — canonical `Blackboard` interface, `MapBlackboard`, store adapters, `Checkpointer`
- [README.md](README.md) — memory overview
- [../orchestration/conductor.md](../orchestration/conductor.md) — how the Conductor reads blackboard state
- [../orchestration/delegation.md](../orchestration/delegation.md) — how `delegate` writes subagent results
- [../goal-mode.md](../goal-mode.md) — goal-mode state persistence and resume
- [../../architecture/data-flow.md](../../architecture/data-flow.md) — blackboard flow diagram
