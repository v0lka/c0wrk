---
name: spec-consult
description: Proactively consult project specifications before making structural changes. Use BEFORE modifying layer boundaries, adding/removing interfaces, changing architectural patterns, adding new components to existing domains, or any change that could violate documented invariants. This skill navigates the spec system to load relevant context.
---

# Spec Consult

**PROACTIVE TRIGGER**: Invoke this skill automatically whenever you are about to:
- Modify or add interfaces between layers/modules
- Add new components to an existing domain
- Change behavior documented in a spec
- Modify architectural boundaries or import rules
- Add a new extension point, plugin, or provider
- Change configuration schema or defaults
- Modify event contracts, protocols, or data flow

Do NOT skip this step even if you think you know the system well. Specs may contain invariants or constraints not obvious from code alone.

---

## Procedure

### Step 1 — Locate the spec system

Find the project's specification root. Look for:
- `specs/` directory at the repository root
- `specs/INDEX.md` — the navigation file
- `specs/META.md` — format and rules reference

If no `specs/` directory exists, inform the user that no specification system was found and proceed without spec consultation.

### Step 2 — Identify relevant specs via INDEX.md

Read `specs/INDEX.md`. It contains a task-to-spec mapping table. Match your current task against the table entries.

**Rules for matching:**
- Match broadly — if your task touches a domain, read that domain's specs even if you're only changing one function.
- If your task crosses layer boundaries, also read the relevant contract spec.
- If unsure, read the domain README first — it provides an overview and points to detail files.

### Step 3 — Read specs in order

Follow this reading order for maximum understanding:

```
1. Domain README (overview, invariants, extension points)
       ↓
2. Detail file for the specific component you're changing
       ↓
3. Contract spec (if your change crosses a boundary)
       ↓
4. Architecture spec (if touching layers, security, or data flow)
```

### Step 4 — Extract constraints

From the specs you read, extract and keep in working memory:
- **Invariants** — rules that must ALWAYS hold (stated affirmatively in specs)
- **Extension points** — the documented way to add new behavior
- **Anti-patterns** — what explicitly NOT to do
- **Breaking change checklists** — from contract specs, "if you change X, also update Y"

### Step 5 — Proceed with implementation

Only after extracting constraints, proceed with the implementation. Keep invariants visible — if any planned change would violate an invariant, stop and discuss with the user.

---

## After Implementation

After completing your changes, determine whether any spec needs updating:
- Did you change documented behavior? → Update the domain spec
- Did you add/remove/rename an interface? → Update the contract spec
- Did you add a new component? → Update the domain README's catalog/table
- Did you make a decision that constrains work beyond the immediate change AND had rejected alternatives, or that contradicts/supersedes a documented ADR or invariant? → Create an ADR; otherwise record the decision in the relevant domain/architecture spec

If specs need updating, invoke the `spec-update` or `spec-create` skill as appropriate.

---

## Edge Cases

- **No INDEX.md found**: Look for any navigation file or README in `specs/`. If nothing exists, report to the user.
- **Spec seems outdated**: Note the discrepancy but do NOT silently fix it during a code change task. Mention it to the user — they may want to invoke `spec-check`.
- **Task doesn't match any INDEX entry**: Read the most closely related domain README. If truly no spec covers your area, mention this to the user — a new spec may be needed.
