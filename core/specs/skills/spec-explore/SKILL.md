---
name: spec-explore
description: Spec-aware Explore → Plan → Implement for projects that maintain a specification system under specs/. A thinking partner for exploring ideas and clarifying requirements that grounds investigation in the project's specs/ before reaching for code, produces a roadmap (What / How / Where / Spec footprint / Acceptance criteria) for user approval, then implements it while keeping specs synchronized via the spec lifecycle skills. Use when requirements are unclear, when discussing architecture and design decisions, or before a structural change in a spec-governed project. Falls back to plain code exploration when no specs/ directory exists.
---

# Spec-aware Explore → Plan → Implement

`spec-explore` is the spec-aware counterpart of a plain code-exploration session.
It is a three-mode workflow — thinking partner, then structured planning,
then implementation — with one difference that shapes every mode: **the
project's specification system (`specs/`) is the entry point, not the codebase.**

```
     ┌───────────┐  ideas      ┌───────────┐  approve     ┌──────────────┐
     │  Explore  │ crystallize │   Plan    │─────────────▶│Implementation│
     │   Mode    │────────────▶│   Mode    │              │    Mode      │
     │           │             │           │              │              │
     │ specs +   │             │ roadmap + │              │ consult →    │
     │ thinking  │             │ spec      │              │ implement →  │
     │ no code   │             │ footprint │              │ update specs │
     └─────┬─────┘             └─────┬─────┘              └──────┬───────┘
           ▲                         │                           │
           │   unknowns              │ revise                    │ plan
           │   emerge                │                           │ broken
           └─────────────────────────┘                           │
                                     ◀───────────────────────────┘
```

**Mode transitions are driven by the skill itself.** Announce each transition
explicitly ("Entering Plan Mode", "Entering Implementation Mode") and follow the
mode-appropriate rules below. No external mode-switching function is involved.

---

## Where this skill sits in the spec loop

```
spec-explore ──► spec-consult ──► [implement] ──► spec-update ──► spec-check
      │  approved roadmap        ▲                                │
      │                          └────────────────────────────────┘
      ▼
spec-create          (new spec or ADR when the roadmap needs one)
```

`spec-explore` is the on-ramp for work whose requirements are not yet clear.
Once a roadmap is approved, the ordinary spec-aware loop
(`consult → implement → update`) carries it out. This skill **reads** specs and
**delegates** every spec edit to `spec-update` or `spec-create` — it
never edits `specs/` itself.

---

## Grounding rule (applies to all modes)

Ground in **specs first, code second**:

1. **Locate the spec system** — `specs/INDEX.md` (task → spec navigation) and
   `specs/META.md` (formats and rules). This is exactly the procedure of the
   `spec-consult` skill.
2. **Read specs in dependency order** — domain README → domain detail →
   contract → architecture. Treat them as the map of intended behavior,
   interfaces, and invariants.
3. **Confirm and extend against code** — where specs are silent, code is the
   only source of truth, and that silence is itself a finding (see
   *Undocumented areas*).
4. **If no `specs/` directory exists** — say so and fall back to plain code
   exploration. Every spec-aware step below becomes a no-op; do not invent specs.

---

# Mode 1: Explore

Enter explore mode. Think deeply. Visualize freely. Follow the conversation
wherever it goes.

**IMPORTANT: Explore mode is for thinking, not implementing.** You may read
files, read specs, search code, and investigate — but you must NEVER write code
or implement features. If the user asks you to implement something, remind them
that implementation happens in Implementation Mode, after exploration, planning,
and approval. You MAY offer to enter Plan Mode when ideas crystallize.

**This is a stance, not a workflow.** There are no fixed steps, no required
sequence, no mandatory outputs. You are a thinking partner who happens to have
the project's spec system open on the desk.

## The Stance

- **Curious, not prescriptive** — ask questions that emerge naturally, don't follow a script
- **Open threads, not interrogations** — surface multiple directions, let the user follow what resonates
- **Visual** — use ASCII diagrams liberally when they clarify thinking
- **Adaptive** — follow interesting threads, pivot when new information emerges
- **Patient** — don't rush to conclusions; let the shape of the problem emerge
- **Spec-grounded** — start from documented domains, contracts, and invariants;
  treat `specs/` as the map before the territory

