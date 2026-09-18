# ADR-042: Goal mode refused under SLM essential-tools narrowing

## Status

Accepted → Identifiers renamed by [ADR-043](./043-model-profiles-rename.md) (`ErrGoalBlockedBySLM` → `ErrGoalBlockedByModelProfiles`, `ConfigResponse.slm` → `ConfigResponse.model_profiles`); the decision itself stands

## Context

The SLM `essential_tools` variant narrows the Conductor's advertised tool set to cut per-prompt JSON-schema overhead. Its only call sites are `HandleMessage`'s non-goal Conductor path and the E2S branch (`runE2SWithState`) — both AFTER the goal-mode early `return`. Goal mode therefore always kept the FULL tool set, which meant the `essential_tools` toggle was **a silent no-op in goal mode**: the operator's profile claimed to narrow the tool set, but a goal run quietly ignored the narrowing. This divergence was documented prose, not an enforced invariant (see [../domains/model-profiles.md](../domains/model-profiles.md)).

The narrowing is tuned for **single-pass** Conductor work and, applied as written, would hide the goal-loop tooling (`propose_goal`, `declare_goal_status`, `declare_verification`). If the narrowing were ever applied to a goal run, the goal could not be derived or concluded — the loop would be unrunnable, not merely degraded.

Two behaviors were possible: (a) keep the status quo — goal mode silently carries the full tool set and the toggle stays a lie there; or (b) state and enforce the mutual exclusion explicitly. Separately, the goal derivation and verification passes are specialized runs that carry their own core directive, so they had also been ignoring the profile's `system_prompt` variant — the very passes that run *under* an active profile.

## Decision

Make goal mode **unavailable** while the essential-tools narrowing is active, and enforce it in depth:

- **The rule.** `goalBlocked = SLM.Enabled && EssentialTools.Enabled`, using the EFFECTIVE/resolved values (the experimental gate is already folded into `SLM.Enabled` by `effectiveSLMConfig`). The two operands are distinct and both must hold: master-on with the variant off leaves the tool set untouched, so goal mode stays available. The predicate is a property of the resolved configuration and re-reads the catalog, so a runtime `SelectSLMProfile` / `SetSLMEnabled` takes effect without a restart.
- **Three enforcement layers.** (1) Frontend UX, fail-safe: `slmGateStore` + `useSLMGate` latch the effective facts and `lib/goalGate.ts` disables the goal toggle with a reason, clearing any stale arming (`inputModeStore.disarmGoal`). (2) Backend session API, authoritative: `FrontendAPI.SendMessage` refuses a goal (explicit flag OR a `/goal` prefix on the post-preprocess text) and `ResumeTask` / `ResumeSession` refuse a paused non-terminal goal, both BEFORE any side effect via `slmGoalBlocked` / `goalResumeBlockedBySLM`. (3) Orchestrator invariant: `HandleMessage`'s goal branch and `resumeGoalLoop` entry return the new sentinel `ErrGoalBlockedBySLM`, so a direct caller bypassing the API hits the same wall.
- **Expose the facts.** `ConfigResponse` gains `slm` (`SLMSettingsResponse {enabled, essential_tools_enabled}`) carrying the EFFECTIVE values so the frontend can mirror the gate without re-deriving it. No new event is introduced — the frontend reuses `GetConfig` plus the existing `backend:ready` / `config:updated` retries.
- **Fail-open by design.** A not-yet-loaded config never blocks (the invariant is enforced downstream regardless), and a task-store read failure is treated as "not blocked" (fail-open): the resume is independently re-checked by the session manager (`Manager.ResumeTask`, before any task activation) and `resumeGoalLoop` is the ultimate authority, so a transient read error must not strand a legitimate resume.
- **Prompt-variant parity.** Because derivation and verification run *under* an active profile, they now opt into the `system_prompt` Lite swap via `buildSpecializedSystemPromptWithLite` (their own `goal_*_lite.md` directives). Subagent-profile specialized runs deliberately do NOT opt in — a profile's body stays authoritative even under Lite. With SLM off or `lite` off, output is byte-identical to the un-swapped baseline.

## Consequences

- Positive: the `essential_tools` toggle is no longer a silent no-op in goal mode — the exclusion is explicit, surfaced in the UI with an actionable message, and enforced at three layers.
- Positive: goal derivation/verification no longer silently ignore the profile's prompt variant, so an active profile actually tunes the whole goal run.
- Positive: a blocked goal never leaves a phantom message or task (the refusal is before any side effect).
- Negative: goal mode and a narrowed tool set become mutually exclusive — a user who wants both must disable the variant (or the whole profile). This is the intended trade: an unrunnable loop is worse than a refused one.
- Negative: new coupling between the SLM domain and goal mode across three enforcement points that must stay consistent (frontend gate, session API, orchestrator sentinel).

## Alternatives Considered

- **Keep the status quo** (goal mode keeps the full tool set; `essential_tools` is a no-op there). Rejected: a silent divergence — the control appears effective but is not.
- **Apply the narrowing in goal mode but exempt the goal-loop tools.** Rejected: the narrowing targets single-pass work; a goal run is not single-pass, and exempting a growing tool set erodes the narrowing's purpose while still hiding ordinary tools from the goal turns.
- **Warn-only** (allow the goal, show a notice). Rejected: the loop would still be unrunnable because the tools are hidden — a warning does not prevent the broken run.
