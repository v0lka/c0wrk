# Compaction Strategies

## Role

c0wrk does not implement compaction strategies — they are **sp4rk engine** primitives (sliding window, summarization, hierarchical, tool-output pruning, regular history mutation). This spec documents only how c0wrk selects and configures them. The canonical strategy behavior is in [the sp4rk compaction spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md).

## Key Files

- `core/builder.go` — `buildContextFactory` constructs the `ContextManagerFactory`; the strategy is selected by the caller (`compactionStrategyForDomain`, see below) and passed in as an argument
- `core/orchestrator_compaction.go` — `Orchestrator.CompactConversationHistory`: manual, on-demand compaction of the session conversation history (see Manual Context Compaction below)
- `backend/session/manager_compaction.go` — `Manager.CompactSessionContext` / `CancelSessionCompaction`: the pause-wait → dialogue compaction → no-op defer → persist-marker → auto-resume flow
- `core/orchestrator.go` — `RequestResumeCompaction` / `ClearResumeCompaction` / `consumeResumeCompaction`: the mutex-guarded one-shot "compact on resume" request, consumed by the next `Resume` (see Resume-side Forced Trajectory Compaction below); `ClearResumeCompaction` (invoked by the session layer's cancel/abandon paths) drops an armed flag so it cannot fire for an unrelated later task
- `core/conductor.go` — `compactionStrategyForDomain(domain, complexity)` selects the strategy from `routing.Domain` + complexity (see [README.md](README.md) for the domain → strategy table); the context factory also applies optional per-step `PruningOverride` values from `StepConfig` (passed as variadic args); `forceCompactionStrategy` (the resume-compaction request) overrides the routing-derived strategy for a run and forces `CompactOnStart`

Engine files (`github.com/v0lka/sp4rk/memory/compaction_sliding.go`, `compaction_summary.go`, `compaction_hierarchy.go`, `compaction_conversation.go`, `context.go`) are documented in [the sp4rk compaction spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md).

## Strategy Overview (canonical in sp4rk)

| Strategy | Best for | Trade-off |
| -------- | -------- | --------- |
| sliding_window | code tasks (recent edits visible) | loses early research/context entirely |
| summarization | research tasks (preserves synthesized findings) | summary may lose details; requires LLM call |
| hierarchical | long-running complex tasks (balanced retention) | more complex logic, harder to predict retained content |

Tool-output pruning (governed by `toolOutputPruning.thresholdPercent`) and regular history mutation (per-`BuildPrompt`, replaces old tool results with `ToolResultCache` references) are engine mechanisms — see the sp4rk compaction spec.

## Manual Context Compaction

On-demand compaction of the session's cross-task conversation history (what the orchestrator injects as prior conversation into every request — NOT the per-executor step context, NOT the UI chat history). Triggered from the status bar by the compact button left of the context-fill indicator (strategy dropdown with tooltips; the button becomes a cancel affordance while a compaction is in flight).

- **sp4rk primitive**: `memory.CompactConversationHistory(ctx, msgs, budget, strategy, cfg, deps)` — message-level strategies (`sliding_window` / `summarization` / `hierarchical`) mirroring the step strategies; unknown strategies fail closed; the last message is never removed; the input is never mutated (canonical in the [sp4rk compaction spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md)).
- **Orchestrator** (`core/orchestrator_compaction.go`): `CompactConversationHistory(ctx, strategy)` compacts `o.conversationHistory` in place using the session's executor compaction settings (Small-LLM context overrides applied — same values `buildContextFactory` uses), summarizes via the session's tracking caller (`coreprompts.CompactionSummarize`, `llm.CallPurposeCompaction` — tokens counted in session totals), and emits `ContextCompaction` + a refreshed `ContextFill` on the EFFECTIVE basis (window − output limit − safety margin) — the session emitter rescales those to the display window itself. The RETURNED before/after percents (persisted into the marker row and the `compaction_finished` event) use the DISPLAY basis (the advertised window) directly, because the frontend renders the marker's metadata verbatim on reload and the reloaded card must match the live one. On error the history is left untouched.
- **Session flow** (`backend/session/manager_compaction.go`): `Manager.CompactSessionContext` — async: emit `compaction_started`, set `session.compacting` (sends/resumes now fail with `ErrSessionCompacting`), pause a running task exactly like `PauseSession` and wait for its checkpoint, compact the dialogue (`orchestrator.CompactConversationHistory`), then auto-resume the task it paused and emit `compaction_finished`. Outcomes: **compacted** — a marker row (role `context_compaction`; metadata carries `strategy`, before/after percents, and the compacted `messages` snapshot) is persisted so a restart restores the compacted history; **no-op** (`ErrNothingCompacted` — the dialogue already fits within the strategy's limits) — NOT a failure: the event reports `success=true, nothing_compacted=true` with zero percents, NO marker row is written (there is no compacted history to snapshot), and the history is untouched. When a paused unfinished task waits for this flow's auto-resume, the no-op additionally DEFERS the compaction to the resume: `orch.RequestResumeCompaction(strategy)` arms the one-shot request (surfaced as `deferred_to_resume=true`) strictly BEFORE the auto-resume, so the flag is consumed by the resumed run; if the task is instead cancelled or abandoned before resuming (user discard via `CancelUnfinishedTask`, goal-mode takeover via `abandonUnfinishedTaskForGoal`, archival), the session layer's `clearResumeCompaction` drops the armed flag so it never fires for an unrelated later task — the resumed run then force-compacts the merged trajectory (see below) and the real numbers arrive as the executor's `context_compaction` card. Note the deferral on a USER-paused session (the flow pauses only the task it finds running, so an already-paused session gets no auto-resume): the armed flag simply waits for the user's own later resume of that same task. A failed auto-resume sets `paused_without_resume` — the checkpoint remains but `session_paused` was suppressed, so clients re-apply the paused state. `CancelSessionCompaction` aborts: during the pause-wait it still waits for the checkpoint (skips the compaction — deterministically, even when the cancel races the checkpoint landing — and auto-resumes); during the compaction it aborts the summarize calls with the history untouched.
- **Resume-side forced trajectory compaction** (`core/orchestrator.go` + `core/conductor.go`): `RequestResumeCompaction(strategy)` arms a mutex-guarded one-shot request; the NEXT `Resume` consumes it at entry (before the goal-loop / plain-Conductor branch is chosen, so the semantics hold for both resume paths) and threads it into the conductor run as `forceCompactionStrategy`. When non-empty, the run (a) replaces the routing-derived compaction strategy with the user-selected one for the ENTIRE run — the forced start-of-run pass AND any later threshold-triggered reactive compactions — and (b) sets `CompactOnStart`, so the engine compacts the merged trajectory (seeded checkpoint steps + the resumed run) exactly once, up front, BEFORE the first LLM call, regardless of fill thresholds. Empty (the default) preserves the normal routing-derived, threshold-driven behavior. Arming with `""` is the "not requested" sentinel (a no-op); re-arming before consumption overwrites the pending strategy. The defer happens ONLY for the no-op outcome: a successful dialogue compaction already shrank the history and persisted the marker, so force-compacting the merged trajectory at resume would duplicate the work (`CompactOnStart` ignores fill thresholds).
- **Restore**: `convertChatMessagesToLLM` treats the LAST marker's snapshot as the seed — conversational rows before it are dropped, the snapshot is expanded in place, later exchanges append. Markers without a snapshot are no-ops; a no-op manual compaction writes no marker at all. The marker row renders as the existing compaction card on reload.
- **Frontend**: `chatStore.compacting[sessionId]` locks the whole input area (`chatInputLock` matrix), the activity shows "Compacting", the compact button swaps to cancel, and `session_paused` handling is suppressed while compacting. `SessionRuntimeStatus.compacting` reconciles the state on session switch/restart. `compaction_finished` outcome handling (`handleCompactionFinished`): an idle no-op (`nothing_compacted` with nothing to resume — no card follows) clears the "Compacting" activity immediately; `deferred_to_resume` WITH the flow's auto-resume behaves like `resumed` — the label is left in place for the subsequent `task_resumed` ("Resuming...") — while a deferral on a USER-paused session (no auto-resume, no upcoming `task_resumed`; the session's own paused state was already applied by its unsuppressed `session_paused`) clears the label like an idle outcome instead of leaving it dangling.

## Tool-Output Pruning Config (c0wrk consumption)

c0wrk configures tool-output pruning centrally in `core/builder.go` (`buildContextFactory`) from `cfg.Executor.ToolOutputPruning`:

- `keepLastN` — how many recent tool results to keep inline before replacing older ones with a placeholder
- `protectedTools` — tool names whose outputs are never pruned regardless of `keepLastN` (default `["store_fact","search_facts"]`)
- `thresholdPercent` — fill % **below which pruning is skipped entirely** (default 50; 0 = always prune). Pruning runs on every `BuildPrompt` once fill reaches this floor — it is independent of the `CheckFill` compaction statuses

Per-step pruning overrides are supported via the `PruningOverride` variadic argument on `ContextManagerFactory`; when a step's `StepConfig` carries a positive `KeepLastN`, it replaces the global value for that step's executor.

## Trigger Thresholds (c0wrk config)

The three compaction thresholds all run the same `cw.Compact` (strategy compaction); only the `CheckFill` status differs:

```
Context fill %:
  0%──────────85%────────92%──────98%────100%
              │           │        │
              ▼           ▼        ▼
            "compact"   "warning" "emergency"  → each runs cw.Compact (strategy)
                                                      │
              >= 100% ─────────────────────────────► "reject"

Tool-output pruning is governed separately by toolOutputPruning.thresholdPercent
(default 50): it runs on every BuildPrompt once fill >= that floor, independent
of the CheckFill statuses above.
```

See [README.md](README.md) Configuration for the `config.yaml` keys.

## Invariants

- Compaction never removes the system prompt or the last message (current LLM turn)
- Tool output pruning runs BEFORE strategy compaction
- Protected tools are NEVER pruned regardless of `KeepLastN`
- Unrecognized domain → `sliding_window` fallback

## Related Specs

- [sp4rk compaction](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/compaction.md) — canonical strategy implementations, pruning, history mutation
- [README.md](README.md) — c0wrk memory overview and config
- [../orchestration/executor.md](../orchestration/executor.md) — context fill during execution
- [../orchestration/router.md](../orchestration/router.md) — domain → strategy mapping
