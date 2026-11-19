# ADR-132: Plan step descriptions carry a declared label contract, normalized deterministically at publish

## Status

Accepted

## Context

A `declare_plan` task's `description` is free-form markdown the model authors, but it is not private to the declare_plan call: every published description flows to four consumers with conflicting format expectations, and it passes through exactly one choke point — `conductorPublisher.Publish` (the `PlanTaskInput` → `PlanStep` conversion in `core/conductor.go`):

1. **The persisted plan file** (`SessionPlansDir`, `plan_<RandomSuffix>.md`) — the artifact the human reviews, approves and can re-open later.
2. **The `PlanGenerated` event → frontend plan panel / PlanEditor** — `frontend/src/lib/planParser.ts` re-parses the markdown into structured fields, and its field grammar (`detectFieldHeader` / `normalizeFieldName`) recognizes ONLY `### Label:` and `**Label**:` shapes for the four canonical labels `What` / `Where` / `How` / `Acceptance Criteria`. A model spelling like `## What`, `AC:` or `Acceptance Criteria -` matches no shape: the field is invisible to the parser, and when the user edits and saves the plan in the PlanEditor, `serializePlanMarkdown` rewrites every step from its parsed fields — the unrecognized content is silently replaced by `...` placeholders. That is data loss triggered by a cosmetic deviation.
3. **The blackboard plan** (`bb.SetPlan`) — restored on resume/continuation and echoed into approval flows, so a non-canonical description persists for the session's lifetime.
4. **The delegated executor's task text** — the wave dispatch builds each subagent's `Task` from `step.Description`, so the description IS the executing step's instructions; raw `#`-header noise and inconsistent label spellings degrade that input directly.

Two structural facts make this worse than a styling nit:

