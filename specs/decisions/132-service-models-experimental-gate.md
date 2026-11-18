# ADR-132: Service-Model Overrides Move Under the Experimental Gate and Gain a Settings UI

## Status

Accepted — amends [ADR-131](131-per-purpose-model-overrides.md) decision 1
(the "deliberately no UI surface" clause and the config-file-authored
delivery): `llm.service_models` is now effective only while
`experimental.enabled` is on and is edited through a Settings → LLM block.
Everything else in ADR-131 — the ten fixed keys, the load-time validation,
the once-per-build fail-soft resolution, the `pinnedModelCaller`
application, the telemetry/reasoning keying, and the advisory embedded
gates — remains authoritative, and [ADR-028](028-session-pinned-judge.md)
stays authoritative for the judge as before.

## Context

ADR-131 shipped `llm.service_models` as a config-file-only surface: no UI,
and the settings draft preserved the section verbatim so a partial UI save
could not erase it. Two considerations changed the calculus. First, the
section is a power-user routing surface whose wrong values silently
re-route the tool-call judge — a safety-adjacent consumer — so it belongs
with the other opt-in machinery the experimental master gates (E2S, the
embedded model's frontend surfaces) rather than in every operator's
default posture. Second, hand-editing composite ids is error-prone
precisely because the value format is strict; the same ModelPickerMenu the
default-model field uses eliminates the whole malformed-value class, and a
visible block makes the ten keys discoverable without the example file.

## Decision

1. **Adapter-level gate, fail-closed.** `backend/configadapter.go`
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
2. **Settings UI, draft-owned section.** A collapsed-by-default
   collapsible block ("Service models") in Settings → LLM — the
   `EmbeddedLLMSettings` shell pattern, mounted by the same
   `useExperimentalStore` gate — lists all ten keys, each with a
   ModelPickerMenu over the draft's enabled models and an explicit
   "Default" entry that clears the pin. `GetConfig` exposes the persisted
   section on `ConfigLLMResponse` (`service_models`, cloned); the settings
   draft now OWNS the section and every full-form save transmits it.
   `UpdateLLMConfig`'s `service_models` request field uses a nil-map
   sentinel: nil (the field absent from the JSON) preserves the section —
   the pre-UI contract, still pinned — while a non-nil map replaces it
   wholesale (`{}` clears), entries cleaned with the load pipeline's
   format rule (`config.CleanServiceModels`), invalid ones dropped
   fail-soft.
3. **Live application.** An LLM save already rebuilds the session router,
   and the experimental toggle rebuilds it too
   (`UpdateExperimentalFeatures`), so a gate flip or a pin edit reaches
   the session router without a restart; brand-new sessions resolve at
   build as before.

## Consequences

- The section is invisible and inert by default; enabling it requires the
  explicit experimental opt-in, matching the risk profile of a feature
  that can re-route the judge.
- Composite-id typos disappear on the UI path: the picker only offers
  enabled models of configured providers. Hand-edited files keep the
  load-time fail-soft behavior unchanged, and the warnings keep firing
  for an inert section's typos.
- The draft-owned section means a UI save CAN now clear pins — by design,
  the operator sees the block; the nil-field sentinel keeps every other
  caller (and the preservation test) safe.
- `config.example.yaml` documents the gate; the settings block is the
  discoverable surface.

## Alternatives Considered

- **A separate `llm.service_models.enabled` bool** — a second switch for a
  section whose availability is exactly the experimental master's job; the
  all-or-nothing gate exists to avoid per-feature masters (the E2S
  precedent). Rejected.
- **Gate at load (drop the section while off)** — would destroy
  hand-edited pins on every load while the gate is off, forcing re-editing
  on each flip; persist-but-inert keeps them. Rejected.
- **UI without the gate** — adds discoverability but keeps ADR-131's
  always-on effectiveness, leaving a safety-adjacent surface on by
  default; the gate is the point. Rejected.
- **Gate without the UI** — a gated but undiscoverable section serves
  nobody; the strict value format is exactly what a picker removes.
  Rejected.

## Related

- [ADR-131](131-per-purpose-model-overrides.md) — amended (decision 1):
  per-build resolution, pin application and the advisory gates unchanged.
- [ADR-068](068-embedded-llm-frontend-experimental-gate.md) — the
  frontend-gate precedent the UI block follows (same store, same
  hide-when-off contract; here the backend side is gated too).
- [ADR-103](103-vector-index-master-switch.md) — the validate → persist →
  apply-live flip pattern the adapter gate mirrors for the toggle path.
- [domains/llm-providers.md](../domains/llm-providers.md) (Model Override,
  Configuration),
  [contracts/desktop-frontend.md](../contracts/desktop-frontend.md)
  (`ConfigResponse.llm.service_models`,
  `LLMFullConfigRequest.service_models`).
- `backend/configadapter.go` (`serviceModelsWithExperimentalGate`),
  `backend/config/service_models.go` (`CleanServiceModels`),
  `backend/frontend_api_config.go` (`buildLLMResponse` exposure,
  `UpdateLLMConfig` sentinel),
  `frontend/src/components/settings/ServiceModelsSettings.tsx`.
