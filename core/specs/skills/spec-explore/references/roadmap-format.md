# Roadmap Format

Full template for the roadmap produced in Plan Mode, plus guidance on
the two things that decide whether a roadmap is implementable: acceptance
criteria and the per-task Spec footprint.

Present the roadmap directly in the conversation using this structure:

```markdown
# Roadmap: [Feature/Change Title]

## Overview
[Exhaustive description of what is being built or changed and why. Synthesize
the problem statement, the chosen approach, and the rationale into a coherent
narrative. A developer reading only this section should understand what they're
building and why.]

## Decisions Recorded
[Every decision from Explore Mode that shapes this plan. Each is a commitment,
not a question — this prevents decisions from being lost between exploration and
implementation.]

- Decision 1: [what was decided and why]
- Decision 2: [what was decided and why]
- ...

[Mark with `→ ADR` any decision that meets the ADR bar: it constrains work beyond
the immediate change AND had real alternatives considered and rejected, OR it
contradicts/supersedes an existing ADR or documented invariant. Such decisions
become Architecture Decision Records via `spec-create` after approval;
everything else stays in Decisions Recorded (and, if worth recording, belongs in
a domain/architecture spec).]

## Invariants & Constraints (from specs)
[Invariants, extension points, and anti-patterns gathered from the spec system.
These are not negotiable — implementation must respect them. Cite the source spec
so each can be re-checked.]

- [Invariant that always holds] — source: `specs/domains/<domain>/README.md`
- [Anti-pattern to avoid] — source: `specs/architecture/<topic>.md`

[Omit this section only if the project has no specs or none are relevant.]

## Global Constraints
[Cross-cutting constraints: performance budgets, compatibility requirements,
architectural rules, coding conventions. Omit if none.]

## Tasks

### Task 1: [Short, actionable title]
**What:** [The concrete outcome — what this task accomplishes. One to two
sentences.]
**How:** [The approach — techniques, patterns, libraries, key steps. Enough to
guide implementation without prescribing every line.]
**Where:** [Files, modules, packages created or modified. Repo-root-relative
paths.]
**Spec footprint:** [The spec-side work this task owns and how — e.g.
"update `specs/contracts/api-db.md` (new method)", "create
`specs/domains/export/README.md` and a detail file", "new ADR", or "none".]
**Acceptance criteria:**
- [ ] [Specific, verifiable criterion — pass or fail, no ambiguity]
- [ ] [Specific, verifiable criterion]

### Task 2: [Short, actionable title]
**What:** ...
**How:** ...
**Where:** ...
**Spec footprint:** ...
**Acceptance criteria:**
- [ ] ...

[... additional tasks ...]

## Task Dependencies
[Which tasks depend on others. Use an ordered list, or a small diagram if
parallelization is possible. Make the execution order explicit.]

## Risks & Mitigations
[Known risks and how they'll be handled. Omit if the plan is low-risk.]
```

### Writing Good Acceptance Criteria

Acceptance criteria are the contract between Plan Mode and Implementation Mode —
they determine when a task is done.

- **Specific** — "Returns 200 with a JSON body matching schema X", not "works correctly"
- **Verifiable** — can be tested, checked, or unambiguously confirmed
- **Binary** — either met or not; no "mostly done"
- **Behavioral** — describes what the system does, not how the code looks

### Writing a Good Spec footprint

The Spec footprint is this skill's addition to the roadmap format. It names the
spec work a task owns so that implementation does not leave specs behind.

- **Every task has one** — use `none` explicitly when a task touches nothing
  documented; never leave it blank.
- **Point at the skill, not just the file** — "update via `spec-update`" or
  "create via `spec-create`".
- **Carry over `UNDOCUMENTED` findings** — an area flagged during exploration
  becomes a `spec-create` footprint entry.
- **Surface ADRs** — decisions meeting the ADR bar, marked `→ ADR`, become
  `spec-create` footprints too.
