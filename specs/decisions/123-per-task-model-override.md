# ADR-123: Per-task model override for delegated subagents

## Status

Accepted

## Context

[ADR-122](./122-subagent-model-provider-override.md) gave Subagent Profiles a
composite `model:` pin (`provider/model`), routed cross-provider through sp4rk
ADR-011's per-request provider override and validated fail-closed at delegate
time. The pin, however, is a property of the **profile**: reusable and visible
at discovery time, but a Conductor delegating one-off work had no way to route
a **single** delegation to another model — it would have to author (or
duplicate) a whole profile for what is per-call intent. Three gaps remained:

1. **No per-delegation routing.** The `delegate` input schema carried no model
   field; the only override surface was the profile's frontmatter.
2. **No precedence rule.** Once a task-level override exists, a task naming a
   profile whose frontmatter also pins a model must resolve deterministically;
   nothing defined which value wins, and the validator could not say **which
   source** failed now that two hand-written sources can be present in one
   call.
3. **Badge noise.** The UI badge rendered any honored pin verbatim — including
   a pin naming the session's own model, where the pill carries no information.

The engine primitive (sp4rk
[ADR-011](https://github.com/v0lka/sp4rk/blob/main/specs/decisions/011-per-request-provider-override.md))
and the mandatory-composite grammar (ADR-122) already cover everything below
the delegation layer, so this decision only extends the delegation-layer
contract: one more hand-written model-reference site, a resolution order, a
source-labeled validation funnel, and a visibility rule for the launch badge.

### Constraints

- The ADR-122 grammar is not negotiable: every hand-written model reference is
  a full composite `"provider/model"` — bare names and empty-bare composites
  (`"provider/"`) are rejected outright (ids split on the FIRST `/`, so
  HF-style names stay expressible).
- A doomed delegation must still fail closed BEFORE any subagent launches.
- The new field must not change the `delegate` result shape, the
  `DelegationRegistry` API, or any event payload key — `model` on
  `subagent_launch` / `plan_step_start` is already additive.
- Re-delegation (a subagent's own `delegate` calls) must validate too — the
  validator rides the delegation context un-stripped by `subagentCtx`.
- Resume relaunch (the durable unit-ledger funnel) must re-run the same
  resolution without an LLM decision and re-validate the value it persisted.

## Decision

The `delegate` tool accepts an optional per-task `model` field; an explicit
task model wins over the profile pin, which wins over the session's active
model, and every honored value goes through the SAME source-labeled validator
ADR-122 introduced:

1. **Composite grammar at a new hand-written site.** `DelegationTask` gains a
   `Model string` field (`json:"model,omitempty"`; `core/tools/delegate.go`).
   The schema description carries the full rule: the value MUST be a full
   composite `"provider/model"` id routing THIS subagent to another provider;
   a bare name or `"provider/"` is rejected and fails the whole delegate call;
   an explicit model overrides the agent profile's pin; omitting it runs the
   profile's pin, or the session's active model. The composite is never
   decomposed by the caller — `buildSubAgentTask` splits it once on the first
   `/` (`llm.ParseCompositeModelID`), exactly as for profile pins.

2. **Precedence: task > profile > session.** Resolution in
   `buildSubAgentTask` (`core/conductor.go`) walks the candidate list in a
   fixed order — `task <id>` (the explicit field), then `profile <agent>` (the
   resolved profile's frontmatter pin) — and the first HONORED candidate
   wins; no candidate leaves the subagent on the session's active model
   (unchanged legacy behavior). The ordering is checked with a short-circuit:
   once a candidate is honored, later ones are skipped, so a task override
   fully suppresses the profile pin (the profile is still resolved for
   persona, tools, steps and skills — only its model link is skipped).

3. **Same validator, source-labeled.** `ModelOverrideValidator` is now
   `func(source, model string) error`; the two sources are `"task <id>"` and
   `"profile <name>"`. The delegate-time funnel (`validateDelegationTasks`)
   validates BOTH sources before any launch — the task's explicit model when
   non-empty, and the profile pin when the resolved profile carries one — and
   any failure rejects the WHOLE delegate call (error result, zero launches),
   as for every other validation failure. The builder-side closure
   (`modelOverrideValidator`, `core/builder.go`) accepts the source and
   interpolates it into its error, so a message names which field failed —
   with both sources possible in one call, a fixed "profile" wording would
   misattribute the failure. The validator stays provider-name-only (the
   router's read-only `ProviderForModel` / `KnownProviders` — the exact
   runtime miss predicate, so validation can never disagree with routing);
   whether the named provider serves the bare model remains the caller's
   responsibility per sp4rk ADR-011's caller-owns-pairing rule. A nil
   validator (no router) skips pre-launch validation and leaves the router's
   per-request provider miss as the backstop.

4. **Defensive re-check with dishonorable fallback.** `buildSubAgentTask`
   re-validates the honored composite against the router before pinning the
   caller — mirroring the agent-name double-check — because the value may
   have drifted mid-run or across a resume relaunch. The validator's error
   (already source-named) aborts the task build before dispatch. A candidate
   that cannot be honored AS SPECIFIED — a bare name, or an empty-bare
   composite when no validator is configured (nil validator / direct test
   construction) — is WARN-ignored (`warnDishonorableModelOverride`, keys
   `source` + `model`) and the NEXT link takes over: a dishonorable task
   model falls back to the profile pin; a dishonorable profile pin falls back
   to the session model. An override that cannot be honored must not silently
   degrade to legacy semantics on the wire — the WARN keeps the drift visible
   while the resolution chain stays total.

5. **Resume persistence.** The durable unit-ledger spec (`DelegationSpec`)
   embeds the full `DelegationTask`, `model` included, so the resume funnel
   relaunches with the same override without consulting the Conductor. The
   defensive re-check (decision 4) is what keeps a provider renamed while the
   app was down from launching a doomed subagent: the persisted composite is
   re-validated against the CURRENT router before the caller is pinned.

6. **Wire and UI semantics (ADR-122 mechanics, unchanged).** The honored
   composite wraps the subagent caller with
   `agent.NewModelProviderOverrideCaller(caller, provider, bare)` — every
   subagent LLM call pins `req.Provider` + the bare `req.Model`, immune to a
   mid-task `SetModel`. The subagent's ContextManager is sized from the
   HONORED model's bare form (`resolveModelMeta`; session model → registry
   default → static fallback on a miss) — never from a candidate that was
   WARN-ignored. The composite rides `SubAgentLaunch`'s additive `model` key
   and, for plan steps, the translator's additive `plan_step_start.model`
   key. The judge stays the session router's ([ADR-028](./028-session-pinned-judge.md)),
   Model Profiles sampling stays session-wide, adaptive budgets key on the
   wire model ([ADR-071](./071-adaptive-llm-request-budgets.md)) — the same
   interplay ADR-122 fixed for profile pins.

7. **Badge visibility: differ-from-session.** The delegate model badge
   (`ModelBadge` on `SubAgentBlock` / `PlanStepBlock`) renders the pinned
   model only when it DIFFERS from the conductor's session model. Both sides
   are normalized to bare names (`bareModel` — the first-`/` split shared
   with the backend's `llm.BareModel`), so `openrouter/qwen3-27b` matches a
   session running bare `qwen3-27b` and renders nothing. A pin naming the
   session's own model carries no information. An absent/empty session model
   counts as DIFFERENT (the conductor's model is unknown — no
   `session_tokens` event yet), so the badge stays visible; an empty/absent
   pinned model renders nothing. The badge is the status bar's shared model
   pill (`components/ModelBadge` — one component for the status bar's context
   block and the delegate headers): the visible TEXT is the bare model name
   (`bareModel` strips the first provider segment — the provider prefix is an
   implementation detail on the pill), while the `title` TOOLTIP carries the
   full composite `provider/model` verbatim, where the provider is exactly
   what matters; no "Forced model:" prefix. The status bar feeds the pill its
   bare `session_tokens.model`, so its rendering is unchanged by the sharing.

## Consequences

**Positive:**

- One-off cross-provider delegation no longer requires authoring a profile;
  the profile pin remains the reusable, roster-visible default while the task
  field carries per-call intent.
- One grammar and one validator serve both hand-written sources; every error
  names its source, so a batch carrying several overrides stays diagnosable.
- The badge is de-noised: a delegate running the session's own model no
  longer renders a meaningless pill.
- The override survives restarts (durable spec) and re-validates before any
  relaunch, so a stale provider name fails the task build instead of the
  subagent's first LLM call.

**Negative / trade-offs:**

- Two validation sources must stay semantically in sync; this is enforced
  structurally (both call the same injected validator through the same
  signature) but is a real coupling — a future source-specific rule would
  need its own ADR.
- Precedence is an ordering rule, not a schema property: a delegate call
  naming both a task `model` and a pin-carrying profile silently prefers the
  task. The schema description and the delegation spec document this, but the
  tool result carries no "profile pin suppressed" signal.
- The roster's `AgentDescriptor.Model` shows only the profile pin (ADR-122);
  a task-level override becomes visible only at launch (badge / launch
  event), not at discovery time.
- All ADR-122 exposures carry over: renaming a provider entry breaks
  persisted composites (now including resumed task-level ones);
  provider-name-only validation lets a real provider with an unlisted model
  still route; judge and sampling stay session-wide.

## Alternatives Considered

- **Task field replaces the profile pin outright** (no resolution chain —
  the profile's model link is removed whenever the task names any model):
  rejected — the chain IS the fallback; a task field that only sometimes
  carries a value (the common omit case) must leave the profile pin intact,
  and "task non-empty ⇒ ignore profile" is exactly the honored-candidate
  short-circuit already specified. No separate rule exists to choose.
- **Profile pin wins over the explicit task field**: rejected — an explicit
  per-call instruction must beat a reusable default, or the override could
  never deviate from a pin-carrying profile and the field would be dead on
  the most common profile-bearing delegations. Also un-overridable profiles
  would push users back to duplicating profiles — the exact gap this ADR
  closes.
- **Validator without the source label** (keep the ADR-122 signature, prefix
  errors with "profile" contextually): rejected — with two hand-written
  sources in one call, an error that cannot name its field misattributes the
  failure and sends the operator to edit the wrong file. The source is known
  at every call site, so threading it costs nothing.
- **Carry the override in the task description text** ("run on model X" as a
  prompt-level instruction): rejected — not validated, not enforced, no wire
  signal, no badge; the model would silently stay the session's unless the
  subagent obeyed prose, reintroducing exactly the unenforceable indirection
  ADR-122 removed for profiles.

## Related

- [ADR-122](./122-subagent-model-provider-override.md) — the composite
  grammar, the provider-override caller and the profile `model:` pin this
  decision extends to the task level (one-way reference; ADR-122 is not
  amended).
- [sp4rk ADR-011](https://github.com/v0lka/sp4rk/blob/main/specs/decisions/011-per-request-provider-override.md)
  — the engine primitive (`ChatRequest.Provider`, the override caller, the
  read-only validation surface, caller-owns-pairing).
- [ADR-021](./021-subagents.md) — the Subagent Profile format whose
  `model:` frontmatter is the second link in the resolution chain.
- [ADR-028](./028-session-pinned-judge.md) — the session-pinned judge that
  deliberately stays on the session model.
- [ADR-071](./071-adaptive-llm-request-budgets.md) — adaptive request budgets
  keying on the wire model.
- [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md)
  — the delegate input schema (`tasks[].model`), the validation funnel and
  the model-resolution chain at launch.
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — the
  `subagent_launch` / `plan_step_start` additive `model` keys and the badge
  visibility rule.
