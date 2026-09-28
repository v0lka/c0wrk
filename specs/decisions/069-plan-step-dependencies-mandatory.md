# ADR-069: Mandatory depends_on declaration on plan steps

## Status

Accepted

## Context

[ADR-047](047-plan-step-dependencies.md) made explicit ordering the default the
model is told to produce (Conductor guidance, tool description, router and
conductor specs), echoed the Kahn-layered execution waves on every successful
`declare_plan`, and printed the dependency edges in the approve-markdown — but
it deliberately refused to gate: no plan is rejected for a missing edge,
because a missing edge is an intent, not a fact any static rule can infer from
a plan that omits it.

In practice the prose has proved insufficient: Conductor runs still omit
`depends_on` outright — entire flat plans, including steps that consume a
predecessor's output, artifacts, or decisions. The non-blocking levers fire
only after the fact: the waves echo and the single-wave hint show the collapse
at declaration time, but nothing stops an under-specified plan from being
approved and executed concurrently.

There is a cheaper check ADR-047's refusal did not separate out: the field's
**presence**. The missing-edge problem is semantic (whether step B really
consumes step A's output), but the missing-field problem is syntactic — either
the model stated its dependency intent for a task, or it did not. Requiring
that statement for every task costs an empty array for a genuinely independent
step and converts "silently flat" into "explicitly flat", at which point the
existing levers (waves echo, single-wave hint, `Depends on: (none)` in the
approve-markdown) put the declaration in front of both the Conductor and the
approving human.

## Decision

Every `declare_plan` task MUST carry `depends_on`; a task with no dependencies
declares an explicit empty array `[]` — the plan contract's form of "None".
JSON `null` is not accepted: it unmarshals to the same nil slice as an omitted
field and is rejected with the `[]` fix. The schema keeps a plain
`"type": "array"` (no type unions) for broad provider compatibility with
strict function-calling stacks.

Enforcement is two-layered, so the rule holds on every path that can publish a
plan:

1. **Schema (registry Gate 1).** `depends_on` joins the task-level `required`
   list of `declare_plan`'s input schema, so `sdktools.ValidateToolInput`
   structurally rejects an omitted field before dispatch, naming the nested
   path (`tasks[2].depends_on`).
2. **Semantics (`validatePlanTasks`).** A task whose `DependsOn` is nil —
   absent field or `null` — is rejected with the 1-based task number and the
   corrective fix. The nil-sentinel is sound by `encoding/json` contract: an
   absent field and a JSON `null` both leave the slice nil, while an explicit
   `[]` unmarshals to a non-nil empty slice (pinned by
   `TestUnmarshalDependsOnNilSentinel`). Direct `tool.Execute` paths that skip
   Gate 1 are still covered by this layer.

Supporting changes: the Conductor guidance (`core/conductor.go`), the
`declare_plan` tool description and schema descriptions, and the router and
conductor specs state the mandatory rule. Everything downstream is unchanged:
reference resolution and cycle detection, the execution-wave echo, the
single-wave hint, and `SerializePlan`'s `Depends on:` rendering.
`execute_plan` and the plan-restore path never re-validate presence, so plans
persisted before this rule resume unaffected.

This supersedes ADR-047's "Refusal: no hard blocking" stance for the
field-presence case. ADR-047's semantic levers (execution-wave echo,
approve-markdown rendering), its over-declaring bias (a spurious edge only
serializes), and its refusals — no edge inference from step order, no
forced serialization — all stand.

## Consequences

**Positive:**

- A flat plan is now a deliberate declaration. The "tests ran concurrently
  with implement" failure mode requires the model to have asserted `[]` for
  the consuming step, and the single-wave hint plus the approve-markdown put
  that assertion in front of the human before approval.
- The gate fails fast at declaration with an actionable, retryable message;
  an invalid plan never reaches the filesystem, the blackboard, or the
  approval flow.
- Zero false positives: the check proves presence of a statement, not the
  statement's content, so a legitimately flat plan never gets rejected — it
  just has to say so.

**Negative:**

- A model that ignores the schema burns one retry on a validation error
  instead of silently publishing an under-specified plan.
- Genuinely flat plans carry boilerplate `[]` entries (cosmetic).
- The residual risk moves one level down: a declared-but-wrong `[]` still
  runs concurrently. Validation proves presence of intent, not correctness of
  intent — the waves echo, the single-wave hint, and the approve-markdown
  remain the levers for that.

## Alternatives Considered

- **Accept JSON `null` as the "none" form.** Rejected: `null` unmarshals to
  the same nil slice as an absent field, so accepting it would blur the very
  presence signal the gate reads; and a type-union schema
  (`["array", "null"]`) risks strict function-calling providers. The validator
  rejects `null` with a message that names `[]`.
- **Infer dependencies from step order.** Still rejected, per ADR-047:
  declaration order is a presentation choice, not a statement of data flow.
- **Keep ADR-047's purely non-blocking stance.** Rejected: observed
  field-omission rates made the prose-only regime inadequate; the presence
  check is cheap, precise, and unable to reject a correct plan.

## Related Specs

- [conductor-tools.md](../contracts/conductor-tools.md) — the `declare_plan`
  contract: mandatory `depends_on` in the input shape and the two enforcement
  layers in its behavioral notes.
- [conductor.md](../domains/orchestration/conductor.md) and
  [router.md](../domains/orchestration/router.md) — the Conductor guidance and
  routing policy carrying the mandatory-declaration rule.
- [ADR-047](047-plan-step-dependencies.md) — the superseded no-gate stance;
  its visibility levers remain in force.
- [ADR-012](012-conductor-orchestration-pipeline.md) — the Conductor-driven
  plan/execute pipeline this decision tunes.
