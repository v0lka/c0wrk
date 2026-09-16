# ADR-048: Unified Recovery Ledger (one durable unit record, one writer, one settlement funnel)

## Status

Accepted

## Context

Three independently reported defects all had the same shape: **a unit of work
that stopped before it completed was misread after the fact**, because the
"what stopped, and why" state was spread across three unrelated places — the
in-memory Delegation Registry, the blackboard `StepResult`, and the delegation
spec store — each with a different reader and a different notion of "finished".

1. **An interrupted delegate was never recovered.** A subagent abandoned
   mid-flight (app crash, window close, process kill) leaves the Delegation
   Registry gone (it is per-run, in memory) and the blackboard `StepResult`
   either absent or stale. On the next `Resume` the delegate arm only knew how
   to relaunch a *paused* delegation (a `StepResult` carrying `agent.ErrPaused`);
   anything else was **marked failed and never relaunched**. The work was
   silently dropped. On the frontend the replayed history carried no terminal
   event, so the delegate's chat block stayed a **misleading `running`** block
   until the user noticed.

2. **A cooperative pause inside the goal verifier was read as a rejection.**
   The independent goal verifier ([goal-mode.md](../domains/goal-mode.md#independent-verification))
   runs as an **isolated Conductor pass on a throwaway seeded blackboard**, so it
   had no `PersistableBlackboard` to hang persistence off, and its pass result
   was discarded — the gate only read the verification sink. A pause that
   tripped inside the pass declared no verdict, `sink.Last() == nil`, and the
   code synthesized `Confirmed: false` → the agent's `"met"` claim was turned
   into a **`not_met` rejection** ("goal not met"). A user who paused during
   verification was told the goal had failed verification.

3. **An errored goal turn was swallowed and mislabelled.** In `runGoalTurns` a
   turn's error was discarded (`_ = terr`) and the turn fell through to the
   anti-spin check; an errored turn that made no tool calls and declared no
   verdict looked **idle**, so the goal halted `blocked_idle` and the task
   surfaced as a degraded **"partial"** — the concrete provider/transport cause
   was lost and there was no clear resumable failure to recover from.

Each fix was individually straightforward, but patching three readers would
have left the underlying model broken: **"recovery" was three mechanisms with
three sources of truth.** The decision below introduces one durable record for
every execution unit and routes all three scenarios (and their readers) through
it.

## Decision

### One durable per-unit record — `core/units`

A new package [`core/units`](../../core/units/units.go) defines the contract. A
**unit** is one durable record of an execution unit: a plan step, a delegated
subagent, a goal-verification pass, the root task, a tool invocation.

```go
type UnitRecord struct {
    ID, TaskID, Namespace string
    Kind      UnitKind   // task | plan_step | subagent | goal_verification | tool
    ParentID  string
    Depth     int
    Status    UnitStatus // pending | running | paused | completed | failed | interrupted
    Spec      json.RawMessage // opaque REBUILD spec — enough to relaunch without an LLM decision
    Steps     json.RawMessage // opaque resume checkpoint (a JSON []agent.Step)
    CreatedAt, UpdatedAt time.Time
}

type Ledger interface { // one write/read API
    Begin(rec UnitRecord) error
    Checkpoint(id string, steps []agent.Step) error
    Settle(id string, status UnitStatus) error
    List() ([]UnitRecord, error)
}
type Store interface { // one persistence seam
    SaveUnit(rec UnitRecord) error
    LoadUnits(taskID string) ([]UnitRecord, error)
}
```

`Spec` and `Steps` are **opaque JSON owned by the caller**, so a new unit kind
rides the same record with no schema change. `UnitStatus.Terminal()` is the
single definition of "finished" (`completed`/`failed`/`interrupted`); `paused`,
`pending` and `running` are still in flight. `interrupted` is deliberately
distinct from `paused`: it is a unit left unfinished by a crash/app exit and
carries **no explicit resume intent**, whereas `paused` is a cooperative
checkpoint the resume path owns.

Two constructors, one shared task scope:

- `NewBlackboardLedger(bb PersistableBlackboard) units.Ledger` — the **mainline**
  constructor; derives the task from `bb.TaskID()` and the store from the
  blackboard's optional `UnitStore()` capability, returning the blackboard's
  cached ledger when present and never failing (it falls back to in-memory).
- `units.NewLedger(store, taskID, namespace)` — the **isolated** constructor, for
  a context that has a task store but no persistent blackboard (the goal
  verifier). The `namespace` keeps its unit ids from colliding with the
  mainline's.

Storage is one **additive** table, `task_units`, in the existing session SQLite
database ([persistence.go](../../backend/session/persistence.go), keyed by task +
namespace + unit id, `CREATE TABLE IF NOT EXISTS` + index, applied on the next
store open with no change to existing tables and FK-cascaded on session delete).
It is exposed through the existing store as `TaskStoreAdapter.UnitStore()`
([task_adapter.go](../../backend/session/task_adapter.go)) — **nil** when the
store has no unit persistence (the graceful-degradation signal) — and cached on
the persistent blackboard as `UnitStore()` / `UnitLedger()`. Reusing
`task_delegations`/`task_steps` in place was rejected because both are consumed
by fixed-type readers on the resume and restore paths (see Alternatives).

### One writer — the launcher owns every lifecycle transition

The `conductorLauncher` ([conductor.go](../../core/conductor.go)) is the
**single writer** of unit lifecycle. `wireDelegationSpecSink` is ledger-aware
and, at registration, calls `ledger.Begin(...)`; the launcher then centralizes
every transition:

- `markUnitRunning` → `Settle(running)` at start;
- `persistUnitOutcome` → the blackboard `StepResult` mirror **plus**
  `recordUnitLedger` → `Checkpoint(steps)` (when non-empty) + `Settle(paused |
  failed | completed)`;
- `settleUnit` → a ledger-only terminal settle for outcomes with no step result
  (build failure, cancellation, unsatisfiable dependency).

Every call site for the three delivery kinds is wired: plan steps
(`Execute` register/running/paused/settled/skipped/never-started/ctx-cancel;
`defaultPlanStepWave` build-failure/running/paused), blocking delegates
(`runRegularBlocking`), redelegation (`runRedelegBlocking`), and async delegates
(`launchAsync`). The blackboard `StepResult` is retained as the in-memory view
(read by `read_step_output` and the plan continuation), so existing behavior is
preserved; the ledger is the durable **additional** record.

The goal verifier's isolated pass writes through a ledger-bound sink
([unit_sink.go](../../core/unit_sink.go)) scoped to the goal task and the
**`goal_verification` namespace**, parent-linked under a `goal_verification`
**container** unit (`newVerifierUnitSink`). `RunConductor` prefers a wired
`deps.unitSink` over the blackboard-derived wiring, so the isolated pass persists
even though its LLM view is a throwaway `MapBlackboard`. A `known` id-set guard
prevents an unregistered id (a plan step) from synthesizing a phantom unit.

### One settlement funnel on Resume — enumerate the ledger, settle uniformly

`Orchestrator.Resume` ([orchestrator.go](../../core/orchestrator.go)) no longer
scans delegation specs with special-case branches. `resumePausedWork` →
`resumeUnits` enumerates the task's **durable ledger** (`resumeUnitsFromLedger`)
**merged** with the legacy delegation specs (`legacyResumeUnits`) for tasks
persisted before the ledger existed (ledger units win by id, so nothing settles
twice), groups the normalized `resumeUnit`s by **(scope = namespace, depth)**
deepest-first, and classifies **uniformly**:

- **paused** → relaunch **seeded** from the checkpoint (the checkpoint is lifted
  onto the blackboard first so `buildSubAgentTask` seeds the subagent);
- **not-started / running / interrupted** → relaunch **fresh** — the former
  *"interrupted ⇒ mark failed, never relaunch"* branch is **removed**;
- **terminal** (completed/failed) → **replay** through `Register`+`Complete`
  (never re-run) so dependency resolution still sees them.

Container kinds (`task`, `goal_verification`), plan-step units (owned by the
plan arm) and tool units are skipped. Because a verifier delegate is recorded
with `ParentID = goal_verification` in the `goal_verification` namespace, the
funnel keys its registry group by `(scope, depth)`, so a verifier unit settles in
its own scope and never shares a registry — or collides by id — with a mainline
delegation. A relaunch that pauses again re-checkpoints the wave (unchanged
behavior).

### One read path on the frontend — the ledger is the snapshot

`GetSessionRuntimeStatus` now carries a `work_units` snapshot
([manager_execution.go](../../backend/session/manager_execution.go)):
`workUnitSnapshot` reads the session's durable ledger (via
`NewTaskStoreAdapter(ts).UnitStore()`). **Explicit settle on read:** a unit left
in flight (`pending`/`running`) on a task that is **not** executing was abandoned
by a crash/app exit, so it is settled `interrupted` (a durable ledger write plus
a transient `work_unit_settled` event); a **paused** unit is a resumable
checkpoint the resume funnel owns and is **left untouched**; a live task never
settles. The call is idempotent — once terminal, later polls neither write nor
emit.

The frontend reconciles the same ledger back onto the replayed chat blocks
([sessionRuntime.ts](../../frontend/src/lib/sessionRuntime.ts)): `reconcileWorkUnits`
stores a per-session `workUnitStatus` overlay in `chatStore`, and
`groupMessages(messages, workUnitStatus?)` applies it **last** — a
message-derived `completed`/`failed` is never downgraded. The live
`work_unit_settled` event ([useWorkUnitEvents.ts](../../frontend/src/hooks/events/useWorkUnitEvents.ts))
covers a settle observed while the session is open, and a fresh
`subagent_launch`/`plan_step_start` for the same `step_id` **clears** the overlay
entry (proof the unit is running again).

### The three scenarios, resolved through the one contract

| Reported scenario | Before | After (the contract) |
| --- | --- | --- |
| **Interrupted delegate** | Registry gone; `Resume` marked it failed and never relaunched; block stayed `running` | Ledger holds it `interrupted`; `Resume` relaunches it **fresh**; the snapshot reconciles the block to `interrupted` (never a stale `running`) |
| **Verifier pause** | `(nil)` sink → synthesized `Confirmed:false` → `not_met` | `defaultGoalVerifier` detects the pause (`pausedRun`) and returns the pause sentinel `(nil, agent.ErrPaused)`; `runGoalTurns` breaks **before** the confirm/reject branches, leaving the goal **active**; `goalLoopResult` → `ExecutionStatusPaused` → `session_paused` |
| **Goal turn error** | `_ = terr` discarded; idle-shaped turn → `blocked_idle` / "partial" | The error branch runs **first** in `runGoalTurns`; the turn is retried `goalTurnMaxErrorRetries` (= 2) times, then the loop halts leaving the goal **active**, records the cause on `GoalState.LastError`, and `goalLoopResult` maps it to a **resumable `ExecutionStatusFailed`** carrying the reason as the output |

The goal turn-error and verifier-pause contracts are expressed on top of the
same "the goal stays `active` so `Resume` re-enters" invariant; the interrupted
delegate is the ledger's own `interrupted` status flowing through both the
settlement funnel and the read snapshot.

## Consequences

**Positive:**

- One source of truth. "What stopped, why, and can it continue" is answered by a
  single durable record; every reader (the resume funnel, the plan arm, the
  frontend snapshot) reads the same ledger, so the three readers can no longer
  disagree.
- An interrupted delegate is now recovered rather than dropped, and the UI can no
  longer show a permanent phantom `running` block after a restart.
- The goal verifier's units are durable without giving it a persistent
  blackboard: the isolated pass writes through a namespaced, parent-linked sink,
  so verifier work survives a restart and a cooperative pause is no longer
  misread as a rejection.
- An errored goal turn is a first-class **resumable failure** with its cause
  preserved, instead of a silent `blocked_idle`/`partial`.
- Additive storage only: one `task_units` table (idempotent, FK-cascaded, applied
  on the next store open). No existing table, interface or mock was extended —
  the unit capability is asserted optionally, so a store without it degrades to
  an in-memory ledger.

**Negative:**

- A new durable write per unit transition (register, running, checkpoint, settle)
  on top of the blackboard mirror the launcher already performed, and a second
  table to keep in mind for migrations. The ledger's in-memory overlay keeps a
  store-less run cheap, but a persisted run does more I/O than before.
- The `groupMessages` overlay and the session-load `work_units` fetch are extra
  frontend state; the reconcile must stay idempotent and must never downgrade a
  message-derived terminal status (guarded by tests).
- `GetSessionRuntimeStatus` now performs a **write** (the explicit
  `interrupted` settle) on what had been a read-only call. It is idempotent and
  confined to units on a task that is not executing, but the call is no longer
  side-effect-free.

## Alternatives Considered

- **Reuse `task_delegations` / `task_steps` in place instead of a new table.**
  Rejected. `task_delegations.spec` is unmarshaled into `tools.DelegationSpec`
  by the resume wave's spec load, and `task_steps` rows are folded into the
  blackboard's `StepResults` keyed by `step_id` on restore; writing non-delegation
  unit records there would corrupt delegate restore or create phantom plan-step
  results. A single additive `task_units` table (same DB, pool and task lifecycle)
  is the minimal gap-filling storage.
- **Keep three readers and fix each separately.** Rejected. It is exactly the
  state that produced the three defects: the Registry (in-memory, per-run), the
  blackboard `StepResult` (value view) and the spec store (rebuild view) each
  answered "is it done?" differently, and the verifier had none of them. A single
  durable record is what makes the three fixes coherent instead of three special
  cases.
- **Settle an in-flight unit `failed` on read (instead of `interrupted`).**
  Rejected. A crash is not a failure of the unit; marking it `failed` would
  discard a perfectly recoverable unit and make `Resume` replay it as terminal.
  `interrupted` is non-terminal-but-not-paused, so the funnel relaunches it fresh
  while the UI can still show that it was abandoned.
- **Auto-apply the snapshot by making the frontend derive status from the ledger
  alone.** Rejected. Replayed messages already carry a message-derived terminal
  status (`completed`/`failed`) that is authoritative for units whose terminal
  event *did* persist; the overlay must only fill in what the history lacks, so
  it is applied last and never downgrades a terminal status.
- **Give the goal verifier a persistent blackboard.** Rejected. The verifier's
  isolation (a fresh seeded blackboard) is a security/correctness property — it
  must not see the live task's incomplete state. A namespaced, parent-linked
  ledger sink gives it durability without giving it the live blackboard.

## Related Specs

- [goal-mode.md](../domains/goal-mode.md) — the independent-verification pause
  exception and the turn-error resumable-failure contract.
- [orchestration/delegation.md](../domains/orchestration/delegation.md) — the
  durable unit ledger, the single-writer launcher, and the Resume relaunch
  funnel.
- [session-lifecycle.md](../domains/session-lifecycle.md) — the goal resume
  contract and the `work_units` runtime snapshot.
- [event-catalog.md](../contracts/event-catalog.md) — the `work_unit_settled`
  event and the `work_units` field.
- [desktop-frontend.md](../contracts/desktop-frontend.md) —
  `GetSessionRuntimeStatus.work_units`.
- [ADR-012](012-conductor-orchestration-pipeline.md) — the Conductor
  plan/execute pipeline the ledger now makes durable.
- [ADR-019](019-goal-mode.md) — goal mode, whose verifier and turn loop this
  decision makes recoverable.
- [ADR-026](026-smart-approve-unified-funnel.md) — the precedent for collapsing
  scattered branches into one funnel.
