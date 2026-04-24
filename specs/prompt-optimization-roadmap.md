# Prompt Optimization — Phased Implementation Roadmap

> **Planning artifact only**: This document defines implementation phases, sequencing, dependencies, risks, and verification gates for the `prompt-optimization` feature described in `specs/prompt-optimization-spec.md`.  
> It does **not** represent implementation already performed and does **not** execute any code changes by itself.

## 1. Scope, Inputs, and Constraints

### 1.1 Source Inputs
- Functional and technical source of truth: `specs/prompt-optimization-spec.md`
- Repository/process constraints: `AGENTS.md`

### 1.2 Hard Constraints (must be preserved in all phases)
1. Layered flow must remain: `frontend -> desktop -> backend -> core`.
2. Frontend must call backend only through `@/api/*` wrappers (no direct Wails imports in components).
3. Frontend-callable methods must be exposed on `*desktop.App` under `desktop/api_*.go`.
4. Zustand selector stability rules must be respected (no object/array allocation inside selectors).
5. Logging must use `log/slog`; avoid non-boundary global logging.
6. Verification gate includes `make test` as authoritative repo-wide test command.
7. No new frontend test framework introduced.

### 1.3 Required Outcome Summary
Deliver end-to-end prompt optimization flow where user-triggered action in chat input runs:
1) LLM translation + keyword extraction, 2) semantic search (`top_k=5`), 3) LLM prompt rewrite, and returns `optimizedPrompt` to replace input text.

---

## 2. Dependency-Aware Execution Order

1. **Phase 0 — Discovery & Alignment** (resolves unknowns and contract decisions)  
2. **Phase 1 — Contracts & Error Taxonomy Freeze** (DTO/error compatibility across FE/Desktop/BE)  
3. **Phase 2 — Backend Orchestration Pipeline** (algorithm + timeout/cancel/logging/NFR instrumentation)  
4. **Phase 3 — Desktop API Exposure** (frontend-callable App method + boundary validation/mapping)  
5. **Phase 4 — Frontend API Wrapper + ChatInput UI Integration** (button/UX/state + RPC wiring)  
6. **Phase 5 — Validation & Quality Gates** (unit/integration/manual + command gates)  
7. **Phase 6 — Rollout Hardening & Operational Readiness** (fallback policy finalization, observability, release controls)

> Rationale: Contract and unknown-resolution work must happen before implementation to avoid cross-layer rework; backend behavior should stabilize before frontend integration; rollout controls depend on validated behavior and metrics.

---

## 3. Phase Plan

## Phase 0 — Discovery & Alignment

### Objectives
- Resolve all known unknowns from spec audit.
- Confirm no layer or policy violations in planned design.

### Scope
- Architecture decision checkpoints only (no implementation yet).

### Tasks
1. Confirm available LLM call abstraction(s) and expected input/output handling for one-shot structured JSON responses.
2. Confirm semantic search API shape and where to enforce `top_k=5` server-side.
3. Confirm existing frontend error surfacing utility (toast/error path) to reuse.
4. Confirm cancellation primitives for non-task RPC path (and fallback behavior if missing).
5. Confirm existing input size/sanitization constants and helper locations.
6. Confirm whether language detection utility exists; if not, lock policy: final rewrite instructed to mirror original language.
7. Confirm product decision: strict failure vs soft fallback when semantic-search transport fails (spec recommends strict V1).

### Risks
- Ambiguous assumptions causing API contract churn later.

### Deliverables
- Alignment notes (decision log) covering each unknown.
- Explicit list of assumptions accepted for V1.

### Exit Criteria
- All seven unknown categories are either resolved or formally deferred with documented fallback behavior.

---

## Phase 1 — Contracts & Error Taxonomy Freeze

### Objectives
- Define stable request/response and error contracts across all layers before coding.

### Scope
- DTO and error-code schema definition and mapping strategy.

### Tasks
1. Finalize request DTO fields: `sessionId`, `projectId?`, `prompt`, `topK`, `includeDebug`.
2. Finalize response DTO fields with required UI minimum (`optimizedPrompt`) and optional debug metadata.
3. Enforce contract rule: backend overrides/enforces `topK=5` in V1 regardless of caller value.
4. Freeze structured error code set:
   - `OPTIMIZE_INVALID_ARGUMENT`
   - `OPTIMIZE_TRANSLATION_FAILED`
   - `OPTIMIZE_PARSE_FAILED`
   - `OPTIMIZE_NO_KEYWORDS`
   - `OPTIMIZE_SEARCH_FAILED`
   - `OPTIMIZE_REWRITE_FAILED`
   - `OPTIMIZE_TIMEOUT`
   - `OPTIMIZE_CANCELED`
5. Define cross-layer error translation policy (backend -> desktop -> frontend wrapper -> user-safe message).
6. Define logging field contract and privacy policy (no full prompt logging by default).

