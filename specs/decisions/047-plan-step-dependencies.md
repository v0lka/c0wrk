# ADR-047: Plan Step Dependencies (explicit ordering, wave echo, dependency rendering)

## Status

Accepted

## Context

The Conductor declares a plan through the `declare_plan` tool: an ordered list of
steps, each with an optional `depends_on` edge
([conductor-tools.md](../contracts/conductor-tools.md)). `execute_plan` then runs
that DAG with parallelism — it repeatedly dispatches the steps whose
prerequisites have all completed, so **a step that carries no `depends_on` runs
CONCURRENTLY with its siblings**. The edge is therefore load-bearing: it is the
only thing that serializes a step behind the output, artifact, or decision it
consumes.

Two forces made the previous behavior wrong in opposite directions:

1. **The framing nudged models toward a flat plan.** The Conductor guidance
   (`core/conductor.go`), the router policy
   ([router.md](../domains/orchestration/router.md)) and
   [conductor.md](../domains/orchestration/conductor.md) all described a
   non-trivial task as one that "decomposes into a **DAG of independent
   steps**", and the tool description summarized `depends_on` as "independent
   steps run in parallel". Read literally, that rewards the *absence* of edges:
   a model that treats every step as "independent" omits the edge between
   "write the failing tests" and "implement", the two steps fan out
   concurrently, and the run fails nondeterministically. A missing `depends_on`
   is a correctness bug, but the prose made it look like the default state.

2. **The approval gate hid the ordering.** In `await_approval` mode the user
   signs off on the plan rendered by `SerializePlan`
   ([plan_serializer.go](../../core/plan_serializer.go)) — the markdown that is
   written to the session plan file and handed to the approval callback. That
   rendering printed each step's id, summary, and description but **not** its
   `depends_on`, so the human reviewer could not see which steps were meant to
   run in sequence. The gate that exists to catch a bad plan was blind to the
   single field that decides whether the plan is safe to parallelize.

The decision below fixes both without adding a blocker: it makes explicit
ordering the default the model is told to produce, makes the resulting
concurrency visible (`declare_plan` echoes the execution waves) and makes the
dependency edges visible where the human approves them (the plan markdown).

## Decision

### Lever A — explicit order is the default; a missing edge is named an error

Remove the "DAG of independent steps" framing everywhere it appears and replace
it with an explicit obligation. The Conductor guidance, the router policy, the
Conductor heuristics table, and the `declare_plan` tool description must all
state that **`depends_on` must be declared whenever a step consumes another
step's output, artifacts, or decisions**, that a step with no `depends_on` runs
concurrently with its siblings, and that an omitted link is a correctness bug —
not a harmless omission. The rule is deliberately biased toward over-declaring:
a spurious edge only serializes (a benign cost), whereas a missing edge runs the
steps in parallel (a correctness failure). Edit sites: `core/conductor.go`
(`conductorGuidanceForComplexity` and the complexity ≥ 2 guidance banner),
`core/tools/declare_plan.go` (`toolDeclarePlanDescription`, including a
flat-plan anti-example), [router.md](../domains/orchestration/router.md) and
[conductor.md](../domains/orchestration/conductor.md).

### Lever B — echo the execution waves; hint when a multi-step plan collapses to one wave

`declare_plan` computes the plan's **execution waves** by Kahn-layering the
validated `depends_on` DAG (`planExecutionWaves`): wave 1 holds every
dependency-free step, wave N+1 holds the steps whose prerequisites all completed
in waves ≤ N, and steps inside a wave keep their declaration order. Acyclicity
is already guaranteed by `validatePlanTasks`, so the layering is always total.
`formatExecutionWaves` renders the echo
`Execution waves: 1=[step_1, step_3] · 2=[step_2] · 3=[step_4]`.

- **Every successful call** (both modes) appends the wave echo to the tool
  result, so the Conductor sees the under-specified graph *before* `execute_plan`
  fans the steps into concurrency.
- **`mode == "present"` only:** when a **multi-step** plan collapses into a
  **single wave** (`len(waves) == 1 && len(tasks) > 1` — every step
  dependency-free, all run concurrently) the result additionally carries a
  **non-blocking** hint (`IsError: false`): *"All N steps are in a single
  parallel wave — every step will run concurrently. If any step consumes
  another's output, re-declare with depends_on before executing."*
- The hint never fires for a one-step plan and is **suppressed in
  `await_approval`** — the user is already reviewing the plan there and only the
  wave echo is surfaced, on approval. The validation-error, continuation-hint,
  `request_changes`, and `abandon` paths are unchanged.

