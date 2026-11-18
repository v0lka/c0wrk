# ADR-130: Per-Purpose Service-Model Overrides (`llm.service_models`)

## Status

Accepted — amends [ADR-028](028-session-pinned-judge.md) decision 2: the
strict judge's model resolution gains an opt-in, config-level exception (a
resolved `llm.service_models.judge` pin serves judge calls). The judge pin
ADR-028 removed was a **per-session** pin tracking session state; this ADR
adds a **static config** pin with no session coupling, and ADR-028 remains
authoritative for everything else — binding once at `Build`, no re-bind path,
no sync closure, the selector lock, and "global default changes never re-bind
a live session's judge".

The experimental gate and the Settings UI for the section were briefly
drafted as a separate ADR-131 (same session, same branch); they are folded
into this record as decisions 5 and 6 and the separate file is removed —
ADR-131 no longer exists and its number stays unused (numbers are never
reused). The section is effective only while `experimental.enabled` is on
(persisted but inert while off), and the original "deliberately no UI
surface" clause of decision 1 is superseded within this record: the section
is edited through a Settings → LLM block under the same gate.

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

The gate-and-UI half has its own motivation. The section is a power-user
routing surface whose wrong values silently re-route the tool-call judge — a
safety-adjacent consumer — so it belongs with the other opt-in machinery the
experimental master gates (E2S, the embedded model's frontend surfaces)
rather than in every operator's default posture. Hand-editing composite ids
is error-prone precisely because the value format is strict; the same
ModelPickerMenu the default-model field uses eliminates the whole
malformed-value class, and a visible block makes the ten keys discoverable
without the example file.

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
   behavior. Delivery is experimental-gated and UI-edited: the section is
   effective only while `experimental.enabled` is on (decision 5) and is
   edited through a Settings → LLM block (decision 6). Hand-authored entries
   keep normalizing (and warning) through the load pipeline even while the
   gate is off, so a typo is reported in an inert section too.

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

5. **Adapter-level gate, fail-closed.** `backend/configadapter.go`
   (`serviceModelsWithExperimentalGate` — the single config →
   `BuilderConfig` conversion point) maps the persisted section to nil
   while `experimental.enabled` is off. Every pin consumer is covered at
   once: session builds and per-config router rebuilds resolve no
   overrides, and the embedded readiness gates' `ServiceModelPin` advisory
   resolves nothing. The section itself stays persisted untouched, so
   flipping the switch back restores the pins at the next config rebuild —
   the persist-but-inert semantics the `web_access` section has under its
   own gate. The gate is deliberately NOT part of the load validation:
   hand-edited entries keep normalizing (and warning) so a typo is
   reported even while the section is inert.

6. **Settings UI, draft-owned section, live application.** A
   collapsed-by-default collapsible block ("Service models") in
   Settings → LLM — the `EmbeddedLLMSettings` shell pattern, mounted by
   the same `useExperimentalStore` gate — lists all ten keys, each with a
   ModelPickerMenu over the draft's enabled models and an explicit
   "Default" entry that clears the pin. `GetConfig` exposes the persisted
   section on `ConfigLLMResponse` (`service_models`, cloned); the settings
   draft OWNS the section and every full-form save transmits it.
   `UpdateLLMConfig`'s `service_models` request field uses a nil-map
   sentinel: nil (the field absent from the JSON) preserves the section —
   the pre-UI contract, still pinned — while a non-nil map replaces it
   wholesale (`{}` clears), entries cleaned with the load pipeline's
   format rule (`config.CleanServiceModels`), invalid ones dropped
   fail-soft. An LLM save already rebuilds the session router, and the
   experimental toggle rebuilds it too (`UpdateExperimentalFeatures`), so
   a gate flip or a pin edit reaches the session router without a
   restart; brand-new sessions resolve at build as before.

## Consequences

- An operator can give each auxiliary purpose its own model — a strong judge
  and router on cloud providers while the session converses on a local model,
  or cheap local models for titles and suggestions — without touching the
  session's default.
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
- The settings draft OWNS the section: a UI save can clear pins — by design,
  the operator sees the block — while the nil-field sentinel keeps every
  other caller safe (field absent from the JSON = the section is preserved;
  pinned by `TestUpdateLLMConfig_PreservesServiceModels`).
- The section is invisible and inert by default; enabling it requires the
  explicit experimental opt-in, matching the risk profile of a feature that
  can re-route the judge.
- Composite-id typos disappear on the UI path: the picker only offers enabled
  models of configured providers; hand-edited files keep the load-time
  fail-soft behavior, and the warnings keep firing for an inert section's
  typos.

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
- **A settings UI for per-purpose pickers without the gate** — deferred in
  the original draft of this record (the config-file surface kept
  draft-preservation trivial), then shipped under the experimental gate once
  the judge-rerouting risk profile and the malformed-value argument landed
  (decisions 5 and 6); the surviving rejected variant is a UI **without**
  the gate.
- **A separate `llm.service_models.enabled` bool** — a second switch for a
  section whose availability is exactly the experimental master's job; the
  all-or-nothing gate exists to avoid per-feature masters (the E2S
  precedent). Rejected.
- **Gate at load (drop the section while off)** — would destroy
  hand-edited pins on every load while the gate is off, forcing re-editing
  on each flip; persist-but-inert keeps them. Rejected.
- **UI without the gate** — adds discoverability but keeps the section
  always-on, leaving a safety-adjacent, judge-rerouting surface on by
  default; the gate is the point. Rejected.
- **Gate without the UI** — a gated but undiscoverable section serves
  nobody; the strict value format is exactly what a picker removes.
  Rejected.

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
- [ADR-068](068-embedded-llm-frontend-experimental-gate.md) — the
  frontend-gate precedent the Settings block follows (same store, same
  hide-when-off contract; here the backend side is gated too).
- [ADR-103](103-vector-index-master-switch.md) — the validate → persist →
  apply-live flip pattern the adapter gate mirrors for the toggle path.
- [domains/llm-providers.md](../domains/llm-providers.md) (Model Override,
  Service Call Observability, Configuration),
  [architecture/security-model.md](../architecture/security-model.md) (Judge
  Provisioning), [domains/orchestration/router.md](../domains/orchestration/router.md)
  (routing/reflection models),
  [contracts/desktop-frontend.md](../contracts/desktop-frontend.md)
  (`ConfigResponse.llm.service_models`, `LLMFullConfigRequest.service_models`).
- `core/model_overrides.go` (resolver, `pinnedModelCaller`, the seams),
  `backend/config/service_models.go` (canonical key set, load validation,
  `CleanServiceModels`), `core/builder.go` (per-build resolution and the
  judge/router/reflector wiring), `backend/configadapter.go`
  (`serviceModelsWithExperimentalGate`), `backend/frontend_api_config.go`
  (`buildLLMResponse` exposure, `UpdateLLMConfig` sentinel),
  `backend/frontend_api_embedded.go` (advisory gates),
  `frontend/src/components/settings/ServiceModelsSettings.tsx` (the
  Settings block).
