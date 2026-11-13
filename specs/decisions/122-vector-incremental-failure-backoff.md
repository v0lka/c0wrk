# ADR-122: Incremental-Pass Failure Backoff and Circuit Breaker

## Status

Accepted

## Context

The debounce-armed incremental pass is the vector index's background heartbeat: every file-change burst (or every isolated shell call — the trigger set covers `write_file`, `edit_file`, `bash_exec`, `posh_exec`) schedules one `IndexIncremental` after a 1 s debounce window. When the embedder or the runtime around it breaks (ONNX session failure, a wedged device, an exhausted environment), that heartbeat became a hot retry loop: the next file change re-armed the pass at full speed, the pass failed again, and the loop burned CPU on inference attempts that could not succeed while the session log filled with identical warnings. The only honest signal available was whatever the last pass happened to leave in the status — with a live collection the index could even keep reporting `ready` while every incremental update silently failed.

[ADR-120](120-vector-store-repair-ladder.md) bounded the STORE repair ladder (transient = no repair; proven defect = one quarantine + retry; terminal = explicit user rebuild), but it deliberately governs attach/rebuild failures, not the steady-state incremental loop. A repeating incremental failure is a different animal: it is almost always the embedder/runtime, it is triggered automatically by file changes rather than by an attach, and neither a rebuild nor a quarantine is its remedy — the failure class must be visible on its own terms, without dragging the repair ladder into a loop ADR-120 explicitly rejected ("retry-with-backoff on every attach failure" was rejected there because a proven store defect is deterministic; an embedder fault in the incremental loop is exactly the case where backing off IS the honest behavior).

## Decision

### D1 — A failed pass backs the next debounce arm off exponentially

`runIncrementalGuarded` records the outcome of every debounce-armed pass. After the first failure, subsequent arms carry an exponential backoff instead of the configured debounce window: 5 s doubled per consecutive failure (5 s → 10 s → 20 s → 40 s → 80 s → …), capped at 10 minutes. The backoff is computed at arm time from the consecutive-failure streak (constants `incrementalFailureBackoffStart` / `incrementalFailureBackoffMax`); the `afterFunc` seam and the debounce arming path are otherwise untouched. The implementation is timer-only, so every timing behavior is testable on virtual time (`testing/synctest` through the existing `afterFunc` hook) with no sleeps.

### D2 — Five consecutive failures suspend file-change arming (the breaker)

