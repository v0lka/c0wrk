---
name: spec-init
description: >-
  Initialize a specification system for a project from scratch. Analyzes the
  codebase to identify architectural layers, domains, boundaries, and key
  decisions, then generates the initial set of specs (META.md, INDEX.md,
  domain specs, contracts, architecture docs, ADR template). Use when
  bootstrapping specs for a new project, setting up documentation governance,
  or creating a spec system for an existing codebase that has none.
---

# Spec System Initialization

Bootstrap a complete specification system for a project by analyzing its codebase and generating an initial set of structured, agent-optimized spec documents.

## Context

A spec system gives AI agents deterministic context about intended behavior, interfaces, and architectural decisions — enabling safe changes without full codebase exploration. This skill creates the system from scratch, tailored to the project's actual structure.

## Prerequisites

- The project must have an existing codebase (not an empty repo)
- No `specs/` directory should exist yet (or user confirms overwrite)
- The agent needs to understand the project's language(s), module system, and directory structure

---

## Procedure

### Step 1 — Confirm scope and location

Ask the user:
1. Where should the specs live? (default: `specs/` at repository root)
2. Is there an existing `AGENTS.md` or similar file to add a pointer to specs?
3. Are there specific areas they want prioritized or skipped?

If `specs/` already exists, warn the user and ask whether to overwrite or abort.

### Step 2 — Analyze the codebase

Perform a systematic analysis to identify:

**Layers / Modules:**
- Top-level directories and their responsibilities
- Import/dependency direction between layers
- Entry points and initialization flow

**Domains:**
- Conceptual areas of functionality (NOT mirroring file structure)
- Key types, interfaces, and data structures per domain
- Primary flows (happy paths)

**Boundaries:**
- Where layers interact (interfaces, RPC, events, shared types)
- Dependency direction rules (who imports whom)
- Data transformation at boundaries

**Architectural decisions already made:**
- Technology choices evident from dependencies (DB, frameworks, protocols)
- Patterns in use (event-driven, layered, hexagonal, etc.)
- Constraints from the build system or module structure

**Configuration:**
- Config file format and location
- Key parameters and their domains

Document your findings as working notes before proceeding to generation.

### Step 3 — Design the spec structure

Based on analysis, design the directory structure:

```
specs/
├── META.md
├── INDEX.md
│
├── architecture/
│   └── (identified system-level concerns)
│
├── domains/
│   ├── <domain-a>/
│   │   ├── README.md
│   │   └── <component>.md (if domain has multiple components)
│   └── <domain-b>.md (if single-file domain)
│
├── contracts/
│   └── <layer-a>-<layer-b>.md (for each identified boundary)
│
└── decisions/
    └── _template.md
```

Present the proposed structure to the user for approval before generating files.

### Step 4 — Generate META.md

Use the template from `assets/meta-template.md` as the base. Adapt the "File Organization" section to reflect the actual structure designed in Step 3.

META.md is the foundational document — all other specs reference it for templates and rules. Generate it first.

### Step 5 — Generate architecture specs

For each identified system-level concern, create a spec in `architecture/`:

Common topics (generate only those relevant to the project):
- **layers.md** — layer hierarchy, import rules, responsibilities
- **data-flow.md** — request lifecycle, event flow
- **security-model.md** — auth, authorization, policy enforcement

Follow the Architecture template from META.md. Include:
- ASCII dependency diagram
- Import rules as invariants
- Anti-patterns specific to this project's architecture

### Step 6 — Generate domain specs

For each identified domain:

**If domain has multiple components** → create a directory:
- `README.md` (Domain README template) — overview, key files, core types, flow, invariants
- `<component>.md` (Domain Detail template) — one per significant component

**If domain is simple** → create a single file:
- `<domain>.md` (Domain README template)

**Content generation rules:**
- Key Files: verify each path exists before including
- Core Types: extract actual type definitions from code (code blocks)
- Flow: trace the actual happy path through code and render as ASCII
- Invariants: derive from code patterns, tests, and comments (state affirmatively)
- Configuration: extract from actual config files/structs

### Step 7 — Generate contract specs

For each identified boundary between layers/modules:
- One contract file per boundary
- Include actual interface definitions from code
- Document initialization (how things are wired at startup)
- Derive "Breaking Change Checklist" from observed coupling

### Step 8 — Generate ADR template and initial ADRs

Create `decisions/_template.md` from `assets/adr-template.md`.

Generate ADRs only for decisions that pass the ADR bar — the same test `spec-create` applies:
- **Constrains work beyond the immediate change** (cross-domain, cross-cutting, or hard to reverse) **AND had real alternatives considered and rejected**
- If `Alternatives Considered` cannot be filled honestly, it is not an ADR — record the decision in the relevant domain or architecture spec instead
- At bootstrap the supersedes branch of the bar cannot apply — the spec tree starts empty

Common candidates:
- Module structure choice (monorepo vs multi-repo, single module vs workspace)
- Database/storage choice
- Key framework or library selection
- Build system or deployment architecture

### Step 9 — Generate INDEX.md

Create the navigation file with:
1. **Task → Spec table**: map common development tasks to spec files
2. **Dependency graph**: ASCII diagram of layer relationships
3. **Directory listing**: all spec files organized by section

### Step 10 — Update AGENTS.md (if exists)

If the project has an `AGENTS.md` or equivalent agent instructions file, add a pointer:

```markdown
## Specifications

Detailed system specs live in `specs/`. Before making structural changes, read the relevant spec:

- Start with `specs/INDEX.md` to find the right document for your task.
- `specs/META.md` defines spec formats and update rules.
```

The AGENTS.md pointer is the "how to use the spec system" entry point — do not create any other workflow/usage document; INDEX.md and META.md carry the navigation and the rules.

### Step 11 — Final validation

Run the validation checklist across all generated specs:

- [ ] Every spec file follows the correct template (all sections present, correct order)
- [ ] All cross-references resolve to existing files
- [ ] All "Key Files" paths point to actual files in the repo
- [ ] Invariants are stated affirmatively
- [ ] INDEX.md lists every spec file
- [ ] META.md "File Organization" matches actual directory structure
- [ ] No project-internal jargon used without definition
- [ ] ASCII diagrams render correctly in monospace

Present a summary to the user:
```
SPEC INIT COMPLETE
═══════════════════════════════════
Generated:
  META.md, INDEX.md
  Architecture specs: N
  Domain specs: N (M READMEs + K detail files)
  Contract specs: N
  ADRs: N + template

Total files: N
```

---

## Quality Guidelines

- **Accuracy over completeness**: it's better to generate fewer specs with correct content than many specs with guesses. If unsure about behavior, mark with `<!-- TODO: verify -->` and note it in the summary.
- **Derive from code, don't invent**: every claim in a spec must be traceable to actual code. Read the source before writing about it.
- **Respect domain boundaries**: one source file may participate in multiple domains. Assign it to the domain where its primary responsibility lies.
- **Start lean**: generate only specs for areas with clear, documentable behavior. Thin areas can be added later via `spec-create`.
- **Ask when unsure**: if the codebase has ambiguous boundaries or unclear patterns, ask the user rather than guessing.
