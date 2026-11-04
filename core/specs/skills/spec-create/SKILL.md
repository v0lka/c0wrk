---
name: spec-create
description: Create a new specification document following the project's spec system templates and conventions. Use when adding a new domain, component, contract, architecture doc, or Architecture Decision Record (ADR). Ensures correct format, naming, placement, cross-references, and INDEX.md update.
---

# Spec Create

Create a new specification document in the project's `specs/` system. This skill ensures the document follows the correct template, naming conventions, and is properly registered.

---

## Procedure

### Step 1 — Determine document type

Identify which of the 5 spec types you need to create:

| Type | When to use | Location |
|------|-------------|----------|
| **Domain README** | New conceptual domain (multiple components) | `specs/domains/<domain>/README.md` |
| **Domain Detail** | New component within an existing domain | `specs/domains/<domain>/<component>.md` |
| **Contract** | New boundary between layers/modules | `specs/contracts/<layer-a>-<layer-b>.md` |
| **Architecture** | New system-level concern (layers, security, flow) | `specs/architecture/<topic>.md` |
| **ADR** | Decision that passes the ADR bar test (see ADR-specific rules): constrains work beyond the immediate change AND had rejected alternatives, OR supersedes an existing ADR/invariant | `specs/decisions/NNN-slug.md` |

### Step 2 — Read META.md

Read `specs/META.md` to confirm the template for your chosen type. The templates below are generic — always prefer the project's META.md if it exists, as it may have project-specific additions.

### Step 3 — Apply naming conventions

- Files: `kebab-case.md`
- Domain directories: created only when a domain requires multiple files
- `README.md` inside a domain directory: overview and entry point
- `_template.md` prefix: template files (not actual specs)
- ADR files: `NNN-kebab-case-slug.md` (three-digit sequential number)

### Step 4 — Write the document using the correct template

---

## Templates

### Domain README

```markdown
# [Domain Name]

## Purpose

1-3 sentences. What this domain does in the system.

## Key Files

- `path/from/repo/root/file.ext` — role description

## Core Types

Key type definitions (code blocks) with brief explanations.

## Flow

ASCII diagram or numbered sequence showing the primary happy path.

## Invariants

Bullet list of properties that ALWAYS hold. Affirmative phrasing only.

## Configuration

Key parameters from configuration with defaults and valid values.

## Extension Points

How to add new behavior without breaking existing functionality.

## Related Specs

- [Spec Name](relative/path.md) — relationship context
```

### Domain Detail

```markdown
# [Component Name]

## Role

1 sentence: what this component does within its domain.

## Key Files

- `path/to/file.ext` — description

## Behavior

Detailed description. May include:
- State machines (ASCII)
- Decision tables
- Pseudocode
- Sequence diagrams

## Error Handling

How this component handles and propagates errors.

## Invariants

Properties that always hold for this component. Affirmative phrasing only.

## Related Specs

- [Spec Name](relative/path.md) — relationship context
```

### Contract

```markdown
# Contract: [Layer A] <-> [Layer B]

## Boundary Rule

One sentence: direction of dependency and what is NOT allowed.

## Interfaces

| Interface | Package | Consumed By | Purpose |
| --------- | ------- | ----------- | ------- |

## Initialization

How components are wired together at startup.

## Data Flow Across Boundary

What data crosses the boundary, in what form, in which direction.

## Error Propagation

Rules for wrapping/transforming errors at this boundary.

## Breaking Change Checklist

If you change X, you MUST also update Y.
```

### Architecture

```markdown
# [Topic]

## Context

Why this architectural aspect matters.

## [Main Content]

Diagrams, rules, descriptions. Structure varies by topic.

## Invariants

Architectural rules that must never be violated. Affirmative phrasing only.

## Anti-Patterns

What NOT to do, with brief explanation of why.
```

### ADR (Architecture Decision Record)

```markdown
# ADR-NNN: [Title]

## Status

Accepted

## Context

The problem or question that required a decision.

## Decision

What was decided.

## Consequences

Positive and negative impacts.

## Alternatives Considered

What was evaluated and why it was rejected.
```

**ADR-specific rules:**

**The ADR bar test** (the same criteria as `spec-init` Step 8):

Create an ADR only if the decision meets BOTH:
1. **Constrains work beyond the immediate change** — cross-domain, cross-cutting, or hard to reverse
2. **Had real alternatives considered and rejected** — a genuine choice existed, and the rejected options are worth remembering

OR meets this one alone:

3. **Contradicts or supersedes** an existing ADR or a documented invariant.

**Do NOT create an ADR for:**
- An implementation detail → record it as an invariant in the domain spec
- A local, cheaply reversible choice → record it in the domain spec
- Restating an existing spec or invariant → do nothing
- A decision with no genuine alternatives → record it in the domain spec

**`Alternatives Considered` is the bar.** If that section cannot be filled honestly, it is not an ADR — the decision belongs in a domain spec.

- Determine the next sequential number by scanning ALL files in `specs/decisions/` — including superseded ADRs. The scan is number-keyed: parse the leading `NNN` from each filename (`NNN-slug.md`) and ignore the slug; next number = highest parsed NNN + 1
- Numbers are NEVER reused (even for superseded ADRs)
- Under parallel branches, a freshly allocated number is provisional until that branch merges — a sibling branch may have allocated the same number in the meantime
- If a duplicate number reaches the tree at merge time, the second-landed ADR takes the next free number. When renumbering, update ALL of: the filename, the `# ADR-NNN: [Title]` heading, `Superseded by [NNN](./NNN-slug.md)` links in other ADRs, INDEX.md entries, and any cross-references to this ADR in other specs
- Once `Status: Accepted`, the ADR is immutable
- To change a decision: create a NEW ADR, then update the old one's Status to `Superseded by [NNN](./NNN-slug.md)` (this is the only allowed edit to an accepted ADR)

---

### Step 5 — Write cross-references

- Use relative paths from `specs/` directory
- Format: `[display text](relative/path.md)`
- Section anchors: lowercase, hyphen-separated: `[section](path.md#section-name)`
- Source code references: backtick path from repo root, e.g. `` `src/module/file.ext` ``

### Step 6 — Update INDEX.md

After creating the new spec file:
1. Add an entry to the "Navigation by Task" table (if applicable)
2. Add an entry to the "Directory Listing" section
3. Maintain alphabetical/logical ordering within sections

### Step 7 — Validate (embedded checklist)

Before declaring done, verify:

- [ ] All sections from the template are present and in correct order
- [ ] Cross-references point to existing files (verify with file reads)
- [ ] Paths in Key Files are accurate (verify files exist)
- [ ] Invariants are stated affirmatively ("X always does Y", NOT "X never does Z")
- [ ] INDEX.md has been updated to include the new file
- [ ] Naming follows conventions (kebab-case, correct directory)
- [ ] ADR only: no other file in `specs/decisions/` carries the same NNN number
- [ ] No filler prose — every sentence carries information
- [ ] ASCII diagrams used for flows (not Mermaid or other renderers)

---

## Content Principles

- **Agent-optimized**: predictable structure, explicit cross-references, no filler prose
- **Invariants are affirmative**: "The scheduler always processes tasks in priority order" (not "The scheduler should not skip high-priority tasks")
- **Key Files use repo-root paths**: `src/core/scheduler.go`, not `./scheduler.go`
- **Tables for catalogs**: interface catalogs, tool registries, config mappings, decision tables
- **ASCII for diagrams**: must work without a renderer
- **No generated content**: specs describe intended behavior; discrepancy with code = bug