At 5 consecutive failed passes (constant `incrementalFailureBreakerThreshold`) the circuit breaker trips: `NotifyFileChange` stops arming altogether, a timer armed before the trip runs no pass if it fires late, and the status reports the suspension honestly — state `unavailable`, a reason naming the failure count and the last cause, and the kind `suspended`. Cancelled passes count as neither success nor failure (a cancellation is a project switch or shutdown superseding the pass — the same "a cancelled operation is never a verdict" rule the repair ladder's superseded-pass silence follows).

`suspended` is a NEW, separate `error_kind`. It is deliberately NOT routed through `notifyInitFailure`: the breaker's verdict is about the embedder/runtime pipeline, not the store. It must not invoke the repair ladder's failure callback (the ADR-120 surfaces and their repair semantics stay untouched), and nothing is quarantined — `chunks/`, the embedding cache, any corpus or overlay publication are all left byte-identical. The kind rides the existing wire pair (`last_error`/`error_kind`) so every status surface renders it with no new plumbing. The trip itself is also pushed onto the focused root's `vector_index:status` stream through the project callbacks, with the repair path's superseded-pass silence as the currency rule (a pass whose index context was replaced by a newer lifecycle stays silent): the failing pass's own terminal contract has just reported `ready` — a live collection keeps search answering — so without the push that misleading event would remain the UI's freshest news until the next pull. The verdict also OUTLIVES later terminal `ready` reports: a pass that fails with a live collection reports `ready` (the indexer's ready-vs-unavailable contract), so the manager's progress wrapper leaves the suspend pair in place while the breaker is tripped — the reachable producer is a failing branch-switch pass (debounce arms are refused while suspended, and a failed Reindex repair re-records its own verdict right after) — otherwise the suspension would repaint itself as a bare, unexplained `ready` while file-change indexing silently stayed refused. Only the breaker's own reset paths (D3) drop the pair.

### D3 — Four reset paths

1. **A successful pass resets the streak** (success of ANY pass: a debounce-armed pass, or a successful branch-switch pass).
2. **A manual Reindex resets the breaker** — the explicit escape hatch; the repair pass it launches owns the status from its first progress report.
3. **An indexing-lifecycle restart on the manager resets the breaker** — a lifecycle must not inherit a suspension it never earned. `SwitchProject` carries this reset on both of its arms (a CODE switch and the No-Project one). In the per-root registry ([ADR-081](081-vector-per-root-registry.md)) a manager's only production `SwitchProject` is its creation in `ManagerForRoot`, so the practical resets for a suspended root are the paths above plus manager REBUILDS: the `vector_index.enabled` toggle (item 4), the LRU eviction beyond the four-root cap, and an app restart. A Git-panel FOCUS switch is deliberately NOT a reset: `ApplyFocus` keeps every live manager running (background sessions keep searching their own trees), so switching away and back returns the same manager with its suspension intact — re-surfaced honestly by the focus snapshot — until one of the resets above runs.
4. **The `vector_index.enabled` toggle resets structurally** — `UpdateIndexingSettings`' disable tears the per-root manager down and an enable builds a fresh one, so the breaker never survives a master-switch cycle.

The reset clears the streak, re-opens file-change arming, and drops the `suspended` status pair (the pass that follows owns the status from its own reports) — the reset paths are the ONLY thing that drops the pair: a later failing pass's terminal `ready` report leaves it standing (D2).

### D4 — Scope: debounce-armed passes only

The breaker counts debounce-armed incremental passes. Attach/rebuild failures stay ADR-120's territory; branch-switch pass FAILURES stay the branch switch's own logged outcome (they do not feed the streak), while a branch-switch SUCCESS resets the breaker like any successful pass. The reindex repair pass is not a breaker input. Constants, not config: the backoff and threshold are safety properties, not tuning knobs — no new `vector_index.*` keys.

## Consequences

- A broken embedder no longer produces a hot retry loop: retries space out to seconds, then tens of seconds, and stop entirely after five consecutive failures — with the stop visible as an honest `unavailable`/`suspended` status instead of a silently stuck `ready`.
- The status surface gains one kind: `suspended` names the actual failure class (embedder/runtime) and the remedy named in the reason (a successful pass, Reindex, or the master switch). The search panel's repair kind gate SUPPORTS `suspended` — the manager-level Reindex behind the action is the breaker's own reset path (it re-opens file-change arming and relaunches the pass, which fails honestly again if the embedder is still dead), so disabling the action with "a rebuild cannot fix this failure" would contradict the status cause naming Reindex as the remedy, and the gated-out RPC is the panel's own `reindexVectorIndex` — no out-of-band Reindex caller exists to preserve. The master switch remains the second hatch.
- Costs and limits: a genuinely flaky embedder takes up to 5 s longer to notice a recovery (the first post-failure arm is backed off until a pass succeeds); a suspended index stops applying file changes until a reset path runs — by design, because every automatic retry was failing and the failure is surfaced.
- The breaker state is per-manager (per workspace root), in-memory only, and reset by manager teardown — app restart and config-driven rebuilds both start clean.

## Alternatives Considered

- **Retry at full speed forever** (status quo): rejected — a deterministic embedder failure turned every file change into a failed inference attempt, burning CPU and log volume with no honest terminal state.
- **Backoff without a breaker**: rejected — unbounded retries at 10-minute intervals keep a permanently broken pipeline "running" and the status would keep flip-flopping between progress and failure without ever explaining the situation.
- **Route the suspend through `notifyInitFailure` (the repair ladder)**: rejected — it would invoke the OnFailure callback and present the suspension as a store/attach verdict, dragging ADR-120's repair semantics into a failure class they do not address and tempting a quarantine that would destroy shared state for an embedder fault.
- **Configurable backoff/threshold (`vector_index.failure_backoff_*`)**: rejected for v1 — a safety property whose mis-tuning reproduces the hot loop it exists to prevent; constants keep the behavior uniform and reviewable.
- **Fail the search path outright on suspend**: rejected — the existing collection may still answer queries; the suspend is about the UPDATE pipeline, and search already fails naturally on a broken embedder (query embedding fails too).

## Related

- [ADR-120](120-vector-store-repair-ladder.md) — the store repair ladder this breaker is deliberately separated from; the honest `last_error`/`error_kind` pair the suspend status rides
- [ADR-104](104-content-addressed-vector-store.md) — the store machinery the breaker never touches (no quarantine, publications stay byte-identical)
- [ADR-103](103-vector-index-master-switch.md) — the master-switch toggle whose teardown/rebuild is the structural reset
- [ADR-064](064-branch-scoped-lazy-vector-index-open.md) — the branch-scoped open whose cancellation rule ("a cancelled operation is never a verdict") the streak counting borrows
- [../domains/workspace.md](../domains/workspace.md) — the vector-index behavior section this decision extends
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — the `error_kind` dictionary with the `suspended` value