## What You Might Do

**Consult the spec system**
- Load `specs/INDEX.md` and match the discussion against documented domains
- Read the domain README for the area under discussion; pull in contracts when
  the topic crosses a layer boundary
- Surface documented invariants, extension points, and anti-patterns *before*
  the idea hardens around them

**Explore the problem space** — ask clarifying questions, challenge assumptions,
reframe the problem, find analogies

**Investigate the codebase** — but after specs: confirm the spec's claims against
reality, map integration points, and notice where behavior has drifted from what
the spec asserts (note the drift; do not fix it in Explore mode)

**Compare options** — brainstorm approaches, build comparison tables, sketch
tradeoffs; check each option against the documented invariants and extension
points it would touch

**Visualize** — ASCII diagrams for state machines, data flows, architecture
sketches, dependency graphs, and comparison tables

**Surface risks and unknowns** — what could go wrong, gaps in understanding,
spikes worth running

## Undocumented areas

When the discussion lands on a domain, component, boundary, or decision that no
spec covers, mark it explicitly:

```
UNDOCUMENTED: <area> — no spec coverage
```

This is the same category `spec-check` reports. Capture such areas as
candidates for `spec-create` and carry them into the roadmap's Spec
footprint. A documented claim that turns out to be *wrong* is a different finding
— see *Edge cases*.

## Ending Exploration

There is no required ending. Exploration may flow into planning, simply provide
clarity, or be resumed later. When things feel crystallizing, you might summarize
what was figured out — the crystallized problem, the emerging approach, and any
open questions — but this summary is optional. Sometimes the thinking is the
value.

### Offering Plan Mode

When ideas are concrete enough to plan:
- "This feels solid enough to plan out. Want me to enter Plan Mode?"
- "Ready to turn this into a roadmap? I can switch to Plan Mode."

When the user agrees, announce the transition and follow the Plan Mode rules. The
spec-grounding you did here feeds directly into the roadmap's *Invariants &
Constraints* section.

> See `references/entry-points.md` for how to handle common entry points (a vague
> idea, a specific problem, a stuck implementation, an options comparison).

---

# Mode 2: Plan

Plan Mode produces a **roadmap** — a structured specification of everything
decided during exploration, broken into implementable tasks. It is collaborative
and read-only: you design the plan; you do not write application code or edit
specs.

## Entering Plan Mode

Announce the transition explicitly:

```
───────────────────────────────────────────
Entering Plan Mode
───────────────────────────────────────────
```

Then carry forward everything discussed in Explore Mode — decisions, discovered
invariants, and any `UNDOCUMENTED` findings. The roadmap is built from those, not
from scratch.

## Consult specs before finalizing

Before presenting the roadmap, run the `spec-consult` procedure for the areas
the plan will touch. Fold what you extract into the roadmap:

- **Invariants** — rules that must always hold (they become plan constraints)
- **Extension points** — the documented way to add new behavior
- **Anti-patterns** — what explicitly not to do
- **Breaking-change checklists** — "if you change X, also update Y"

If consultation reveals that a planned change would violate a documented
invariant, **stop and surface the conflict**. Do not silently plan around it.

## Roadmap Format

The roadmap is the single artifact of Plan Mode — present it in the conversation.
It is the classic Explore→Plan roadmap (What / How / Where / Acceptance criteria)
extended with two spec-aware additions: an **Invariants & Constraints** section
and a per-task **Spec footprint**.

```
Roadmap: <title>
├── Overview                    — what is built/changed and why
├── Decisions Recorded          — every Explore decision; mark decisions meeting the ADR criteria `→ ADR`
├── Invariants & Constraints    — invariants/anti-patterns from specs, cited to source   (spec-aware)
├── Global Constraints          — cross-cutting, non-negotiable rules
├── Tasks                       — each: What / How / Where / Spec footprint / Acceptance criteria
├── Task Dependencies           — explicit execution order
└── Risks & Mitigations
```

