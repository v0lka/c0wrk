---
name: spec-check
description: Detect discrepancies between project specifications and actual code. Use when you suspect specs have drifted from implementation, after a major refactoring, during onboarding to verify spec accuracy, or on explicit user request. For each discrepancy found, interactively asks the user whether to trust the code, trust the spec, trust neither, or ignore.
---

# Spec Check

Systematically compare project specifications against actual source code to detect drift. For every discrepancy found, ask the user how to resolve it.

**This is an interactive, potentially lengthy process.** It reads both specs and source code, identifies mismatches, and requires user decisions for each one.

---

## When to Invoke

- User explicitly requests a spec consistency check
- After a large refactoring where many specs may be stale
- When onboarding to a project and needing to verify spec accuracy
- When a spec seems to contradict what you observe in code
- Periodically as a maintenance task

---

## Procedure

### Step 1 — Determine scope

Ask the user (or infer from context) what scope to check:

| Scope | What it checks |
|-------|---------------|
| **Single spec** | One specific spec file vs. its referenced source files |
| **Domain** | All specs in a domain directory vs. corresponding code |
| **Contracts** | All contract specs vs. actual interfaces in code |
| **Full** | Entire `specs/` directory vs. codebase |

For large scopes, inform the user this will be thorough and may surface many findings.

### Step 2 — Load the spec

Read the target spec file(s) fully. Extract checkable claims:

- **Key Files** — do the referenced files exist at those paths?
- **Core Types / Interfaces** — do the type definitions match what's in code?
- **Invariants** — does the code actually enforce these properties?
- **Behavior descriptions** — does the code implement what the spec describes?
- **Configuration** — do parameter names, defaults, and valid values match?
- **Cross-references** — do linked spec files exist? Do section anchors resolve?
- **Event catalogs** — do documented events match actual event emissions?
- **Breaking Change Checklists** — are the listed dependencies accurate?

### Step 3 — Load corresponding source code

For each claim in the spec, read the relevant source file(s). Focus on:
- Type/interface definitions (structural match)
- Function signatures and behavior (semantic match)
- Configuration defaults and parameter names
- Event emissions and subscriptions
- Import patterns and boundaries

### Step 4 — Identify discrepancies

Categorize each finding:

| Category | Description | Example |
|----------|-------------|---------|
| **PATH_MISSING** | Key File path doesn't exist | Spec says `src/router.go`, file was moved to `src/core/router.go` |
| **TYPE_MISMATCH** | Type definition differs | Spec shows 3 fields, code has 5 |
| **INTERFACE_DRIFT** | Interface signature changed | Method was added/removed/renamed |
| **BEHAVIOR_DRIFT** | Documented behavior differs from implementation | Spec says "retries 3 times", code retries 5 times |
| **CONFIG_DRIFT** | Config param name/default/range differs | Spec says default is 10, code uses 20 |
| **INVARIANT_VIOLATED** | Code doesn't enforce a documented invariant | Spec says "always validates input", code has a path that skips |
| **CROSS_REF_BROKEN** | Referenced spec or section doesn't exist | Link to `domains/auth.md` but file was removed |
| **UNDOCUMENTED** | Significant code exists with no spec coverage | New module has no corresponding domain spec |
| **STALE_CONTENT** | Spec documents something that was removed from code | Spec describes a feature that was deleted |
| **DUPLICATE_ADR_NUMBER** | Two ADR files in `specs/decisions/` share the same leading `NNN` | `008-a.md` and `008-b.md` both exist — parallel branches allocated 008 independently and merged cleanly |

### Step 5 — Report and ask for each discrepancy

For EACH discrepancy, present it clearly to the user and ask for a resolution. Use the following format:

```
DISCREPANCY: [category]
Spec: [file path] — [what the spec says]
Code: [file path] — [what the code does]
```

Then ask the user to choose one of:

| Resolution | Meaning | Action |
|------------|---------|--------|
| **Trust code** | Code is correct, spec is stale | Update the spec to match code |
| **Trust spec** | Spec is correct, code has a bug | Flag for code fix (do NOT auto-fix code) |
| **Trust neither** | Both are wrong, needs rethinking | Note it as an open item for the user to address |
| **Ignore** | Not worth fixing right now | Skip, optionally log for later |

### Step 6 — Apply resolutions

After collecting all user decisions:

- **Trust code** → invoke `spec-update` skill to update the spec
- **Trust spec** → report the code locations that need fixing (do NOT modify code within this skill; the user decides when to fix)
- **Trust neither** → summarize as an open question requiring design discussion
- **Ignore** → skip silently

### Step 7 — Summary report

After processing all discrepancies, provide a summary:

```
SPEC CHECK SUMMARY
══════════════════════════════════════════
Scope: [what was checked]
Specs checked: N
Discrepancies found: N

  Resolved (trust code):    N — specs updated
  Resolved (trust spec):    N — code fixes needed
  Unresolved (neither):     N — requires discussion
  Ignored:                  N

Remaining action items:
- [list of code fixes needed, if any]
- [list of open design questions, if any]
```

---

## Checking Strategies by Spec Type

### Domain README / Detail

1. Verify every path in "Key Files" exists
2. Compare "Core Types" code blocks against actual type definitions
3. For "Behavior" sections: trace the described flow through actual code
4. For "Invariants": search for code paths that might violate them
5. For "Configuration": check actual config structs/schemas

### Contract

1. Verify every interface in the "Interfaces" table exists in code
2. Check method signatures match
3. Verify "Initialization" matches actual wiring code
4. Check "Error Propagation" rules are followed at the boundary

### Architecture

1. Verify import rules are not violated (check actual imports)
2. Verify "Anti-Patterns" are not present in code
3. Verify "Invariants" hold across the codebase

### ADR

ADRs are historical records and generally don't drift. Check only:
1. `Status` field — if `Accepted`, verify no contradicting newer ADR exists
2. Referenced code/specs in Context/Decision — verify they still exist
3. Duplicate numbers — scan the entire `specs/decisions/` tree for files sharing the same leading `NNN` (parse `NNN` from each `NNN-slug.md` filename and ignore the slug; include superseded ADRs — a retired record keeps its number). Report each contested number with its claimant files.

**DUPLICATE_ADR_NUMBER resolution:** renumber the second-landed record to the next free number per the renumber protocol in `spec-create` — update all references (filename, `# ADR-NNN:` heading, supersede links, INDEX.md entries, cross-references in other specs) — then re-run the cross-reference check to catch links broken by the renumbering.

---

## Efficiency Notes

- Start with "Key Files" path verification — it's fast and catches renames/moves
- Then check type/interface structural matches — these are objective
- Save behavioral/semantic checks for last — they require deeper analysis
- For "Full" scope: batch discrepancies by domain and present one domain at a time to avoid overwhelming the user
- If more than 10 discrepancies are found in a single spec, ask the user whether the spec might need a full rewrite rather than incremental fixes
