# Specification System

This document defines the format, rules, and procedures for creating and maintaining project specifications. It is the source of truth for how specs are structured.

## Purpose

Specifications provide AI coding agents with deterministic context about system behavior, interfaces, and architectural decisions. They enable agents to make safe, informed changes without extensive codebase exploration.

## Principles

1. **Self-standing documents** — specs are NOT generated from code. They describe intended behavior; a discrepancy between spec and code indicates a bug in the code (or a spec that needs updating).
2. **Agent-optimized** — predictable structure, explicit cross-references, no filler prose. Every section has a purpose.
3. **Living documents** — updated by agents on user request. Never silently drifting.
4. **Domain-based** — organized by conceptual domains, NOT by file/directory structure.
5. **Contracts are first-class** — cross-boundary interfaces deserve their own documents.
6. **Full-scope passes** — a declared cleanup or remediation pass (comments, specs, debt ledgers) covers its entire declared scope; a sweep over a subset of that scope is a spot-fix and is never billed as a pass.

## File Organization

```
specs/
├── META.md                         this file
├── INDEX.md                        navigation: task -> spec file(s)
│
├── architecture/                   system-level (layers, flows, security)
│   ├── layers.md
│   ├── data-flow.md
│   └── security-model.md
│
├── domains/                        by business domain (NOT file structure)
│   ├── frontend/                   (README.md = domain overview / entry point)
│   │   ├── README.md
│   │   ├── branch-list.md
│   │   ├── button-tooltips.md
│   │   ├── events.md
│   │   ├── fonts.md
│   │   ├── git-changes-list.md
│   │   ├── git-operation-console.md
│   │   ├── loading-states.md
│   │   ├── rendering.md
│   │   ├── row-actions.md
│   │   ├── sound-notifications.md
│   │   ├── stores.md
│   │   ├── system-notifications.md
│   │   ├── tabs.md
│   │   └── ui-scale.md
│   ├── memory/
│   │   ├── README.md
│   │   ├── blackboard.md
│   │   └── compaction.md
│   ├── orchestration/
│   │   ├── README.md
│   │   ├── conductor.md
│   │   ├── delegation.md
│   │   ├── executor.md
│   │   └── router.md
│   ├── tool-system/
│   │   ├── README.md
│   │   ├── builtins.md
│   │   └── mcp-gateway.md
│   ├── capabilities.md
│   ├── cli.md
│   ├── commands.md
│   ├── crash-logging.md
│   ├── e2s.md
│   ├── embedded-llm.md
│   ├── fonts.md
│   ├── git-auto-fetch.md
│   ├── goal-mode.md
│   ├── llm-providers.md
│   ├── model-profiles.md
│   ├── papers.md
│   ├── prompt-suggestions.md
│   ├── research.md
│   ├── review.md
│   ├── session-lifecycle.md
│   ├── session-worktrees.md
│   ├── specs.md
│   ├── terminal.md
│   ├── testing.md
│   ├── theming.md
│   ├── tool-manager.md
│   ├── verify-on-edit.md
│   ├── web-access.md
│   ├── webview-gpu-policy.md
│   └── workspace.md
│
├── contracts/                      interfaces between layers
│   ├── core-sp4rk.md
│   ├── backend-core.md
│   ├── desktop-frontend.md
│   ├── event-catalog.md
│   └── conductor-tools.md
│
└── decisions/                      Architecture Decision Records (NNN-slug.md; full list in INDEX.md)
    ├── _template.md
    ├── 001-single-module.md
    ├── 002-sp4rk-isolation.md
    ├── ...
    └── 118-kimi-subscription-model-catalog.md
```

## Naming Conventions

- Files: `kebab-case.md`
- Directories within `domains/`: created when a domain requires multiple files
- `README.md` inside a domain directory: overview and entry point for that domain
- `_template.md` prefix: template files (not actual specs)
- ADR files: `NNN-slug.md` (three-digit number, kebab-case slug)

## Document Formats

### Domain README (`domains/*/README.md` or `domains/*.md`)

Required sections in order. A spec may insert additional topic-specific sections between them; the required sections keep their relative order.

```markdown
# [Domain Name]

## Purpose

1-3 sentences. What this domain does in the system.

## Key Files

- `path/from/repo/root/file.go` - role description

## Core Types

Key type definitions (Go code blocks) with brief explanations.

## Flow

ASCII diagram or numbered sequence showing the primary happy path.

## Invariants

Bullet list of properties that ALWAYS hold. Use affirmative phrasing.

## Configuration

Key parameters from config.yaml with defaults and valid values.

## Extension Points

How to add new behavior without breaking existing functionality.

## Related Specs

- [link](relative/path.md) - context of relationship
```