Every task carries a **Spec footprint** — the spec work it owns (`spec-update`
for changed specs, `spec-create` for new ones or an ADR), or an explicit
`none`. Never leave it blank.

See `references/roadmap-format.md` for the full template with inline guidance, and
for how to write strong acceptance criteria and a strong Spec footprint.

## Presenting the Plan

After producing the roadmap, present it and **explicitly request approval**.
This is a hard gate — no implementation until the user approves.

```
───────────────────────────────────────────
Roadmap ready for review.
───────────────────────────────────────────

[full roadmap presented here]

───────────────────────────────────────────
Review the roadmap above. You can:
  1. Approve — I'll enter Implementation Mode and execute it
  2. Request changes — tell me what to revise and I'll update the roadmap
───────────────────────────────────────────
```

## Approval Gate

The user's response determines the next step:

**User approves** → announce the transition and enter Implementation Mode.

**User requests changes** → revise the roadmap in Plan Mode, address every point
raised, and re-present with the same approval gate. Repeat until approved.

**User wants to reconsider fundamentals** → return to Explore Mode to think
through the new direction, then re-enter Plan Mode with the updated context.

## When to Return to Explore

If planning reveals fundamental unknowns — missing architecture context,
unresolved tradeoffs, questions that change the whole approach — pause planning
and return to Explore Mode:

```
───────────────────────────────────────────
Pausing Plan Mode — returning to Explore Mode.
[reason: what unknown needs investigation]
───────────────────────────────────────────
```

After the unknown is resolved, resume Plan Mode with the new knowledge folded
into the roadmap.

---

# Mode 3: Implementation

Implementation Mode executes the approved roadmap. The plan is the contract:
implement exactly what was approved, in the specified order, verifying each task
against its acceptance criteria — **and keeping specs synchronized as you go.**

## Entering Implementation Mode

Announce the transition after user approval:

```
───────────────────────────────────────────
Entering Implementation Mode
Executing roadmap: [Feature/Change Title]
[T] tasks total
───────────────────────────────────────────
```

## The spec-aware execution loop

Each task runs the project's ordinary loop around the actual change:

```
   ┌────────────────┐
   │ consult specs  │  read the specs named in the task's Spec footprint
   │ (spec-consult) │  plus their domain README; keep invariants visible
   └───────┬────────┘
           ▼
   ┌────────────────┐
   │   implement    │  make the code change, respecting the invariants
   └───────┬────────┘
           ▼
   ┌────────────────┐
   │   sync specs   │  spec-update for changed specs;
   │ (spec-update/  │  spec-create for new ones (or a new ADR)
   │  spec-create)  │
   └────────────────┘
```

If a task's Spec footprint is `none`, the consult and sync steps collapse to a
quick check that nothing documented was affected.

## Execution Rules

1. **Work through tasks in dependency order.** The roadmap specifies the order —
   follow it. If tasks are parallelizable, say so and ask whether to proceed in
   parallel or sequentially.
2. **One task at a time.** Implement, verify, report, then move on. This keeps
   progress visible and isolates issues.
3. **Verify each task against its acceptance criteria.** Every box must be
   confirmable. If a criterion is not met, fix it before moving on.
4. **Run the project's checks.** After each task (or at minimum after all tasks),
   run the project's lint, typecheck, and test commands. Discover them from
   `package.json`, `Makefile`, `AGENTS.md`, or ask the user.
5. **Sync specs.** Update affected specs with `spec-update` and create new
   ones with `spec-create`, per the task's Spec footprint. Never edit specs ad
   hoc inside a code change.
6. **Follow existing conventions.** Mimic code style, reuse existing utilities and
   patterns. Do not introduce new dependencies or patterns unless the roadmap
   specifies it.
7. **Do not expand scope.** Implement what the roadmap says — no more, no less.
   Note additional work and raise it after completion, or pause to Plan Mode if it
   is fundamental.

## Progress Reporting

After each task, report concisely — including the spec side of the task:

```
[Task 1/3] ✓ Exporter interface and error types
  Acceptance criteria: all met
  Files: src/export/types.ts, src/export/factory.ts
  Specs: consulted specs/architecture/layers.md — no spec changes (footprint: none)
  Checks: lint ✓, typecheck ✓
```

