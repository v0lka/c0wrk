# Example Session

A full spec-aware run: explore → plan → implement. The project has a `specs/`
directory. Illustrative of tone and flow, not a script.

```
User: /spec-explore
      I'm not sure how to structure this new feature

You: Let's think through it — and let's start from what the project already
     documents. What's the feature?

User: Users need to export data in multiple formats

You: [reads specs/INDEX.md, then the domains it points to]

     specs/INDEX.md maps nothing to "export". The closest documented area is
     the reporting domain, which owns `Data` and says nothing about
     serialization.

     UNDOCUMENTED: data export — no spec coverage

     That's a finding in itself: whatever we build here will need new spec
     coverage.

     Export is interesting. Let me visualize the space...

     ┌─────────────────────────────────────────┐
     │       EXPORT ARCHITECTURE               │
     └─────────────────────────────────────────┘

     Option A: Format-Specific Exporters
     ────────────────────────────────────
     Data → CSV Exporter → file.csv
          → JSON Exporter → file.json
          → XML Exporter → file.xml

     + Simple, explicit
     - Duplication across formats

     Option B: Pipeline Pattern
     ──────────────────────────
     Data → Normalize → Transform → Serialize
                           ↓
                   [CSV|JSON|XML]

     + Reusable transforms
     - More abstraction

     How many formats? How different are they?

User: Three formats, pretty different structures

You: Three formats and different structures suggests the pipeline's shared
     transforms won't pay for themselves.

     Option A might be clearest — behind one interface:

     interface Exporter {
       export(data: Data): Result<string, ExportError>
     }

     Before I recommend it, let me check the architecture specs...

     [reads specs/architecture/layers.md]

     Note: layers.md states the domain layer never imports infrastructure.
     So exporters may produce strings — that's domain-safe — but they must
     not write to disk. I/O belongs at the edge. That constrains the design.

     Does the field-extraction duplication bother you?
     Or is simplicity more valuable here?

User: Simplicity wins. Show error, no retry, all-or-nothing.

You: Then a Result<string, ExportError> return type fits: explicit
     success/failure, all-or-nothing, caller decides what to do.

     This feels solid enough to plan. Want me to enter Plan Mode?

User: Yes

You: ───────────────────────────────────────────
     Entering Plan Mode
     ───────────────────────────────────────────

     [runs the spec-consult procedure for the areas the plan touches]

     # Roadmap: Multi-format Data Export

     ## Overview
     Add a multi-format data export feature using format-specific exporters
     behind a common interface, selected by a factory. Exporters produce
     strings only; I/O stays at the edge. Errors use a Result<T, Error>
     pattern — all-or-nothing, no retry, no partial export.

     ## Decisions Recorded
     - Format-specific exporters (Option A) over the pipeline pattern —
       different structures don't benefit from shared transforms
     - Simplicity over DRY — accept field-extraction duplication
     - Result<string, ExportError> return type — explicit, caller-decides
     - All-or-nothing semantics — no partial export, no retry
     - Factory selects the exporter from a format parameter
     - Exporters never perform I/O (keeps the domain layer pure)

     ## Invariants & Constraints (from specs)
     - The domain layer never imports infrastructure — source:
       `specs/architecture/layers.md`
     - Reporting's `Data` type is the export input; do not fork it — source:
       `specs/domains/reporting/README.md`

     ## Global Constraints
     - Follow existing TypeScript conventions in the codebase
     - Exporters must be independently testable

     ## Tasks

     ### Task 1: Exporter interface and error types
     **What:** Define the Exporter interface, ExportError type, and a Result
     helper if not already present.
     **How:** Result<T, E> as a discriminated union; Exporter with
     `export(data: Data): Result<string, ExportError>`; ExportError as a union
     (InvalidData, SerializationError).
     **Where:** `src/export/types.ts`, `src/export/result.ts`
     **Spec footprint:** none (internal types only)
     **Acceptance criteria:**
     - [ ] Exporter interface matches the signature above
     - [ ] ExportError covers invalid data and serialization failures
     - [ ] Result supports success/failure discrimination
     - [ ] All types compile without errors

     ### Task 2: Exporter factory
     **What:** A factory returning the exporter for a format string.
     **How:** `getExporter(format): Exporter` via a lookup map; throw on
     unknown format (programmer error, not a runtime export error).
     **Where:** `src/export/factory.ts`
     **Spec footprint:** none
     **Acceptance criteria:**
     - [ ] Returns the right exporter for "csv", "json", "xml"
     - [ ] Throws with a descriptive message on unknown format

     ### Task 3: CSV exporter
     **What:** Convert Data to CSV.
     **How:** Extract and escape fields (commas, quotes, newlines); header row;
     return ExportError for missing required fields.
     **Where:** `src/export/csv-exporter.ts`
     **Spec footprint:** create `specs/domains/export/README.md` and
     `csv-exporter.md` (UNDOCUMENTED area from Explore)
     **Acceptance criteria:**
     - [ ] Produces valid CSV with a header row
     - [ ] Escapes commas, quotes, and newlines
     - [ ] Returns ExportError for missing required fields
     - [ ] Header-only output for an empty set

     ### Task 4: JSON exporter
     **What:** Convert Data to JSON.
     **How:** `JSON.stringify` with indentation; ExportError on failure.
     **Where:** `src/export/json-exporter.ts`
     **Spec footprint:** create `json-exporter.md` in the export domain
     **Acceptance criteria:**
     - [ ] Produces valid, pretty-printed JSON
     - [ ] Returns ExportError on serialization failure

     ### Task 5: XML exporter
     **What:** Convert Data to XML.
     **How:** Escape <, >, &, ", '; wrap in a root element.
     **Where:** `src/export/xml-exporter.ts`
     **Spec footprint:** create `xml-exporter.md`; update `specs/INDEX.md`
     **Acceptance criteria:**
     - [ ] Well-formed XML with a root element
     - [ ] Escapes <, >, &, ", '
     - [ ] Returns ExportError for missing required fields

     ## Task Dependencies
     Task 1 → Tasks 2, 3, 4, 5
     Recommended order: 1 → 2 → 3 → 4 → 5

     ## Risks & Mitigations
     - Field-extraction duplication: acceptable per decision; extract a shared
       helper only if it becomes painful
     - XML escaping edge cases: use a well-tested escaping function

     ───────────────────────────────────────────
     Review the roadmap above. You can:
       1. Approve — I'll enter Implementation Mode and execute it
       2. Request changes — tell me what to revise
     ───────────────────────────────────────────

User: Looks good, go ahead

You: Approved. One note: none of these decisions pass the ADR bar — they
     are local to the new export domain and land in its domain spec, so no
     ADR is warranted here.

     ───────────────────────────────────────────
     Entering Implementation Mode
     Executing roadmap: Multi-format Data Export
     5 tasks total
     ───────────────────────────────────────────

     [Task 1/5] ✓ Exporter interface and error types
       Acceptance criteria: all met
       Files: src/export/types.ts, src/export/result.ts
       Specs: consulted specs/architecture/layers.md — no spec changes (footprint: none)
       Checks: typecheck ✓

     [Task 2/5] ✓ Exporter factory
       Acceptance criteria: all met
       Files: src/export/factory.ts
       Specs: no spec changes (footprint: none)
       Checks: typecheck ✓

     [Task 3/5] ✓ CSV exporter
       Acceptance criteria: all met
       Files: src/export/csv-exporter.ts
       Specs: created specs/domains/export/README.md, csv-exporter.md
       Checks: typecheck ✓, tests ✓

     [Task 4/5] ✓ JSON exporter
       Acceptance criteria: all met
       Files: src/export/json-exporter.ts
       Specs: created specs/domains/export/json-exporter.md
       Checks: typecheck ✓, tests ✓

     [Task 5/5] ✓ XML exporter
       Acceptance criteria: all met
       Files: src/export/xml-exporter.ts
       Specs: created xml-exporter.md; updated specs/INDEX.md
       Checks: typecheck ✓, tests ✓

     ───────────────────────────────────────────
     Roadmap complete: Multi-format Data Export
     5/5 tasks implemented and verified

     Final checks:
       lint ✓
       typecheck ✓
       tests ✓ (12 passing)

     Spec sync:
       updated:  specs/INDEX.md
       created:  specs/domains/export/ (README + 3 detail files)
       footprint left open: none

     Suggested next: run spec-check to confirm the new specs match the code.
     ───────────────────────────────────────────
```