### Domain Detail (`domains/*/<name>.md`)

For individual components within a domain:

```markdown
# [Component Name]

## Role

1 sentence: what this component does within its domain.

## Key Files

- `path/to/file.go` - description

## Behavior

Detailed description. May include:

- State machines (ASCII)
- Decision tables
- Pseudocode
- Sequence diagrams

## Error Handling

How this component handles and propagates errors.

## Invariants

Properties that always hold for this component.

## Related Specs

- [link](relative/path.md) - relationship context
```

### Contract (`contracts/*.md`)

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

### Architecture (`architecture/*.md`)

```markdown
# [Topic]

## Context

Why this architectural aspect matters.

## [Main Content]

Diagrams, rules, descriptions. Structure varies by topic.

## Invariants

Architectural rules that must never be violated.

## Anti-Patterns

What NOT to do, with brief explanation of why.

## Related Specs

- [link](relative/path.md) - relationship context
```

### ADR (`decisions/NNN-slug.md`)

```markdown
# ADR-NNN: [Title]

## Status

Accepted | Superseded by [NNN](./NNN-slug.md) | Superseded (no successor ADR — the decision was reversed by code drift and recorded in place)

## Context

The problem or question that required a decision.

## Decision

What was decided.

## Consequences

Positive and negative impacts on the codebase.

## Alternatives Considered

What was evaluated and why it was rejected.

## Historical Note

Optional, and the only section a committed accepted ADR may still gain. Dated
(`YYYY-MM-DD`). Lists paths, identifiers, or enumerated sets named above that
have since been renamed, moved, removed, or extended, each with its successor
state. Records location or membership only — it never restates or revises the
decision.
```

`## Alternatives Considered` is mandatory for every ADR created from this template.
Five records accepted before the section was mandated — ADR-008, ADR-009,
ADR-011, ADR-028 and ADR-087 — carry no such section. They are grandfathered:
a committed accepted ADR is immutable, so the section is never retrofitted into
them, and its absence from those five records is not a finding.

Optional trailing sections follow `## Alternatives Considered` in this order:

- `## Related` — the ADRs, specs and code this record interacts with. The
  spellings `## Related Specs` and `## References` are the same section under
  its earlier names; new records use `## Related`.
- `## Amendment (<scope>)` / `## Corrigendum (<date>)` — a dated, scoped
  correction or extension of the decision above. It states what changed and
  from when, and leaves the original Context and Decision prose intact.
- `## Historical Note` — as defined in the template above.

## Cross-References

- Always use relative paths from `specs/` directory
- Format: `[display text](relative/path.md)`
- For intra-file section references: `[display text](relative/path.md#section-name)` (lowercase, hyphens)
- When referencing source code: backtick path from repo root, e.g. `core/builder.go`
- **No line numbers** in code references. File paths combined with identifiers (function names, interface names, struct names, field/variable names) provide sufficient specificity. Line numbers are fragile — they drift with every code change and are impractical to keep current.

## Update Protocol

### When to update specs

- After any change that alters documented behavior
- After adding/removing/renaming interfaces that appear in a contract
- After changing architectural boundaries or invariants
- After making a new architectural decision (create ADR)

### How to update

1. Read the current spec fully before modifying
2. Preserve the document format (sections, ordering) defined in this META.md
3. Update cross-references if file paths changed
4. After adding or removing a spec file, update `INDEX.md`
5. ADRs with `Status: Accepted` are immutable once committed to the repository; an uncommitted accepted ADR is an editable draft, and a committed one is superseded by a new ADR. A committed accepted ADR is a **record of the state at decision time**, so a path, identifier, or spec it names is not a live pointer and its absence is not drift when the ADR names it as something the decision removes, when it belongs to another repository or an upstream dependency, or when its successor is named inline or through the `Status` supersession chain (the ADR-041 precedent). The single permitted edit besides the `Status` line is an appended **Historical Note**: a dated record of the successor state for a path, identifier, or enumerated set named above that has since been renamed, moved, removed, or extended. The note states the current location or membership only — it never rewords `Context`, `Decision`, `Consequences` or `Alternatives Considered`, and it never changes what was decided

### Validation checklist

- [ ] All sections from the template are present
- [ ] Cross-references point to existing files
- [ ] Code paths in Key Files are accurate
- [ ] Invariants are stated affirmatively
- [ ] INDEX.md reflects the current file set