### Dependencies
- Requires Phase 0 decision outcomes.

### Risks
- Contract drift between generated bindings, desktop DTOs, and frontend types.

### Deliverables
- Contract spec appendix/checklist ready for implementation.

### Exit Criteria
- DTO and error taxonomy approved and considered immutable for V1 unless blocker discovered.

---

## Phase 2 — Backend Contracts & Orchestration Pipeline

### Objectives
- Implement core feature behavior in backend frontend-API layer per spec algorithm.

### Scope
- Backend orchestration only; keep provider/search internals in existing core facilities.

### Tasks
1. Add backend frontend-API method (e.g., `OptimizePrompt(ctx, req)`).
2. Input validation/normalization:
   - reject empty prompt
   - resolve required session/project context
   - sanitize malformed input edge cases
3. Step A call: LLM one-shot translation + keyword extraction with strict JSON schema request.
4. Parse/validate Step A response; map failures to parse/keyword-specific errors.
5. Step B: semantic search with enforced `top_k=5`; continue when result count is 0..4, fail on transport error per chosen policy.
6. Step C: LLM one-shot optimization using translated prompt + search context.
7. Return response DTO including `optimizedPrompt` and optional metadata.
8. Add timeout and cancellation handling:
   - target hard timeout: 12s (configurable path for future)
   - cancellation mapping to `OPTIMIZE_CANCELED`
   - safe completion behavior for canceled/timeout requests
9. Add structured logging with required fields:
   - session/project/request IDs, prompt length, keyword count, results count, top_k, step timings, total duration, error code.
10. Confirm security/tool policy invariants unchanged (no new tool permissions).

### Dependencies
- Phase 1 contract freeze.

### Risks
- Latency budget breach from sequential LLM calls.
- Fragile parse handling for model non-JSON outputs.

### Deliverables
- Backend orchestration implementation + unit tests.

### Exit Criteria
- Backend tests for happy path and defined failure matrix pass locally.
- Contract outputs match frozen DTO/error taxonomy.

---

## Phase 3 — Desktop API Exposure

### Objectives
- Expose stable frontend-callable App API method that delegates to backend orchestration.

### Scope
- `desktop/api_*.go` method and boundary mapping only.

### Tasks
1. Add `*desktop.App` method for optimization in appropriate `desktop/api_*.go` split file.
2. Implement boundary-level argument validation (minimal guardrails).
3. Delegate to backend frontend-API service method.
4. Map backend errors into frontend-consumable typed errors while preserving error codes.
5. Ensure Wails binding generation path is accounted for in developer workflow.

### Dependencies
- Phase 2 backend method availability.

### Risks
- Mismatch between desktop method signature and frontend API wrapper expectations.

### Deliverables
- Desktop API method + boundary tests (where applicable).

### Exit Criteria
- Frontend can call a single stable App method with frozen request/response contracts.

---

## Phase 4 — Frontend API Exposure + UI Integration

### Objectives
- Wire user interaction in chat input through API wrapper to backend and reflect UX states correctly.

### Scope
- `frontend/src/api/chat.ts`, `frontend/src/api/index.ts` (if needed), `frontend/src/components/chat/ChatInput.tsx`.

### Tasks
1. Add `optimizePrompt(...)` wrapper in `@/api/chat`:
   - obtains app via existing `getApp()` pattern
   - invokes desktop API method
   - normalizes typed errors for UI
2. Export wrapper through API barrel if current import architecture requires it.
3. In `ChatInput.tsx`, add optimize action handler and local `isOptimizing` state.
4. Add optimize button in action row with:
   - tooltip/title `Optimize prompt`
   - `aria-label="Optimize prompt"`
   - icon from `lucide-react` (preferred `WandSparkles`, fallback per availability)
5. Apply disabled gating exactly per spec:
   - empty trimmed input
   - `isInputDisabled`
   - `showCancel`
   - `isOptimizing`
6. Implement loading visual while in-flight.
7. Success behavior: replace textarea content with `optimizedPrompt`; preserve focus behavior.
8. Failure behavior: keep original text; surface user-safe error via existing UI error/toast mechanism.
9. Concurrency safety:
   - prevent duplicate clicks while request in-flight
   - ignore stale response on unmount/session switch or use abort signal where supported
10. Ensure Zustand selector stability and avoid introducing derived object/array selectors.

### Dependencies
- Phase 3 desktop API exposure complete.

### Risks
- UI race conditions (stale overwrite).
- Error UX inconsistency if bypassing existing notification pattern.

### Deliverables
- Frontend API wrapper and ChatInput integration.

### Exit Criteria
- Manual UX checklist passes for success/failure/loading/disable/concurrency cases.

---

## Phase 5 — Testing, Validation, and Acceptance Gates

### Objectives
- Validate behavior, contracts, and NFR adherence before rollout.

### Scope
- Go tests, frontend checks, and manual regression checklist.

