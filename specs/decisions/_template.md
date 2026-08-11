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

- ADRs with `Status: Accepted` are immutable (no edits after acceptance)
- To change a decision: create a new ADR that supersedes the old one
- Update the old ADR's status to `Superseded by [NNN]`
