# ADR Template

Use this template when creating new Architecture Decision Records for the sp4rk Agent SDK.

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

[Positive and negative impacts on the framework. What becomes easier? What becomes harder?]

## Alternatives Considered

[What was evaluated and why it was rejected. Brief rationale for each.]
```

---

## Numbering Rules

- Three-digit sequential number: 001, 002, 003...
- Never reuse a number (even if an ADR is superseded)
- File name: `NNN-kebab-case-slug.md`
- Numbering is local to the sp4rk spec set (independent of any host application's ADR sequence)

## Status Values

- `Accepted` — active decision, must be followed
- `Superseded by [NNN](./NNN-slug.md)` — replaced by a newer decision

## Lifecycle

- ADRs with `Status: Accepted` are immutable (no edits after acceptance)
- To change a decision: create a new ADR that supersedes the old one
- Update the old ADR's status to `Superseded by [NNN]`

## Related

Optional section for cross-references to other specs or ADRs:

```markdown
## Related

- [ADR-NNN](./NNN-slug.md) — context of relationship
- [spec](../domains/<area>/<file>.md) — context of relationship
```