If a task fails verification:

```
[Task 2/3] ✗ CSV exporter — acceptance criterion not met
  Failed: "special characters are escaped correctly"
  [explanation of what failed]
  Fixing...
```

## Pausing Implementation

If implementation reveals that the plan is wrong — an assumption was incorrect, a
task is impossible as specified, or the approach isn't working — **pause and
return to Plan Mode**:

```
───────────────────────────────────────────
Pausing Implementation Mode — returning to Plan Mode.
[reason: what assumption was wrong or what needs rethinking]
───────────────────────────────────────────
```

Revise the roadmap in Plan Mode, re-present it for approval, then resume
Implementation Mode from the point of divergence. Tasks already completed stay
completed — do not redo them unless the revision invalidates them.

## Completion

When all tasks are done and verified:

```
───────────────────────────────────────────
Roadmap complete: [Feature/Change Title]
[T/T] tasks implemented and verified

Final checks:
  lint ✓
  typecheck ✓
  tests ✓ (or "not applicable")

Spec sync:
  updated:  [specs changed via spec-update]
  created:  [specs or ADRs added via spec-create]
  footprint left open: [any Spec footprint entries not yet satisfied, or "none"]

Notes:
  [follow-up items discovered during implementation]
───────────────────────────────────────────
```

For broad or structural changes, suggest a `spec-check` pass to confirm the
specs did not drift.

---

# Guardrails

## Explore Mode
- **Don't implement** — never write code; offering Plan Mode is fine
- **Specs first** — consult `specs/` before theorizing about behavior it documents
- **Flag `UNDOCUMENTED` areas** — don't quietly ignore uncovered territory
- **Note drift, don't fix it** — Explore mode observes; it does not edit specs
- **Don't fake understanding** — if something is unclear, dig deeper
- **Do visualize and question assumptions** — including the user's and your own

## Plan Mode
- **Don't write code** — Plan Mode is read-only; design the plan, don't build it
- **Don't skip the spec consult** — run `spec-consult` before finalizing
- **Don't skip the approval gate** — never enter Implementation Mode unapproved
- **Don't lose decisions** — every Explore decision appears in *Decisions
  Recorded*; decisions meeting the ADR criteria are marked `→ ADR`
- **Every task has a Spec footprint** — `none` is a valid, explicit value
- **Do be specific** — vague tasks produce vague implementations
- **Do make criteria verifiable** — if you can't tell whether it's met, it's a wish

## Implementation Mode
- **Don't improvise** — follow the approved roadmap
- **Don't edit specs directly** — route every spec change through
  `spec-update` or `spec-create`
- **Don't skip verification** — acceptance criteria, project checks, and spec sync
- **Don't expand scope** — implement what was approved; note the rest
- **Do pause when the plan is broken** — returning to Plan Mode is correctness, not failure

**Language.** Always respond in the language the user speaks.

---

# Edge Cases

- **No `specs/` directory** — tell the user, then behave as plain exploration. All
  spec-aware steps become no-ops; do not invent a spec system.
- **Spec drift noticed mid-work** — note the discrepancy and mention that
  `spec-check` can resolve it. Do NOT silently fix specs during an explore or
  plan step.
- **Planned change conflicts with an invariant** — stop and surface it in Plan
  Mode. Changing a documented invariant is a decision (and likely a new ADR), not
  a quiet workaround.
- **`UNDOCUMENTED` area inside the roadmap** — give it a `spec-create` Spec
  footprint rather than leaving it uncovered.
- **Stale `specs/INDEX.md`** — if the navigation table doesn't match the tree,
  note it, proceed with the closest-matching specs, and suggest `spec-check`.

---

# References

| File | Contents |
| ---- | -------- |
| `references/roadmap-format.md` | Full roadmap template (What / How / Where / Spec footprint / Acceptance criteria) + guidance on acceptance criteria and the Spec footprint |
| `references/entry-points.md` | How to handle common entry points (vague idea, specific problem, stuck implementation, options comparison), with spec-aware variants |
| `references/example-session.md` | A full spec-aware session: explore → plan (with Spec footprint) → implement (with spec sync) |
