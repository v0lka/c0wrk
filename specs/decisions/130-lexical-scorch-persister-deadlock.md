# ADR-130: Lexical scorch writer opens never pause for merger catch-up

## Status

Accepted

## Context

The BM25 lexical leg of the vector index is a bleve v2.6.0 scorch index. `lexical.Open` (writer mode) opened it with library defaults — `bleve.Open`/`bleve.New` carry no scorch configuration — leaving scorch's default persister pacing active: `DefaultPersisterNapUnderNumFiles = 1000` pauses the persister loop whenever the number of `.zap` segment files on disk is at or above the threshold and the merger has not yet caught up with the persisted epoch. That pause waits for merger notifications and terminates only on index close.

Two properties of scorch v2.6.0 turn that pacing into a permanent write hang:

- Segment merges run synchronously inside `mergerLoop` (`planMergeAtSnapshot` → `MergeUsing`), so a merger frame absent from a goroutine dump proves the merger goroutine is gone. A panic inside the loop kills it for good — the deferred recover fires the async error and the goroutine exits — while the index stays open. Without a registered async-error callback, the panic survives only as a stats counter.
- Safe batches (`prepareSegment`) block on the `introduction.persisted` channel, which only the persister answers. With the merger dead, merger notifications never arrive, the persister waits forever in `pausePersisterForMergerCatchUp`, and every lexical `Upsert` blocks forever with no error, no cancellation path (`Batch` takes no context) and no recovery except deleting the index and rebuilding (the ADR-120 ladder).

Observed in the field (2026-11 user incident report, not reproducible in-repo): a 24k-file workspace wedged for hours with 999 `.zap` files — one below the threshold's first persist — the persister parked in `pausePersisterForMergerCatchUp`, no merger goroutine in the dump, every indexing pass stuck at its commit stage. The pass-stall watchdog dutifully dumped evidence and, by design, interrupted nothing (Pass Liveness in [workspace.md](../domains/workspace.md)).

## Decision

`lexical.Open` (writer mode) opens every index with explicit scorch runtime options instead of library defaults:

- `scorchPersisterOptions` sets `PersisterNapUnderNumFiles` to `math.MaxInt` and keeps `PersisterNapTimeMSec` at 0 — the merger-catch-up pause can never trigger, so the persister persists every snapshot unconditionally and a batch can never block on a missing merger notification. The merge planner still consolidates segments in the background; a lagging or dead merger costs disk growth and search speed, never availability.
- `asyncErrorCallbackName` routes scorch background errors (persister/merger/introducer loop panics, persist and cleanup failures) through a package-registered callback that logs them — `Error` for background-loop panics (the goroutine is gone), `Warn` otherwise. Previously they vanished into stats counters, which is what made the incident undiagnosable from logs alone.

The options ride every writer OPEN, never the persisted index meta: meta values are re-read through a JSON→`map[string]interface{}` round-trip that turns integers into float64 and cannot carry `math.MaxInt` losslessly (a read-only open that inherits the mangled meta value fails its options parse outright). Keeping the create call option-less preserves byte-level meta compatibility with every existing on-disk index and with foreign plain opens.

`OpenReadOnly` is unchanged: scorch starts no persister or merger loops in read-only mode, so the deadlock mechanism does not exist there.

## Consequences

- The reported hang class is structurally impossible: the only unbounded wait between a lexical commit and its completion is gone; every other scorch wait is fed by live loops or surfaces as an error.
- A dead or persistently failing merger degrades into unbounded `.zap` accumulation (disk growth, slower queries) instead of hanging writes — and is now visible in the logs through the async-error callback.
- Scorch's file-count pacing optimization is disabled; large builds produce more, smaller segments than the default pacing would until merges catch up.
- Scorch background errors become log noise — bounded to real background failures, which previously were silent.

## Alternatives Considered

- **A bounded threshold (e.g. 100k files).** Keeps some pacing but preserves the deadlock mechanism — a long-lived process with a dead merger reaches any finite threshold eventually, which is how the default 1000 was reached.
- **Capping batch size or commit frequency.** Acts downstream of the pause: it slows segment accumulation but cannot prevent the persister pause once the threshold is reached.
- **Watchdog escalation on the stalled commit stage.** A wedged batch cannot be recovered from outside: `prepareSegment` waits on channels with no context path, a stuck batch cannot be cancelled or rejoined, and an automated mid-pass index quarantine is a much larger intervention than removing the wait. The pass-stall watchdog stays evidence-only, and the ADR-120 repair ladder remains the recovery path for an already-wedged store.
- **Upgrading bleve.** A pinned-dependency bump is a separate reviewed change; this decision documents and neutralizes the mechanism the pinned v2.6.0 actually runs, regardless of what a later release changes.

## Related

- [ADR-120](120-vector-store-repair-ladder.md) — the repair ladder that recovers an already-wedged store (quarantine + cache-spared rebuild).
- [ADR-104](104-content-addressed-vector-store.md) — the store architecture whose lexical leg this configures.
- [workspace.md](../domains/workspace.md) — Vector Index (Pass Liveness: the stall watchdog that surfaces, never interrupts).
- `core/vectorindex/lexical/index.go` — `scorchOptions`, the registered async-error sink.