- **`#`-level headers inside a description can collide with the plan file's own step structure.** Both the Go serializer and the frontend parser delimit steps by the `# Step N (id):` heading; a description carrying its own `#`-headers is structure masquerading as content.
- **Prompts are not a guaranteed enforcement surface.** The Model Profiles system-prompt Lite swap replaces the orchestrator prompt with `core/prompts/orchestrator_lite.md`, which never mentions `declare_plan`; the family-specific orchestrator prompts drift independently. The only surfaces every model family sees in every configuration are the tool description and its JSON schema — the same surfaces that already carry the mandatory-`depends_on` rule ([ADR-069](069-plan-step-dependencies-mandatory.md), landed per [ADR-047](047-plan-step-dependencies.md)'s trim-to-fit precedent).

Relying on prose alone therefore leaves the failure mode intact exactly where it hurts (Lite profile users, who are the population most likely to produce deviant formats), and the parser round-trip keeps destroying user data whenever the model deviates.

## Decision

The format contract is **declared on the tool surfaces** and **enforced in code at the publish boundary** — belt and braces, each covering the other's gaps.

**1. Declared contract on both tool surfaces.** `core/tools/declare_plan.go` states the description format identically in the tool description (Inputs section) and in the `tasks[].description` JSON-schema description: review-ready markdown; the labels **What**, **How**, **Where**, **Acceptance Criteria**, one per line; AC is a `- [ ]` checklist; no `#`-headers inside. The tool surfaces are format-authoritative because they survive the Lite prompt swap, every orchestrator-family prompt, and any future prompt rework.

**2. Deterministic normalization at publish.** `conductorPublisher.Publish` runs every description through `normalizePlanDescription` (`core/plan_description.go`) during the `PlanTaskInput` → `PlanStep` conversion — the single point every plan passes through before any consumer sees it. The normalizer is a fence-aware line scan (CommonMark fence rules: ≤3-space indent allowance, backtick info strings, run-length matching; fence bodies verbatim): outside fences, `#`-header field labels demote to the canonical `**Label**:` markers, known near-miss label spellings canonicalize (`planCanonicalLabels` mirrors the frontend parser's name set — `what` / `where` / `how` / `acceptance criteria` — plus the `ac` alias, case- and whitespace-insensitive, with `:` or ` - ` separators), already-canonical lines are untouched, and runs of 3+ blank lines collapse to one; an unclosed trailing fence is closed with its own marker. The function is idempotent. Its canonical label grammar is deliberately the same the frontend parser already accepts, so serializer, parser and editor agree on one shape.

Because normalization happens once at the boundary, all four consumers — plan file, `PlanGenerated` event, blackboard, executor task text — receive the canonical form without any per-consumer change or per-call-site awareness.

## Consequences

Positive:

- The PlanEditor round-trip can no longer silently discard step content: every persisted description is already in the parser's grammar.
- The contract holds for every model family and every prompt configuration (including Lite), because conformance is produced by code at the boundary rather than requested in prose and hoped for.
- One place to evolve: a future label-set change is one map in `core/plan_description.go` plus the two schema/description strings, not a sweep over call sites or consumers.
- The executor's task text loses header-noise and inconsistent label spellings by construction.

Negative / residual:

- Description text is transformed, not preserved verbatim. A description whose prose legitimately opens with a `#`-header-like label line is rewritten; fence-awareness protects intentional code blocks, and idempotency means re-normalization is a no-op, but a descriptive voice that habitually uses markdown headers must adapt to the declared contract.
- **Legacy plans are not normalized.** The plan-restore path reads persisted plans as-is and never re-runs the normalizer (mirroring the ADR-069 stance that `execute_plan`/plan-restore never re-validate). A plan file written before this change that carries `## What` still loses that field on a PlanEditor round-trip. Re-declaring the plan is the migration path; retroactively rewriting persisted plans was considered and rejected as a silent mutation of reviewed, user-approved artifacts.
- The Go normalizer and the TS parser must agree on the canonical label set; the agreement is pinned by tests on both sides (`plan_description_test.go` fixtures vs `planParser.test.ts`), and drift between them is now a cross-language contract to maintain.

## Alternatives Considered

- **Prompt-only guidance.** Rejected: prompts are not guaranteed surfaces — the Lite swap drops all `declare_plan` guidance (`orchestrator_lite.md` never mentions the tool), family prompts drift independently, and nothing enforces compliance. This is precisely the failure mode the normalization closes.
- **Structured schema fields** (`What` / `Where` / `How` / `acceptance_criteria` as separate JSON properties instead of one markdown string). Rejected: it moves the format problem inside each field — field values still need markdown (AC checklists, code snippets) whose shape JSON Schema cannot express — while breaking the wire shape and every downstream consumer of the single `description` string for a cosmetic gain. The declared-contract route keeps one string and a defined shape.
- **Frontend normalization** (canonicalize in `planParser.ts` / the PlanEditor on read). Rejected: it repairs one of the four consumers, after the fact, while the plan file stays noisy, the blackboard/event payloads stay non-canonical, and the executor still receives raw header-noise as its instructions; it also makes the editor's view differ from the persisted bytes and multiplies the places that must know the canonical grammar.
- **Reject non-conforming descriptions at validation** (a hard gate in `validatePlanTasks`). Rejected: a refusal loop over a cosmetic contract is hostile to exactly the small/local model population Model Profiles serves, and every publish path (including restore-time re-declares) would inherit the refusal handling. Deterministic normalization makes conformance automatic instead of negotiated.
- **Normalize per consumer** (each of the four destinations normalizing on its own). Rejected: four implementations of one rule, four chances to drift; the publish boundary already is the single choke point every description passes through, so the rule belongs there.

## Related

- [ADR-047](047-plan-step-dependencies.md) — the plan-step dependency levers that stand (wave echo, single-wave hint, `Depends on:` rendering) and the trim-to-fit precedent for landing contract text in the tool description.
- [ADR-069](069-plan-step-dependencies-mandatory.md) — the other rule living on the `declare_plan` tool surfaces (mandatory `depends_on`), and the stance that plan-restore never re-validates.
- [conductor-tools.md](../contracts/conductor-tools.md) (`declare_plan` behavioral notes) — the contract this ADR documents behaviorally.
- `core/plan_description.go` — `normalizePlanDescription`, `planCanonicalLabels`, the fence scanner.
- `core/conductor.go` — `conductorPublisher.Publish`, the normalization hook and the `Task: step.Description` executor dispatch.
- `core/tools/declare_plan.go` — the declared contract on the tool description and JSON schema.
- `frontend/src/lib/planParser.ts` — the parser whose field grammar (`detectFieldHeader` / `normalizeFieldName`) the canonical labels match; `serializePlanMarkdown`, the rewrite that made deviations lossy.
- [model-profiles.md](../domains/model-profiles.md) (Lite variant) — the prompt swap that makes tool surfaces format-authoritative.
