# ADR Template

Use this template when creating new Architecture Decision Records.

---

```markdown
# ADR-NNN: [Title]

## Status

Accepted

## Context

[The problem or question that required a decision. What forces are at play?]

## Decision

[What was decided. Be specific and actionable.]

## Consequences

[Positive and negative impacts on the codebase. What becomes easier? What becomes harder?]

## Alternatives Considered

[What was evaluated and why it was rejected. Brief rationale for each.]
```

---

## Numbering Rules

- Three-digit sequential number: 001, 002, 003...
- Never reuse a number (even if ADR is superseded)
- File name: `NNN-kebab-case-slug.md`

## Status Values

- `Accepted` — active decision, must be followed
- `Superseded by [NNN](./NNN-slug.md)` — replaced by newer decision
- `Superseded` (no successor ADR — the decision was reversed by code drift and recorded in place)

## Lifecycle

- ADRs with `Status: Accepted` are immutable once committed to the repository (no edits after acceptance); an uncommitted accepted ADR is an editable draft
- To change a committed decision: create a new ADR that supersedes the old one
- Update the old ADR's status to `Superseded by [NNN]`
- A committed accepted ADR is a record of the state at decision time: a path, identifier, or spec it names is not a live pointer, so its absence is not drift when the ADR names it as removed by the decision, when it belongs to another repository or an upstream dependency, or when its successor is named inline or through the `Status` chain
- The one addition a committed accepted ADR may still receive is a dated `## Historical Note` section recording the successor state of a path, identifier, or enumerated set it names that has since been renamed, moved, removed, or extended — location or membership only, never a restatement of the decision (see `specs/META.md`, Update Protocol rule 5)