### Tasks
1. Backend unit/contract tests cover:
   - happy path
   - invalid argument
   - translation/provider failure
   - parse failure
   - no keywords
   - search transport failure
   - rewrite failure
   - fewer-than-5 / zero-results success behavior
   - timeout
   - cancellation
   - top_k enforcement to 5
2. Desktop boundary tests (or equivalent) for request validation and error mapping.
3. Frontend checks:
   - lint/build/type checks per existing scripts
   - no new frontend test framework introduction
4. Run authoritative repo test gate: `make test`.
5. Execute manual QA checklist:
   - button disabled states
   - loading lock and duplicate-click prevention
   - success replacement behavior
   - failure retains original input
   - session-switch/unmount stale-response safety

### Verification Gates
- **Gate A (Code health):** relevant package/unit tests green.
- **Gate B (Repo health):** `make test` passes.
- **Gate C (Frontend confidence):** frontend lint/build checks pass.
- **Gate D (Product behavior):** manual UX checklist fully passed.

### Dependencies
- Phases 2–4 complete.

### Risks
- Frontend confidence gaps due to no automated FE test suite.

### Deliverables
- Test evidence summary and acceptance checklist completion report.

### Exit Criteria
- All four verification gates passed with no unresolved critical defects.

---

## Phase 6 — Rollout Hardening & Operational Readiness

### Objectives
- Prepare safe rollout and post-release diagnostics.

### Scope
- Operational controls, fallback behavior finalization, and monitoring readiness.

### Tasks
1. Confirm rollout strategy (direct release vs feature flag if existing mechanism available).
2. Finalize and document fallback policy for search failure (strict V1 or approved alternative).
3. Validate logs provide enough diagnostics for:
   - latency percentile tracking against p50/p95 targets
   - error code distribution
   - cancellation/timeout rates
4. Confirm user-safe error messaging and no sensitive prompt leakage in logs.
5. Define rollback triggers and response playbook for elevated failure/latency.

### Dependencies
- Phase 5 acceptance complete.

### Risks
- Production instability without observability-backed go/no-go criteria.

### Deliverables
- Rollout checklist and operational runbook notes.

### Exit Criteria
- Go/no-go criteria explicitly documented and approved.

---

## 4. Cross-Phase Risk Register (Top Risks + Mitigations)

1. **Contract drift across layers**  
   Mitigation: Phase 1 freeze + Phase 5 contract tests.
2. **Latency over budget (2 LLM calls + search)**  
   Mitigation: timeout guard (12s), per-step timing logs, prompt/context shaping, monitor p50/p95.
3. **Cancellation race/stale UI overwrite**  
   Mitigation: in-flight lock, stale-response guards, optional AbortSignal propagation.
4. **LLM JSON parse fragility**  
   Mitigation: strict schema prompting, parse validation, explicit parse error code.
5. **Error UX inconsistency**  
   Mitigation: enforce use of existing frontend error/toast path and safe message policy.
6. **Observability/privacy conflict**  
   Mitigation: required structured fields + no raw prompt logging by default.

---

## 5. Known Unknowns and Explicit Assumption Handling

| Unknown | Resolution Task (Phase) | Interim Assumption if Unresolved by deadline |
|---|---|---|
| Available LLM abstraction signatures for one-shot structured output | Phase 0.1 | Use current canonical LLM interface in backend/core without adding new provider layer |
| Semantic search invocation shape and where to force `top_k=5` | Phase 0.2 | Enforce in backend orchestration before calling search service |
| Existing non-task RPC cancellation plumbing | Phase 0.4 | Implement stale-response ignore + loading reset minimum behavior |
| Frontend error display utility to reuse | Phase 0.3 | Reuse current chat/API error surfacing pattern; no new subsystem |
| Max prompt length/sanitization helper constants | Phase 0.5 | Reuse nearest existing input guard constants/patterns in backend boundary |
| Language detection utility presence | Phase 0.6 | Instruct rewrite step to output in same language as original prompt |
| Search failure strict vs soft fallback product decision | Phase 0.7 | Default to strict V1 failure (`OPTIMIZE_SEARCH_FAILED`) |

---

## 6. Definition of Done (Roadmap-Level)

Implementation is considered complete only when:
1. All ordered phases are executed respecting dependencies.
2. Functional behavior matches spec algorithm and UI requirements.
3. Error contracts and DTOs are stable and validated.
4. NFR controls (latency target tracking, timeout, cancellation safety, logging) are present.
5. Verification gates pass, including `make test` and relevant frontend checks.
6. Remaining assumptions are either resolved or explicitly documented with approved fallback behavior.

---

## 7. Out of Scope for This Document

- No source code modifications are executed by this roadmap document.
- No production rollout is performed by this document.
- No deviation from `specs/prompt-optimization-spec.md` is approved unless captured via separate decision process.
