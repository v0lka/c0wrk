# Specification System

This document defines the format, rules, and procedures for creating and maintaining sp4rk specifications. It is the source of truth for how specs are structured.

## Purpose

Specifications provide AI coding agents with deterministic context about sp4rk's behavior, interfaces, and architectural decisions. They enable agents to make safe, informed changes to the engine without extensive codebase exploration.

## Principles

1. **Self-standing documents** — specs are NOT generated from code. They describe intended behavior; a discrepancy between spec and code indicates a bug in the code (or a spec that needs updating).
2. **Agent-optimized** — predictable structure, explicit cross-references, no filler prose. Every section has a purpose.
3. **Living documents** — updated by agents on user request. Never silently drifting.
4. **Domain-based** — organized by conceptual domains, NOT by file/directory structure.
5. **Contracts are first-class** — cross-boundary interfaces deserve their own documents.
6. **Engine, not application** — sp4rk is a reusable agent-execution engine. Specs describe engine primitives and the contracts a host application must satisfy; they never assume a specific embedding application.

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
├── domains/                        by conceptual domain (NOT file structure)
│   ├── orchestration/
│   │   ├── README.md               domain overview (entry point)
│   │   ├── conductor.md
│   │   ├── executor.md
│   │   ├── router.md
│   │   ├── planner.md
│   │   ├── reflector.md
│   │   └── subagents.md
│   ├── tool-system/
│   │   ├── README.md
│   │   ├── builtins.md
│   │   └── mcp-gateway.md
│   ├── memory/
│   │   ├── README.md
│   │   ├── compaction.md
│   │   └── blackboard.md
│   ├── llm-providers.md
│   ├── skills.md
│   ├── prompt-building.md
│   └── embedding.md
│
├── contracts/                      interfaces between engine and host application
│   ├── agent-execution.md
│   ├── llm-providers.md
│   └── tools.md
│
└── decisions/                      Architecture Decision Records
    ├── _template.md
    └── 00N-slug.md
```

## Naming Conventions

- Files: `kebab-case.md`
- Directories within `domains/`: created when a domain requires multiple files
- `README.md` inside a domain directory: overview and entry point for that domain
- `_template.md` prefix: template files (not actual specs)
- ADR files: `NNN-slug.md` (three-digit number, kebab-case slug)
- ADR numbering is local to the sp4rk spec set. sp4rk-native decisions are numbered `00N` independently from any host application's decision log.

## Document Formats

### Domain README (`domains/*/README.md` or `domains/*.md`)

Required sections in order:

```markdown
# [Domain Name]

## Purpose

1-3 sentences. What this domain does in the engine.

## Key Files

- `github.com/v0lka/sp4rk/<pkg>/file.go` - role description

## Core Types

Key type definitions (Go code blocks) with brief explanations.

## Flow

ASCII diagram or numbered sequence showing the primary happy path.

## Invariants

Bullet list of properties that ALWAYS hold. Use affirmative phrasing.

## Configuration

Key parameters with defaults and valid values.

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

- `github.com/v0lka/sp4rk/<pkg>/file.go` - description

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

How components are wired together.

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

Accepted | Superseded by [NNN](./NNN-slug.md)

## Context

The problem or question that required a decision.

## Decision

What was decided.

## Consequences

Positive and negative impacts on the codebase.

## Alternatives Considered

What was evaluated and why it was rejected.
```

## Cross-References

- Always use relative paths from the `specs/` directory
- Format: `[display text](relative/path.md)`
- For intra-file section references: `[display text](relative/path.md#section-name)` (lowercase, hyphens)
- When referencing source code: use the module import path, e.g. `github.com/v0lka/sp4rk/<pkg>` (optionally refined with the file or identifier name)
- **No line numbers** in code references. Package paths combined with identifiers (function names, interface names, struct names, field/variable names) provide sufficient specificity. Line numbers are fragile — they drift with every code change and are impractical to keep current.

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
4. After adding or removing a spec file, update [INDEX.md](INDEX.md)
5. ADRs with `Status: Accepted` are immutable; create a new ADR to supersede

### Validation checklist

- [ ] All sections from the template are present
- [ ] Cross-references point to existing files (or files planned to exist in this spec set)
- [ ] Code paths in Key Files use `github.com/v0lka/sp4rk/<pkg>` and are accurate
- [ ] Invariants are stated affirmatively
- [ ] INDEX.md reflects the current file set
- [ ] No references to a specific host application — sp4rk specs stay engine-generic
