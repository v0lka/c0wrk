# ADR-131: Per-Purpose Service-Model Overrides (`llm.service_models`)

## Status

Accepted — amends [ADR-028](028-session-pinned-judge.md) decision 2: the
strict judge's model resolution gains an opt-in, config-level exception (a
resolved `llm.service_models.judge` pin serves judge calls). The judge pin
ADR-028 removed was a **per-session** pin tracking session state; this ADR
adds a **static config** pin with no session coupling, and ADR-028 remains
authoritative for everything else — binding once at `Build`, no re-bind path,
no sync closure, the selector lock, and "global default changes never re-bind
a live session's judge".

Amended by [ADR-132](132-service-models-experimental-gate.md): the section
is effective only while `experimental.enabled` is on (persisted but inert
while off), and decision 1's "deliberately no UI surface" clause is
relaxed — the section is edited through a Settings → LLM block under the
same gate. The key set, load validation, resolution, application, and
gate mechanics of this ADR are unchanged.

## Context

Every auxiliary one-shot LLM call outside the agent execution loop — tool-call
judging, task routing, step reflection, session titles, commit messages, the
prompt optimizer's two passes, compaction summarization, goal and prompt
suggestions — rides the session router's ACTIVE provider/model by construction
([ADR-028](028-session-pinned-judge.md)). That default is correct for the
conversational loop, but auxiliary calls have heterogeneous needs: the judge
and routing want a capable model, titles and suggestions want a cheap one, and
an operator running the embedded local model as the session default does not
want a background title generation to wake a multi-gigabyte weight load for a
call that never needed the local server.

ADR-028 removed the only prior judge model pin (`security.judge.model`)
because a per-session pin could only duplicate (or fight) the router's own
active-model state, and its re-bind closure existed to refresh data the judge
need not capture. That argument applies to pins that TRACK session state; it
does not cover an operator wanting a FIXED, different model for a service
purpose. There was no mechanism for that: `llm.default_model` moves every call
at once, per-message overrides move the whole session, and the per-delegation
overrides ([ADR-122](122-subagent-model-provider-override.md),
[ADR-123](123-per-task-model-override.md)) apply only to subagents.

## Decision

1. **One config section, ten fixed keys.** `llm.service_models` maps a
   deliberately closed key set onto composite `"provider/model"` ids (split on
   the FIRST `/` only — the model name itself may contain `/`, the
   [ADR-122](122-subagent-model-provider-override.md) mandatory-composite
   rule): `judge` (tool-call confirmation judge), `router` (CHAT/CODE task
   router), `reflector` (step reflection), `title`, `commit_message`,
   `optimize_extract`, `optimize_rewrite`, `compaction_summarize`,
   `goal_suggest`, `prompt_suggest`. The canonical key set and its validation
   live in `backend/config/service_models.go`; core mirrors the three keys it
   wires itself (`judge`, `router`, `reflector` as local constants, because
   core cannot import backend/config) and the lockstep is test-enforced. An
   unknown key or a broken value (empty, bare, `"/model"`, `"provider/"`) is
   dropped at load with a per-key warning and rewritten away at the next save;
   an absent or empty section warns about nothing and is the no-override
   behavior. There is deliberately no UI surface — the section is
   config-file-authored, and the settings draft (`UpdateLLMConfig`) carries no
   `service_models` field, so a partial UI save preserves the section verbatim
   (`candidate := previous`).

2. **Resolution is per-build and fail-soft.** `core/model_overrides.go`
   `resolveServiceModelOverrides` resolves the configured map ONCE per
   session/router build against THAT router (`core/builder.go` `Build`, and
   again on every router rebuild so a provider the rebuild dropped falls back
   rather than pinning to a ghost). Membership is checked provider-name-only
   via `Router.ProviderForModel` — the same predicate
   `modelOverrideValidator` applies to delegation composites; the bare model
   need not appear in the provider's list. A key that fails either check is
   absent from the resolved set, so its consumer falls back to the session
   model — never a build or startup failure; unknown providers produce ONE
   aggregated Warn naming every dropped key and the known providers. The
   result is an immutable `ServiceModelOverrides` set threaded into the
   orchestrator (`OrchestratorDeps.ServiceModels`) and held on the builder.

3. **Application per consumer, pinning per call.** Each consumer site resolves
   its key from the per-build set and, when a pin resolves, wraps its caller
   in `pinnedModelCaller` (built on sp4rk's
   `agent.NewModelProviderOverrideCaller`, sp4rk ADR-011): every request —
   each oneshot attempt included — re-stamps `req.Provider` + the bare
   `req.Model`, so a mid-task `Router.SetModel` NEVER redirects a pinned call.
   The sites: the strict judge (`newJudgeForRouter`/`bindSessionJudge` — the
   wrapper reports the pin's composite via `ActiveModel()`, the structural
   probe the judge uses for its tier-off reasoning spelling and advisory cache
   key, so both follow the PIN, not the session model), the router and
   reflector (`overrideWrap` per consumer in the `buildCoreAgents` wiring —
   two consumers never share one wrapper), and the seven oneshot service kinds
   through the single `applyServiceModelOverride` seam, which must run before
   the request is built: the effective model id feeds `serviceReasoningEffort`
   (the pin's bare model resolves the reasoning tier) and the per-call
   telemetry log's `model` field (the pin's composite id, verbatim). Without a
   resolved pin every site passes its caller through as the SAME instance —
   byte-for-byte the pre-override wiring.

