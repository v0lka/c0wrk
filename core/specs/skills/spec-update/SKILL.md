---
name: spec-update
description: Update existing specification documents after code changes that alter documented behavior, interfaces, or invariants. Use after implementation when you have changed behavior covered by a spec, added/removed/renamed interfaces in a contract, modified architectural boundaries, or changed configuration. Ensures format preservation, cross-reference integrity, and INDEX.md currency.
---

# Spec Update

Update existing specification documents to reflect code changes. Specs are the source of truth about intended behavior — when code changes, affected specs must be updated to stay aligned.

---

## When to Invoke

Update specs after any change that:
- Alters behavior documented in a domain spec
- Adds, removes, or renames interfaces that appear in a contract
- Changes architectural boundaries or invariants
- Modifies configuration parameters documented in a spec
- Adds or removes files referenced in a spec's "Key Files" section
- Changes event payloads, types, or protocols documented in a contract

**Do NOT update specs for:**
- Internal refactoring that preserves documented behavior
- Bug fixes that bring code INTO alignment with the spec (the spec was already correct)
- Changes to code not covered by any spec

---

## Procedure

### Step 1 — Identify affected specs

Determine which specs need updating based on what you changed:

| What you changed | Update |
|-----------------|--------|
| Component behavior | Domain detail file for that component |
| New component in existing domain | Domain README (catalog/table) + new detail file (use `spec-create`) |
| Interface signatures | Contract spec for that boundary |
| Initialization/wiring | Contract spec "Initialization" section |
| Configuration parameters | Domain README "Configuration" section |
| Error handling strategy | Domain detail "Error Handling" + contract "Error Propagation" |
| Architectural rules | Architecture spec + potentially affected contracts |
| File paths (rename/move) | All specs referencing those paths in "Key Files" |

### Step 2 — Read the current spec FULLY

Read the entire spec before making any modifications. This is critical because:
- You need to understand the existing structure to preserve it
- There may be invariants you're about to violate
- Cross-references may need updating
- The document format has a required section order

### Step 3 — Make targeted updates

**Rules for updating:**

1. **Preserve the document format** — sections and their order are defined in META.md. Never reorder, remove, or rename sections.
2. **Update, don't append** — modify existing content in place. Don't add "Updated:" prefixes or changelog entries within the spec.
3. **Maintain voice** — specs use declarative, factual statements. No hedging ("should", "might"), no changelog language ("was changed to").
4. **Keep invariants affirmative** — "X always does Y", not "X never does Z".
5. **Update cross-references** — if file paths changed, update all references across all affected specs.
6. **Preserve information density** — no filler prose. Every sentence carries information.

### Step 4 — Handle ADR immutability

`Status: Accepted` locks an ADR only once its current content is committed to the repository. Establish which case applies before touching an accepted ADR — git: `git status --porcelain -- specs/decisions/NNN-slug.md`; empty output = committed (outside a git repository, treat the ADR as committed):

- **Uncommitted** (untracked, or content differs from the last commit) — the ADR is still a draft: edit it freely in place — reword, correct, or even reverse the decision; no superseding ADR is needed. The commit is what locks it
- **Committed** — do NOT edit it. If your change contradicts the decision:
  - Create a new ADR that supersedes it (use `spec-create` skill)
  - Update only the `## Status` line of the old ADR to: `Superseded by [NNN](./NNN-slug.md)`
  - This is the ONLY permitted edit to a committed accepted ADR

If you have no way to run git, establish the commit state from the task context or ask the user — never guess about a committed accepted ADR.

### Step 5 — Update INDEX.md (if applicable)

Update `specs/INDEX.md` if:
- You added or removed a spec file
- You renamed a spec file
- The "task → spec" mapping table needs a new entry for discoverability

### Step 6 — Update cross-references in other specs

If your changes affect content referenced by other specs:
- Search for references to the updated file across all specs
- Verify section anchors still resolve (lowercase, hyphen-separated)
- Update any broken links

### Step 7 — Validate (embedded checklist)

Before declaring the update complete, verify:

- [ ] All sections from the template are still present and in correct order
- [ ] No sections were accidentally removed or reordered
- [ ] Cross-references point to existing files and valid section anchors
- [ ] Paths in "Key Files" are accurate (verify the files exist)
- [ ] Invariants are stated affirmatively
- [ ] INDEX.md reflects the current file set (if files were added/removed)
- [ ] ADR immutability was respected (committed accepted ADRs touched only via superseding; uncommitted drafts edited in place)
- [ ] No filler prose was introduced
- [ ] ASCII diagrams are consistent with updated behavior
- [ ] Tables/catalogs include any new entries

---

## Common Mistakes to Avoid

| Mistake | Why it's bad | What to do |
|---------|-------------|------------|
| Silently skipping spec update | Specs drift, next agent gets wrong context | Always check if specs are affected |
| Rewriting the entire spec | Loses carefully worded invariants | Make minimal, targeted edits |
| Adding changelog entries | Clutters the spec, wastes context window | Update in-place, git history tracks changes |
| Editing a committed accepted ADR | Loses decision history | Create new ADR with Superseded by (uncommitted accepted ADRs are drafts — edit in place) |
| Updating spec before code is done | Risk of spec-code mismatch if implementation changes | Update spec AFTER implementation is complete |
| Skipping cross-reference check | Broken links make specs unreachable | Search for references to modified content |
| Stating invariants negatively | Harder to verify compliance | Always use affirmative phrasing |

---

## Multiple Specs Affected

When a single code change affects multiple specs (e.g., adding a new interface touches both a domain spec and a contract), update them in this order:

1. Contract specs (boundary definitions)
2. Domain detail specs (component behavior)
3. Domain READMEs (catalogs, extension points)
4. Architecture specs (system-level rules)
5. INDEX.md (navigation)

This order ensures that references from higher-level docs to lower-level docs are valid at each step.
