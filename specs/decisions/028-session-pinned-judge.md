# ADR-028: Session-Pinned Tool Judge and Locked Per-Message Selectors

## Status

Accepted (amended 2026-09-28: the judge follows the session's ACTIVE model by
construction — the per-session model-NAME pin `security.judge.model` is
removed and no re-bind path exists; Decision items 1–2 rewritten accordingly)

## Context

The strict Smart Approve judge (see [ADR-026](026-smart-approve-unified-funnel.md))
was provisioned as a builder-level singleton bound to the builder's GLOBAL
active provider/model (`core/builder.go` `rebuildJudgeInternal`). Every
per-session registry clone captured that singleton's pointer at orchestrator
build time, and `RebuildJudge` (triggered by any default-model change — the
settings UI, or another session's chat model picker persisting the new default
via `UpdateLLMConfig`) re-bound the shared-registry judge to whatever model was
picked last, anywhere.

Because each session already runs on its own fresh LLM router, a session's own
LLM calls were unaffected by global switches — but its judge evaluations were
not. A session whose orchestrator was built (or lazily restored) after a
default-model switch could inherit a judge pointed at a provider it never used,
including one that is unreachable (a local server that is down) or slow. Each
such judge failure fail-safes to `CONFIRM` ("Strict judge evaluation failed;
requiring manual confirmation for safety"), flooding the session with manual
confirmation cards under an all-allow policy — even though the session's own
provider was healthy.

Symmetrically, the chat toolbar let the user change the per-message model,
reasoning effort, and goal mode while the session's task was running. Those
controls only take effect at the next `ApplyRequestOverrides`, so a mid-task
pick was either silently deferred or raced the run — and the model-picker
persist also mutated the global default mid-run.

The first generation of this decision bound the judge to a snapshot of the
session router's active provider PLUS a per-session model-NAME pin
(`security.judge.model`), and re-bound both on every model switch through a
sync closure (`sessionJudgeSyncer` handed to the orchestrator as
`JudgeSync`). Both halves later proved to be atavisms: the judge's calls carry
no model of their own, and the router already resolves its active
provider/model per call — so the pin could only ever duplicate (or fight) the
router's own state, and the re-bind existed only to refresh data the judge
need not have captured in the first place.

## Decision

1. **The strict judge rides the session's own router.** `Build` binds a judge
   to the session's OWN router ONCE (`bindSessionJudge`) and installs it on
   the per-session registry clone, overriding the clone-inherited
   shared-registry judge. The judge issues its one-shot calls through the
   router as a plain `llm.Caller` with NO model pinned, so every call
   resolves to the router's ACTIVE provider and model, with the deterministic
   sampling profile from the model catalog layered on by the router. The
   session's usage tracker is passed at bind time, so judge token usage lands
   in the session's accounting. The shared-registry judge remains as a
   clone-time fallback only (used when a session's own bind yields none —
   no router).

2. **The session's model switch needs NO re-bind.** The judge holds the
   router itself, not a captured provider/model pair, so `Router.SetModel`
   (via `ApplyRequestOverrides`; the per-message override, `ResumeSession`,
   and `ResumeTask` all route through it) is picked up by the judge's next
   call with no re-binding — the tier-off reasoning spelling and the
   advisory cache key's model component are resolved LIVE from the router's
   `ActiveModel()` per call. There is no sync closure: the former
   `JudgeSync` orchestrator plumbing is gone. `security.judge.model` is
   removed from the config schema (a leftover key in a user config is
   silently ignored and dropped at the next save).

3. **Global default-model changes never re-bind a live session's judge.**
   `RebuildJudge` rebuilds only the shared registry's judge (the fallback,
   riding the builder's cached router), which affects sessions built
   afterwards — never a live session.

4. **Per-message selectors lock with the run.** The chat toolbar's selector
   cluster (model, reasoning, goal toggle, goal budget, E2S toggle) is
   disabled while
   `taskActive || pausing || compacting` and unlocks when the task finished,
   failed, or is cooperatively paused (a paused resume honors a freshly picked
   model/reasoning override). Frontend presentation only; the backend
   unchanged (live sends already ignore overrides, and goal/skill/agent
   requests are rejected mid-run).

## Consequences

- Everything inside a running session — LLM calls and judge evaluations alike
  — runs on the provider/model the session runs on, regardless of where the
  global default moved; and a mid-session model switch moves the judge with
  zero additional machinery.
- A judge outage now means the session's OWN provider is unhealthy (visible in
  its own responses), not a foreign endpoint picked in another session.
- Judge calls are structurally usage-accounted (the bind passes the session's
  `UsageTracker`; the shared fallback judge is untracked — no session tracker
  exists at builder level).
- Each session's judge has its own LRU cache (size
  `orchestration.maxJudgeCacheSize` per session); the advisory cache key
  includes the bare active model, resolved per call from the router — verdicts
  were already per-task-context and never shared across sessions, so no reuse
  semantics change.
- After an app restart, a restored session re-seeds its router from the
  current global default (as before); the first message's explicit override
  (persisted `selectedModel`) switches the router and the judge follows
  automatically on its next call.
- Documented in [security-model.md](../architecture/security-model.md)
  ("Judge Provisioning"), [llm-providers.md](../domains/llm-providers.md)
  (Model Override), and [rendering.md](../domains/frontend/rendering.md)
  (Per-message selector lock).