4. **Advisory embedded gates key on the effective target.** The backend's
   embedded service gates (`serviceTargetIsEmbedded` /
   `anyEmbeddedTargetFor`) resolve the imminent call's target through
   `OrchestratorBuilder.ServiceModelPin(kind)` — the SAME per-build resolution
   the call itself performs, so no config-side re-implementation can diverge.
   A gate warms the embedded model only when the effective target of at least
   one of its kinds is the embedded provider (a pin pointing elsewhere
   suppresses the warm; an unresolvable pin behaves as before). The shared
   background gate serves its kinds as a union. Transient config/router
   divergence windows have performance-only effect: an unnecessary warm, or —
   in the stale window between a config save and the router rebuild — a
   skipped gate whose call loads through the ensure-loaded transport under the
   request's own budget. Compaction and goal-suggest have no pre-dispatch gate
   by design: they run inside a task, guarded by the transport.

## Consequences

- An operator can give each auxiliary purpose its own model — a strong judge
  and router on cloud providers while the session converses on a local model,
  or cheap local models for titles and suggestions — without touching the
  session's default and without a UI round-trip.
- With an embedded default and a pin pointing away from it, background service
  calls no longer trigger multi-gigabyte local weight loads; the gates follow
  the effective target.
- Pins are STATIC per build: a config edit applies to sessions (and judges)
  built after the save; a live session keeps its build-time resolution — the
  same application model as every other config section, and consistent with
  ADR-028's "global changes never re-bind a live session's judge".
- Downstream per-model machinery follows the pin automatically where it keys
  on the wire model or the caller's active model: reasoning tiers, telemetry,
  the judge's cache-key model component. Usage accounting is unchanged: the
  judge's bind still passes the session's usage tracker, and service calls
  keep their existing timeouts (`timeouts.serviceLLMRequestTimeout` where
  armed).
- The closed key set is maintained in two packages (backend/config canonical,
  core mirror) — a new service call site must extend both and their tests, or
  its key is silently dropped at load.
- The section rides outside the settings draft, so the UI cannot clear it and
  a careless future extension of the draft request cannot erase it (guarded by
  `TestUpdateLLMConfig_PreservesServiceModels`).

## Alternatives Considered

- **Re-adding a per-session judge pin (`security.judge.model`)** — exactly the
  atavism ADR-028 removed: a session-scoped value that duplicates router
  state, needs a sync path, and resets on every session. Rejected; the static
  config pin carries no session coupling and needs no re-bind.
- **Live re-resolution on every call** — a config save would then race
  in-flight calls and the resolution Warn would fire per call. Rejected: the
  per-build snapshot is deterministic, and the rebuild path already
  re-resolves fail-soft against the new router.
- **One "small model" knob for all auxiliary calls** (the retired inline
  `small_llm:` idea) — the purposes need different models (a weak judge is a
  safety regression; a weak router mis-buckets tasks), and Model Profiles
  already own sampling/tool-set tuning, not per-purpose routing. Rejected.
- **A config-side predicate for the embedded gates** (re-implementing
  composite parsing and provider checks in the backend) — two resolvers that
  can drift; rejected in favor of exposing `ServiceModelPin` from the same
  resolution the call uses.
- **A settings UI for per-purpose pickers** — deferred: the config-file
  surface keeps the draft-preservation semantics trivial, and the ten keys are
  an operator/power-user surface, not a first-run one.

## Related

- [ADR-028](028-session-pinned-judge.md) — amended (decision 2): the
  session-pinned judge and its no-pin construction remain the default; the
  judge opt-in pin is the exception this ADR adds.
- [ADR-122](122-subagent-model-provider-override.md) / [ADR-123](123-per-task-model-override.md)
  — the composite-id rule and cross-provider pinning machinery this ADR
  reuses; those ADRs govern subagent execution, this one auxiliary service
  calls.
- [ADR-066](066-embedded-llm-runtime.md) / [ADR-067](067-memory-aware-embedded-llm-provisioning.md)
  — the embedded runtime whose cold-load cost motivated the advisory gates.
- [domains/llm-providers.md](../domains/llm-providers.md) (Model Override,
  Service Call Observability, Configuration),
  [architecture/security-model.md](../architecture/security-model.md) (Judge
  Provisioning), [domains/orchestration/router.md](../domains/orchestration/router.md)
  (routing/reflection models).
- `core/model_overrides.go` (resolver, `pinnedModelCaller`, the seams),
  `backend/config/service_models.go` (canonical key set, load validation),
  `core/builder.go` (per-build resolution and the judge/router/reflector
  wiring), `backend/frontend_api_embedded.go` (advisory gates).
