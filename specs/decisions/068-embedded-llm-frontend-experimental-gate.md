# ADR-068: The embedded local model's frontend surfaces live behind the experimental gate

## Status

Accepted → Partially supersedes [ADR-044](./044-model-profiles-out-of-experimental.md) (the "experimental.enabled gates only E2S" scope claim only; the Model Profiles graduation — its own master toggle, no builder gate, always-visible tab — stands unchanged) and the same incidental clause in [ADR-056](./056-research-always-on-global-seeding.md) (whose substantive decision — RESEARCH always on, never gated — stands unchanged)

## Context

The embedded local model ([ADR-066](./066-embedded-llm-runtime.md), [ADR-067](./067-memory-aware-embedded-llm-provisioning.md)) shipped with its full frontend surface ungated: the Settings block mounts on every launch, and the model's entries appear in both model pickers as soon as the backend generates the provider record. The experimental switch (`experimental.enabled`) is the app's all-or-nothing development switch; since [ADR-044](./044-model-profiles-out-of-experimental.md) it gated only the E2S execution mode, with every other surface either always on (RESEARCH) or carrying its own master toggle (Model Profiles).

The embedded runtime is the first subsystem since E2S whose operator-facing surface should stay hidden while it stabilizes: it is machine-dependent (a multi-gigabyte install, a hardware probe, a memory planner that can refuse the machine), so advertising the surface on every machine invites installs the hardware cannot hold, and the launch-shape surface is expected to keep evolving. The gate also had to stay frontend-only: the backend's provider sync, RPC surface and request path are load-bearing for config integrity — a generated `embedded` record that disappeared with the gate would invalidate stored composite defaults — and a backend gate would re-create exactly the two-switch coupling ADR-044 removed for Model Profiles.

## Decision

While `experimental.enabled` is off, the frontend renders none of the embedded local model's interactive surfaces:

- `LLMSettings` does not mount the `EmbeddedLLMSettings` block (the gate is a render guard in the parent, so a hidden block fires no embedded RPC at all).
- Both model pickers drop the backend-owned `embedded` entries: `excludeEmbeddedModel` (`frontend/src/lib/llm-providers.ts`) is applied at the two feed sites — the settings draft list that feeds the Settings → LLM default-model picker, and `useConfigData`'s list that feeds the chat toolbar's `ModelCombobox`.

Both call sites read the switch reactively from `useExperimentalStore`, so flipping it in Settings hides/reveals the surfaces within the same session, without a reload. The backend stays ungated end-to-end: `SyncEmbeddedProvider` keeps generating the record, every RPC keeps working, and resolution, the router and the ensure-loaded path are untouched — a stored `embedded/Bonsai 2 27B` default keeps resolving underneath the hidden picker. The status-bar indicator is deliberately NOT gated: it is passive state, not functionality. The gate hides surfaces; it does not disable the subsystem.

## Consequences

- Positive: one consistent switch governs the app's two experimental surfaces (E2S and the embedded UI) without per-feature config keys, and the embedded surface can keep changing without every machine discovering it.
- Positive: no config-schema change, no backend change, no migration; the gate is three call sites reading a flag they already load.
- Negative: the switch is no longer single-purpose — ADR-044's "single-purpose switch" consequence and `config.example.yaml`'s "Gated features today" wording age with this decision.
- Negative: a user whose default model IS the embedded model and who turns the gate off keeps a selection the pickers no longer offer (the trigger still names it; the entry is not re-selectable until the switch returns). Honest for a frontend-only gate, and the surface reappears with the switch.

## Alternatives Considered

- **A per-feature config toggle for the embedded UI.** Rejected: a second switch for one feature is exactly the coupling ADR-044 removed for Model Profiles, and the embedded surface has no other manual toggle to hand the job to.
- **Backend gating** (refusing the RPCs or dropping the generated record while the gate is off). Rejected: it would invalidate stored composite defaults, re-create the silent-divergence hazard between stored config and effective behavior, and contradict the intent that this gate hides UI, not capability.
- **Gating the status-bar indicator too.** Rejected for now — the indicator is read-only state (it never installs, loads or probes), so it is not part of the gated functionality; revisitable if it ever grows actions.
