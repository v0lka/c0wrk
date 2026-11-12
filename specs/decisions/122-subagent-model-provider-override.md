# ADR-122: Cross-provider model overrides for Subagent Profiles

## Status

Accepted

## Context

[ADR-021](./021-subagents.md) gave Subagent Profiles a `model:` frontmatter
field, applied at delegation time by wrapping the subagent's caller with
`agent.NewModelOverrideCaller` — which pins only `req.Model`, a bare name
served by whatever provider the session router currently resolves. Two gaps
remained:

1. **No cross-provider routing.** A profile could not force a model living on
   a provider other than the session's active one (the session runs on a cloud
   provider while a reviewer profile is pinned to a local OpenAI-compatible
   server). Routing had no per-caller escape hatch — only the session-wide
   `SetModel`.
2. **No pre-launch validation.** Nothing checked a `model:` override against
   the configured providers. A profile pinning an unservable model surfaced
   only as the subagent's first LLM-call failure — after persona, prompt and
   ContextManager build work — as a mid-run error.

sp4rk added the engine primitive on its side ([sp4rk ADR-011](https://github.com/v0lka/sp4rk/blob/main/specs/decisions/011-per-request-provider-override.md)):
`ChatRequest.Provider` (per-request provider routing; an unknown name fails
immediately as a non-retryable config error before the retry loop; empty =
byte-identical legacy path), `agent.NewModelProviderOverrideCaller`, and the
read-only `Router.ProviderForModel` / `Router.KnownProviders` surface for
pre-launch validation. c0wrk had to wire profile overrides onto that primitive
and decide its own validation contract, context sizing, and interplay with the
session-level subsystems (judge, Model Profiles sampling, request budgets).

### Constraints

- A model reference must be unambiguous BY GRAMMAR, not by lookup order: the
  full composite `"provider/model"` form is mandatory — bare names (and
  composites with an empty bare part) are rejected outright everywhere a
  model reference is hand-written (profile frontmatter, `llm.default_model`,
  the per-message override). The same bare name may be exposed by several
  providers; only the composite form names the intended one, and a silent
  first-match fallback is never acceptable.
- The model name itself may contain `/` (HF-style ids): ids split on the
  FIRST `/` only, so `lmstudio/qwen/qwen3-coder-30b` names that exact wire
  model at `lmstudio` — the mandatory composite form keeps such names
  expressible instead of colliding with the provider prefix.
- The `delegate` input schema and the `PlanStep` shape stay untouched.
- A doomed delegation must fail closed BEFORE any subagent launches.
- Re-delegation (a subagent's own `delegate` calls) must validate too.

## Decision

A Subagent Profile's `model:` field MUST be a full composite id; it is
decomposed at delegation time and the composite never reaches the wire:

1. **Composite grammar and cross-provider routing.** `llm.ParseCompositeModelID`
   (split on the first `/`) parses the raw `Metadata.Model` in
   `buildSubAgentTask` (`core/conductor.go`). The id wraps the subagent
   caller with `agent.NewModelProviderOverrideCaller(caller, provider, bare)` —
   every subagent LLM call pins `req.Provider` to the named configured
   provider and `req.Model` to the bare name, so the request routes
   cross-provider and is immune to a mid-task `SetModel`. A bare name is not
   an input anywhere in this path: the validator rejects it at delegate-call
   time, and a bare value reaching `buildSubAgentTask` anyway (nil validator /
   direct test construction) is IGNORED with a one-shot WARN — the subagent
   runs on the session's active model, since an override that cannot be
   honored as specified must not silently degrade to legacy semantics.

2. **Validation: full-composite mandatory, provider-name-only.** Overrides
   fail closed BEFORE any subagent launches, at two layers calling
   the SAME injected validator:
   - **Delegate-time.** `validateDelegationTasks` (`core/tools/delegate.go`)
     runs the `ModelOverrideValidator` for every task whose resolved profile
     carries a non-empty `Metadata.Model`; a failure rejects the WHOLE
     delegate call (`task %q: <err>`, error result, zero launches). The
     validator rides the delegation context beside the agent resolver
     (`WithModelOverrideValidator`, injected in `RunConductor`), and like the
     resolver it is not stripped by `subagentCtx`, so a redelegating subagent's
     `delegate` calls validate as well.
   - **Defensive re-check at launch.** `buildSubAgentTask` re-runs the
     validator before wrapping the caller (mirroring the agent-name
     double-check, covering a profile edited mid-run); the error is wrapped as
     `delegation %q: …`.
   The validator itself is built once by the builder (`modelOverrideValidator`,
   wired as `OrchestratorDeps.ValidateModelOverride` → `conductorDeps`) as a
   closure over the session router's read-only `ProviderForModel` /
   `KnownProviders`. It validates the **provider name only** — the exact
   runtime `Call` miss predicate; whether the named provider serves the bare
   model stays the caller's responsibility per sp4rk ADR-011 — and its error
   names the profile, the model, the unknown provider and the sorted known
   providers. A bare name is rejected outright with the composite guidance,
   and so is a composite with an empty bare part (`"provider/"` — pinning
   only the provider would let `req.Model` silently inherit the session's
   active model, the opposite of the override's intent). A nil router yields
   a nil validator (no pre-launch validation), and the router's per-request
   provider miss remains the backstop on every path.

3. **CM calibration follows the effective model.** The subagent's
   ContextManager is built from `resolveModelMeta(ctx, effectiveModel)`, where
   `effectiveModel` is the override's bare form; a registry miss falls
   through the existing chain (session model → registry default → static
   fallback). A profile pinned to a small-window model gets compaction
   thresholds sized to its own context window, not the session's.

4. **Interplay stays as already documented — nothing re-wired.**
   - The strict tool judge remains the session router's judge, bound once by
     `Build` ([ADR-028](./028-session-pinned-judge.md)): a per-delegation
     model override never re-points tool-policy verdicts; they keep evaluating
     on the session's provider/model.
   - Model Profiles sampling stays session-wide: `resolveSamplingFunc`
     (`core/builder.go`) resolves the active profile's sampling values at
     router construction for the whole session; a delegation-time model
     override neither switches the active profile nor re-resolves sampling.
   - Adaptive request budgets key on the wire model
     ([ADR-071](./071-adaptive-llm-request-budgets.md)): the budget transport
     extracts the model from the outgoing request body, so an override request
     is budgeted by the override model's own learned class, not the session's.

The composite id also surfaces verbatim at discovery: `AgentDescriptor.Model`
(populated from `AgentMetadata.Model`) rides the agents RPC to the roster and
the subagent badge, so a pinned override is visible before delegation.

## Consequences

**Positive:**

- Profiles can pin models on any configured provider while the session stays
  where it is; request dumps record the `provider` key for misroute debugging.
- A doomed delegation fails at `delegate` time with an actionable error
  (profile, model, unknown provider, known providers) instead of after launch.
- Model references are unambiguous by grammar: no layer depends on a
  first-match bare-name scan, so the same identifier resolves identically in
  the config, the router, and the delegation path; a mid-cycle regression
  review's ambiguity finding (an HF-style model name colliding with the
  provider prefix) is closed — `provider/qwen/qwen3-coder-30b` is now the
  EXACT spelling for such names.
- The validation surface is read-only and reuses the runtime miss predicate,
  so validation can never disagree with routing.
- Subagent compaction is calibrated to the model actually on the wire.

**Negative / trade-offs:**

- Provider-name-only validation lets a composite naming a real provider with a
  model that provider does not list still route (the provider answers) —
  deliberate per sp4rk ADR-011's caller-owns-pairing rule.
- Two validation layers (delegate-time + defensive re-check) must stay
  semantically in sync; both call the same injected validator, and the second
  layer is what keeps a mid-run profile edit from launching a doomed subagent.
- Renaming a provider entry silently breaks composite-pinned profiles — the
  same exposure composite `SetModel` ids already carry; the roster surfaces
  the pinned id verbatim so the mismatch is visible at discovery time.
- The judge and Model Profiles sampling remain keyed to the session even when
  a subagent runs another model — verdict-model alignment is traded for a
  single session-pinned policy surface (ADR-028).
- **Breaking change for existing hand-written references.** A bare
  `default_model`, a bare profile `model:`, or a bare per-message override
  from a config written before this rule fails loudly after the upgrade:
  config load rejects the file, delegate validation refuses the delegation,
  and the per-message path fails the request — every error carries the
  `"provider/model"` guidance. UI-written values were already composite
  (the pickers emit `provider/model`), so only hand-edited files are
  affected; the fix is mechanical (prefix the configured provider's name).

## Alternatives Considered

- **B2 — keep the single-field override and encode the provider inside
  `req.Model`** (`"provider/model"` parsed at the router/provider seam):
  rejected — it overloads the bare model string that is actually sent to the
  API, forces every provider seam and metadata lookup to strip the prefix, is
  ambiguous with model names that themselves contain `/`, and loses the
  explicit pinned-provider signal in request dumps; sp4rk ADR-011 rejected the
  same shape engine-side. The composite id lives in the PROFILE and is
  decomposed once at delegation time; only the bare name reaches `req.Model`.
- **B3 — build a per-delegation sub-router** pinned to the profile's
  provider/model: rejected — it duplicates provider state, retry/backoff,
  sampling and budget wiring per pinned caller, changes the caller identity so
  shared wrapping (token tracking, dump callers) must be rebuilt, and still
  cannot express "this one caller stays pinned" without extra per-caller
  state; rejected engine-side in sp4rk ADR-011 for the same reasons.
- **Fail bare overrides closed on a registry miss** (reject the delegation
  when no enabled provider lists the name): superseded mid-cycle by the
  MANDATORY-COMPOSITE rule. A bare identifier is now invalid BY GRAMMAR at
  every hand-written site — profile frontmatter, `llm.default_model`, the
  per-message override — instead of being tolerated and resolved by lookup
  order, so the miss path this alternative argued about no longer exists.
  The grammar rejection is stricter than this alternative (which only
  proposed failing on a registry miss) and keeps every error actionable:
  the message names the `"provider/model"` form. The resolution half of the
  original alternative (a resolvable bare name pins its provider) is moot
  for the same reason — a composite pins its named provider directly.

## Related

- [sp4rk ADR-011](https://github.com/v0lka/sp4rk/blob/main/specs/decisions/011-per-request-provider-override.md)
  — the engine primitive this decision wires into Subagent Profiles
  (`ChatRequest.Provider`, the provider-override caller, the read-only
  validation surface).
- [ADR-021](./021-subagents.md) — the Subagent Profile format and
  delegation-time application this extends (§2 `model:` field, §5
  application).
- [ADR-028](./028-session-pinned-judge.md) — the session-pinned judge that
  deliberately stays on the session model.
- [ADR-071](./071-adaptive-llm-request-budgets.md) — adaptive request budgets
  keying on the wire model.
- [../domains/orchestration/delegation.md](../domains/orchestration/delegation.md)
  — delegate validation order and profile application at launch.
- [../domains/llm-providers.md](../domains/llm-providers.md) — Model Override
  (c0wrk consumption) and context-window resolution tiers.
