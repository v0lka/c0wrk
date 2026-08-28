# ADR-028: Session-Pinned Tool Judge and Locked Per-Message Selectors

## Status

Accepted

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

## Decision

1. **The strict judge is session-pinned.** `Build` constructs a judge bound to
   the session's OWN router — active provider + active model
   (`sessionJudgeSyncer`) — and installs it on the per-session registry clone,
   overriding the clone-inherited shared-registry judge. The shared-registry
   judge remains as a clone-time fallback only (used when a session's own
   judge construction yields none).

2. **Only the session's own model switch re-binds its judge.**
   `ApplyRequestOverrides` invokes the sync closure after a SUCCESSFUL
   `Router.SetModel`; a failed switch re-binds nothing. The per-message
   override, `ResumeSession`, and `ResumeTask` all route through this single
   point, so the judge follows the session's model in every legal switch
   path. `security.judge.model` keeps its meaning — a per-session model-NAME
   pin — while the endpoint always follows the session's active provider.

3. **Global default-model changes never re-bind a live session's judge.**
   `RebuildJudge` rebuilds only the shared registry's judge, which affects
   sessions built afterwards (the fallback) — never a live session.

4. **Per-message selectors lock with the run.** The chat toolbar's selector
   cluster (model, reasoning, goal toggle, goal budget) is disabled while
   `taskActive || pausing || compacting` and unlocks when the task finished,
   failed, or is cooperatively paused (a paused resume honors a freshly picked
   model/reasoning override). Frontend presentation only; the backend
   unchanged (live sends already ignore overrides, and goal/skill/agent
   requests are rejected mid-run).

## Consequences

- Everything inside a running session — LLM calls and judge evaluations alike
  — runs on the provider/model the session runs on, regardless of where the
  global default moved.
- A judge outage now means the session's OWN provider is unhealthy (visible in
  its own responses), not a foreign endpoint picked in another session.
- Each session's judge has its own LRU cache (size
  `orchestration.maxJudgeCacheSize` per session); verdicts were already
  per-task-context and never shared across sessions, so no reuse semantics
  change.
- After an app restart, a restored session re-seeds its router from the
  current global default (as before); the first message's explicit override
  (persisted `selectedModel`) re-pins it via the same sync path.
- Documented in [security-model.md](../architecture/security-model.md)
  ("Judge Provisioning (session-pinned)"), [llm-providers.md](../domains/llm-providers.md)
  (Model Override), and [rendering.md](../domains/frontend/rendering.md)
  (Per-message selector lock).
