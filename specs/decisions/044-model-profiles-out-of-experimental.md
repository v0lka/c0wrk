# ADR-044: Model Profiles graduated out of the experimental gate

## Status

Accepted → Partially supersedes [ADR-041](./041-slm-profiles.md) and [ADR-043](./043-model-profiles-rename.md) (the experimental-gate coupling only; the profile-catalog architecture and the Model Profiles nomenclature stand unchanged) → Partially superseded by [ADR-068](./068-embedded-llm-frontend-experimental-gate.md) (the "experimental.enabled gates only E2S" scope claim only; the Model Profiles graduation stands unchanged)

## Context

Model Profiles shipped behind the master experimental-features switch (`experimental.enabled`) — [ADR-022](./022-small-llm-profile.md) introduced it that way, and the catalog rework ([ADR-041](./041-slm-profiles.md)) and the rename ([ADR-043](./043-model-profiles-rename.md)) carried the coupling forward. Concretely the gate did four things:

1. `effectiveModelProfilesConfig` (`backend/configadapter.go`) forced `Enabled = false` at the `ToBuilderConfig` boundary when the gate was off, so the builder saw the feature as off regardless of the stored `model_profiles.enabled`.
2. `SetModelProfilesEnabled` (`backend/frontend_api_config.go`) failed closed when turning the profile on while the gate was off.
3. `UpdateExperimentalFeatures(false)` also persisted `model_profiles.enabled = false` in the same write (the one-way reset), so re-enabling the gate never resurrected the profile.
4. The Model Profiles settings tab hid while the gate was off.

Meanwhile the feature grew its own manual master toggle (`model_profiles.enabled`, default off, no auto-detection) that already provides the "off by default, explicit opt-in" guarantee. Two switches for one feature double-gate it and couple unrelated lifecycles: the experimental switch is an all-or-nothing development switch, while Model Profiles is a stable, operator-facing tuning surface. The coupling also produced a silent-divergence hazard — a stored `model_profiles.enabled: true` could disagree with the runtime (UI shows on, builder runs off) — and imposed a cross-section consistency burden (every mutation of one switch had to know about the other).

## Decision

Graduate Model Profiles out of the experimental gate. The manual `model_profiles.enabled` master toggle becomes the feature's **only** switch.

- **No gate at the builder boundary.** `effectiveModelProfilesConfig` (`backend/configadapter.go`) resolves the persisted `model_profiles:` section against the catalog and returns it; it no longer force-sets `Enabled = false`. `ToBuilderConfig` carries the master toggle through verbatim, so the effective profile is exactly what the operator persisted.
- **`SetModelProfilesEnabled` is unconditional.** The RPC no longer rejects enabling while the experimental switch is off — both enabling and disabling are always allowed; a set matching the stored value stays a no-op.
- **`UpdateExperimentalFeatures` no longer touches Model Profiles.** It persists the gate, rebuilds the LLM router, and pushes the refreshed E2S gate onto live session orchestrators (`SetE2SSettings`). The `model_profiles.enabled = false` reset and the Model Profiles gate recompute/push are removed from its body, so its scope is the E2S execution mode only.
- **Response types describe the new scope.** `ExperimentalSettingsResponse` (`backend/api_types.go`) states the switch gates only the E2S execution mode; `ModelProfilesSettingsResponse.Enabled` is the resolved master toggle carried through verbatim (no experimental folding).
- **The settings tab is always visible.** The frontend no longer hides the Model Profiles tab behind the experimental switch (the experimental control's own description now names only E2S).
- **Nothing else moves.** The master toggle itself stays (default off, manual only); `experimental.enabled` still gates the E2S execution mode fail-closed; RESEARCH mode was never gated and remains ungated.

## Consequences

- Positive: one switch per feature — the two switches no longer couple, so the silent-divergence hazard (stored toggle on, runtime off) and the one-way-reset consistency burden disappear.
- Positive: `config.yaml` semantics match the UI — a stored `model_profiles.enabled: true` is honored regardless of the experimental switch.
- Positive: `experimental.enabled` becomes a single-purpose switch (E2S), matching its `config.example.yaml` description ("Gated features today: the E2S explicit-state execution mode").
- Negative: enabling Model Profiles no longer implies opting into experimental features — that is the intended outcome of graduation, and the manual-off-by-default master toggle preserves the "nothing activates unless the operator asks for it" guarantee.
- Negative: the tests that encoded the removed coupling (`TestToBuilderConfig_ExperimentalGatesModelProfiles`, `TestSetModelProfilesEnabled_EnableRequiresExperimental`, `TestUpdateExperimentalFeatures_DisableClearsModelProfilesEnabled`) assert obsolete behavior and are updated or removed alongside the code change.

## Alternatives Considered

- **Keep the feature experimental.** Rejected: the feature has stabilized (catalog, rename, goal-mode gate) and already carries its own manual opt-in toggle; a second switch only confuses the operator and couples unrelated lifecycles.
- **Keep the builder gate but drop the one-way reset.** Rejected: the gate would still force the feature off in the builder, so the stored toggle and the effective behavior could disagree (UI shows on, runtime off) — the same silent divergence the graduation removes.
- **Graduate only the UI (always show the tab) while keeping the builder gate.** Rejected: identical divergence — the tab would be visible and the toggle settable while the feature stayed inert at runtime.
- **Remove the experimental switch entirely.** Rejected: `experimental.enabled` still gates the E2S execution mode; only the Model Profiles coupling is removed here.