### Lever C — render dependencies in the approve-markdown

`SerializePlan` now prints a `Depends on:` line directly under each
`# Step N (id): summary` heading — the concrete ids when the step depends on
other steps, and the literal `Depends on: (none)` when it does not. The same
markdown feeds both the session plan file (`os.WriteFile`) and the approval
payload (`LastPlanMarkdown()`), so the human reviewing an `await_approval` plan
sees the ordering the Conductor declared. Dependencies are trimmed and empty
entries filtered before rendering. Edit site:
[plan_serializer.go](../../core/plan_serializer.go).

### Refusal: no hard blocking

The levers are **informational and are the default**, not a gate. c0wrk does
**not** reject a plan, block approval, or refuse `execute_plan` because an edge
is missing. A legitimate flat plan (genuinely independent steps) is valid and
common, and no static check can distinguish it from an under-specified one —
`depends_on` is an intent the model must state, not a fact c0wrk can infer. A
blocker would therefore reject correct plans (false positives) and force the
model into spurious dependencies to get past it, trading a visible correctness
signal for a hidden one. Instead: bias the model toward declaring edges (A),
surface the concurrency that a flat plan produces (B), and put the edges in
front of the approving human (C) — all non-blocking.

## Consequences

**Positive:**

- The guidance no longer rewards a flat plan; a model is told that omitting
  `depends_on` is a correctness bug, which directly targets the
  "tests/implement ran concurrently" failure mode.
- The Conductor sees the execution waves on every `declare_plan`, so a plan
  that collapsed into one wave is visible at declaration time rather than as a
  nondeterministic failure during `execute_plan`.
- The approval gate is no longer blind: the reviewer sees each step's
  `Depends on:` edges before approving, and `Depends on: (none)` makes an
  under-specified step conspicuous.
- No new failure surface: every lever is non-blocking, so a legitimate flat plan
  still proceeds; the hint is advisory and only in `present` mode.

**Negative:**

- The obligations are prose (guidance, tool description, specs), enforced by the
  model's compliance, not by a checker — a model can still declare a flat plan
  and the steps will still run concurrently.
- `declare_plan` descriptions are bounded by the guard test
  (`core/tools/descriptions_guard_test.go`, 1200-character limit); landing the
  new rule required trimming unrelated prose from `toolDeclarePlanDescription`,
  so the tool's description surface is now tighter.
- The wave echo and hint add lines to the tool result seen by the Conductor,
  which costs a small amount of context on every plan declaration.

## Alternatives Considered

- **Hard blocking: reject or refuse to execute a plan with a missing edge.**
  Rejected. A missing edge is indistinguishable from a legitimately independent
  step by any static rule, so the blocker would reject correct flat plans and
  push models to invent spurious `depends_on` edges just to pass — replacing a
  visible "these steps will run in parallel" signal with a hidden one. The
  decision deliberately fixes the framing and the visibility instead of adding a
  gate.
- **Infer dependencies from step order.** Rejected. Declaration order is a
  presentation choice, not a statement of data flow; inferring edges from it
  would silently serialize plans the author intended to parallelize and would
  make the plan's behavior differ from what its author declared.
- **Serialize all steps (no parallelism).** Rejected. It would discard the
  `execute_plan` DAG parallelism that the engine is built around and slow every
  genuinely independent plan to hide a problem that visibility already solves.
- **Status quo (do nothing).** Rejected. The "independent steps" framing
  actively induced the missing-edge bug, and the approval markdown withheld the
  edges from the reviewer; both are cheap to fix without a gate.

## Related Specs

- [conductor-tools.md](../contracts/conductor-tools.md) — the `declare_plan`
  contract: input schema (`depends_on`), the execution-wave echo, and the
  single-wave hint in its flow diagram and behavioral notes.
- [conductor.md](../domains/orchestration/conductor.md) and
  [router.md](../domains/orchestration/router.md) — the Conductor guidance and
  routing policy that carry the "declare `depends_on` or the steps run
  concurrently" obligation.
- [plan_serializer.go](../../core/plan_serializer.go) — the approve-markdown
  rendering that now prints `Depends on:` per step.
- [ADR-012](012-conductor-orchestration-pipeline.md) — the Conductor-driven
  plan/execute pipeline this decision tunes.
- [delegation.md](../domains/orchestration/delegation.md) — the DAG (delegation)
  mechanism that parallels plan-step DAG order.
