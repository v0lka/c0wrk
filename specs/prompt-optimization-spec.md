# Prompt Optimization Feature — End-to-End Implementation Specification

## 1. Scope and Objective

Implement a user-triggered **"Optimize prompt"** feature in the chat input flow.

When the user clicks the optimize button in the sending area:
1. Current input prompt is sent to backend.
2. Backend runs the requested optimization algorithm:
   - one-shot LLM translation to English + keyword extraction,
   - semantic search with `top_k=5`,
   - one-shot LLM optimization using translated prompt + retrieved context.
3. Optimized prompt is returned and **replaces** the current input text.

This spec is scoped to project-native architecture confirmed in discovery:
- `frontend/src/components/chat/ChatInput.tsx`
- `frontend/src/api/chat.ts`
- `desktop/api_*.go` desktop-exposed methods on `*desktop.App`
- `backend/frontend_api_*.go` orchestration layer
- existing core/backend LLM + vector/semantic-search facilities

---

## 2. Constraints from AGENTS.md (Hard Requirements)

1. **Frontend RPC path**: all backend calls must go through `@/api/*`; no direct `wailsjs/go/desktop/App` imports in UI components.
2. **Desktop API exposure**: frontend-callable methods belong on `*desktop.App` in `desktop/api_*.go` split files.
3. **Layering**: maintain existing layered flow `frontend -> desktop -> backend -> core`; do not bypass boundaries.
4. **State selectors**: use selector-stable patterns in Zustand usage; do not create arrays/objects in selectors.
5. **Logging**: use `log/slog`; pass loggers through constructors/services; avoid global logging in non-boundary code.
6. **Validation/testing commands**: repo-wide verification uses `make test` (authoritative command). No new frontend test framework is introduced.

---

## 3. UX/UI Specification

## 3.1 Placement and Visuals

**Component:** `frontend/src/components/chat/ChatInput.tsx`

Add a new icon button in the existing bottom action row (same control cluster as send/cancel area in chat mode).

- Tooltip/title: `Optimize prompt`
- Accessible name: `aria-label="Optimize prompt"`
- Icon: use existing icon library (`lucide-react`) with a semantic edit/improve glyph (recommended: `WandSparkles` if available in current version; fallback: `Sparkles` or `PenLine`).

## 3.2 Interaction Rules

On click:
- If trimmed input is empty: no request, keep button disabled.
- Otherwise invoke optimization RPC.
- On success: replace textarea content with returned optimized text (`setText(optimized)`), preserving focus if currently focused.

## 3.3 States

Introduce local state in `ChatInput.tsx`:
- `isOptimizing: boolean`

Button state behavior:
- **Disabled when**:
  - `!text.trim()`
  - `isInputDisabled` (existing gating: active task / no project)
  - `showCancel` (to avoid concurrent task cancellation/send interaction conflicts)
  - `isOptimizing`
- **Loading visual** while `isOptimizing=true`:
  - spinner or animated icon rotation
  - optional temporary title/hint: `Optimizing prompt...`

Send/cancel controls during optimization:
- Keep existing task controls unchanged.
- Prevent duplicate optimize requests by hard disabling optimize button while in-flight.

## 3.4 Error UX

If optimization fails:
- Keep original input unchanged.
- Surface user-visible error via existing toast/error pattern already used in frontend API call sites (implementation should reuse current mechanism, not invent a new notification subsystem).

---

## 4. Frontend Architecture and Flow

## 4.1 API Wrapper (No direct Wails in component)

**File:** `frontend/src/api/chat.ts`

Add:

- `optimizePrompt(sessionId: string, prompt: string, options?: { signal?: AbortSignal }): Promise<OptimizePromptResponseUI>`

Wrapper responsibilities:
1. Retrieve `app` via existing `getApp()` helper pattern.
2. Call new desktop method (e.g. `app.OptimizePrompt(req)` or `app.OptimizePrompt(sessionId, prompt)` depending on preferred project convention).
3. Normalize/propagate typed errors.
4. Export through existing barrel (`frontend/src/api/index.ts`) if needed by current import style.

## 4.2 ChatInput Event Flow

In `ChatInput.tsx`:
1. Add `handleOptimizePrompt` callback.
2. Read current `text` and `sessionId` from existing local/store data path.
3. Set `isOptimizing=true`.
4. Await `optimizePrompt(...)`.
5. On success: `setText(response.optimizedPrompt)`.
6. On failure: show error, keep `text` unchanged.
7. Finally: `isOptimizing=false`.

Selector stability:
- Reuse existing primitive selectors and already selected fields.
- Do not introduce derived object/array selectors inline.

## 4.3 Cancellation on Frontend

Minimum behavior:
- If component unmounts during request, ignore late response (guard with mounted flag or request token).

Preferred behavior (if existing API client supports it without architectural drift):
- Pass `AbortSignal` to wrapper and map to desktop/backend cancellation token.

Assumption/unknown:
- Exact cancellation primitive for non-task RPC calls is not confirmed in discovered files; implement graceful late-response ignore if true cancel propagation is unavailable.

---

## 5. Backend/Desktop Architecture

## 5.1 Desktop API Surface

Add new frontend-callable method on `*desktop.App` in a suitable split file (`desktop/api_session.go` or new `desktop/api_prompt.go`, following existing naming conventions):

- `OptimizePrompt(req OptimizePromptRequest) (OptimizePromptResponse, error)`

or (if existing API style prefers positional args):

- `OptimizePrompt(sessionID string, prompt string) (string, error)`

**Recommendation:** request/response structs for forward compatibility and explicit error contracts.

Desktop method responsibilities:
1. Validate basic inputs (non-empty prompt; session/project context presence if required by backend service).
2. Delegate to backend frontend-API service method.
3. Return typed response/error to Wails binding.

## 5.2 Backend Frontend-API Orchestration

Implement in backend frontend API layer (`backend/frontend_api_*.go`, aligned with existing chat/vector split):

Suggested method:
- `OptimizePrompt(ctx context.Context, req OptimizePromptRequest) (OptimizePromptResponse, error)`

Responsibilities:
1. Input validation and normalization.
2. Orchestrate algorithm steps (Section 6).
3. Call existing LLM provider abstraction through core/backend service boundaries.
4. Call existing semantic/vector search facility with `top_k=5`.
5. Return optimized prompt and optional debug metadata.

Service boundary rule:
- Keep orchestration in backend frontend-API layer; low-level provider/search logic remains in existing core/vector components.

---

## 6. Algorithm Specification (Required Behavior)

## 6.1 Step A — Translation + Keyword Extraction (One-shot LLM)

Input:
- original user prompt (any language)

LLM one-shot output schema (strictly requested in prompt):
- `translated_prompt_en: string`
- `keywords: string[]` (for semantic search)

Requirements:
- Deterministic-ish output preference (low temperature) for stable keyword extraction.
- Enforce JSON output contract in prompt instructions and validate parsing.

Validation:
- If translation empty or keyword list empty -> return structured error (`OPTIMIZE_PARSE_FAILED` or `OPTIMIZE_NO_KEYWORDS`).

## 6.2 Step B — Semantic Search

Use extracted keywords to query existing vector semantic search facility.

Hard requirement:
- `top_k = 5`

Query strategy:
- Use keyword set as search query basis (either joined string or provider-native multi-keyword API).
- Retrieve top 5 ranked results with text snippets/metadata needed for optimization context.

If search returns fewer than 5:
- Continue with available results (including zero results), mark fallback path in logs/metadata.

## 6.3 Step C — Final Prompt Optimization (One-shot LLM)

Input context to second one-shot call:
1. `translated_prompt_en`
2. retrieved semantic search context (0..5 items)
3. instruction to produce improved user prompt (clear, specific, execution-ready)

Output:
- `optimized_prompt: string`

Return value:
- optimized prompt text (language behavior described below).

## 6.4 Language Handling Policy

Requested algorithm explicitly translates to English before search/optimization.

Default policy for V1 (implementation recommendation):
- Generate optimized prompt in **original user language if detected confidently**, otherwise English.

Assumption/unknown:
- No confirmed existing language-detection utility was discovered in step context. If unavailable, instruct final LLM call to output in same language as original prompt using original text as reference.

---

## 7. Data Contracts

## 7.1 Request DTO

```json
{
  "sessionId": "string",
  "projectId": "string|null",
  "prompt": "string",
  "topK": 5,
  "includeDebug": false
}
```

Notes:
- `topK` is present for future extensibility but server enforces/overrides to 5 in V1.
- `projectId` may be inferred server-side if session already maps to project; keep nullable for compatibility.

## 7.2 Response DTO

```json
{
  "optimizedPrompt": "string",
  "translatedPromptEn": "string",
  "keywords": ["string"],
  "searchResults": [
    {
      "id": "string",
      "score": 0.0,
      "content": "string",
      "source": "string"
    }
  ],
  "meta": {
    "topKUsed": 5,
    "resultsUsed": 0,
    "fallbackUsed": false,
    "durationMs": 0
  }
}
```

UI usage:
- `optimizedPrompt` is required for replacement.
- Other fields may be omitted from UI consumption unless debug mode is enabled.

## 7.3 Error Contract

Return structured error code + message through desktop/frontend API translation.

Recommended codes:
- `OPTIMIZE_INVALID_ARGUMENT` — empty prompt, missing session/project context.
- `OPTIMIZE_TRANSLATION_FAILED` — first LLM call transport/provider failure.
- `OPTIMIZE_PARSE_FAILED` — malformed JSON/invalid extraction output.
- `OPTIMIZE_NO_KEYWORDS` — keyword extraction produced none.
- `OPTIMIZE_SEARCH_FAILED` — vector search failure.
- `OPTIMIZE_REWRITE_FAILED` — final LLM optimization call failed.
- `OPTIMIZE_TIMEOUT` — exceeded request timeout.
- `OPTIMIZE_CANCELED` — user/system canceled.

Message policy:
- User-facing messages should be concise and safe (no provider secrets, no internal stack traces).
- Detailed diagnostics stay in `slog`.

---

## 8. Sequence Flow (End-to-End)

1. User types prompt in `ChatInput`.
2. User clicks Optimize button.
3. `ChatInput.handleOptimizePrompt` validates text and calls `@/api/chat.optimizePrompt`.
4. API wrapper invokes desktop `App.OptimizePrompt(...)`.
5. Desktop method delegates to backend frontend-API service.
6. Backend performs:
   - LLM call #1 (translate + keywords)
   - semantic search (`top_k=5`)
   - LLM call #2 (final optimized prompt)
7. Backend returns DTO.
8. Frontend receives response and replaces textarea content with `optimizedPrompt`.
9. UI leaves optimize loading state.

---

## 9. Non-Functional Requirements

## 9.1 Latency Targets

Target budgets (desktop local/network mixed):
- p50 ≤ 2.5s
- p95 ≤ 6.0s
- hard timeout: 12s (configurable later)

If timeout occurs:
- return `OPTIMIZE_TIMEOUT`
- keep original text unchanged.

## 9.2 Cancellation Behavior

- User-initiated cancel during optimization should stop further processing when cancellation plumbing exists.
- If full cancellation propagation is unavailable, system must safely ignore stale response and reset loading state.

## 9.3 Logging (`slog`)

Log fields at INFO/DEBUG boundaries:
- `session_id`, `project_id`, `request_id`
- `prompt_len`
- `keywords_count`
- `search_results_count`
- `top_k` (must log `5`)
- per-step latency + total latency
- error code on failure

Do not log full raw prompt by default (privacy/security).

## 9.4 Security / Tool Policy Implications

- Feature uses backend orchestration and existing LLM/vector capabilities only.
- Must respect configured security/tool policies enforced in current build path (`core/builder.go` policy application).
- No new tool execution permissions should be introduced for this feature.

## 9.5 Fallback Behavior

- If semantic search fails: return error (V1 strict mode) OR optionally continue with rewrite from translated prompt only.
- Recommended V1 behavior: **strict failure on search error**, but continue if search returns zero results successfully.

Assumption/unknown:
- Product preference between strict vs soft fallback on search transport failure is not specified; defaulting to strict error improves debuggability.

---

## 10. Edge Cases and Validation Rules

1. Empty/whitespace input -> disabled button and `OPTIMIZE_INVALID_ARGUMENT` guard server-side.
2. Extremely long prompt -> enforce max input length aligned with existing backend limits (unknown exact threshold; must reuse existing limit constant if present).
3. Non-UTF8/invalid chars -> sanitize/normalize before LLM request.
4. LLM returns non-JSON for step A -> parsing retry optional (max 1 retry) else `OPTIMIZE_PARSE_FAILED`.
5. Duplicate rapid clicks -> single-flight via `isOptimizing` lock in component.
6. Session switched mid-flight -> ignore stale response unless session ID still matches request origin.

Unknown to resolve during implementation:
- exact existing request-size limit constants and normalization helper location.

---

## 11. Testing and Verification Plan

## 11.1 Required Commands

Primary repo command:
- `make test`

Optional focused runs during development:
- targeted `go test` for changed backend/core packages
- frontend lint/build checks if UI/TS changes are made (`cd frontend && npm run lint`, optional `npm run build`)

## 11.2 Test Scopes

### Backend/Go unit tests

1. **Desktop API method tests**
   - valid request success path
   - invalid argument rejection
   - backend error mapping to error codes

2. **Backend orchestration tests**
   - happy path with mocked LLM + vector search
   - translation parse failure
   - no keywords
   - search error
   - final rewrite error
   - search returns <5 and ==0 results
   - timeout/cancel path

3. **Contract tests**
   - DTO JSON marshaling/unmarshaling
   - required field presence and `top_k=5` enforcement

### Frontend tests (without adding new framework)

Given AGENTS constraints (no frontend test suite currently):
- Validate via typecheck/lint/build + manual verification checklist.

Manual verification checklist:
1. Optimize button appears in send area with icon + `Optimize prompt` hint.
2. Button disabled on empty input and during blocked states.
3. Clicking button shows loading state and prevents re-click.
4. Success replaces input text.
5. Failure keeps original text and shows error.
6. Session switch / unmount does not inject stale optimized text.

---

## 12. Implementation Plan (Suggested Work Breakdown)

1. Add DTO types in backend/desktop boundary.
2. Add desktop API method on `*desktop.App` in appropriate `desktop/api_*.go`.
3. Implement backend frontend-API orchestration method with two LLM calls + semantic search `top_k=5`.
4. Add frontend API wrapper in `frontend/src/api/chat.ts`.
5. Add UI button + handler/state in `ChatInput.tsx`.
6. Add/adjust logging and error mapping.
7. Add Go tests for desktop/backend layers.
8. Run `make test` and frontend lint/build checks.

---

## 13. Rollout Notes

1. Ship behind a lightweight feature flag if project already has settings gate mechanism; otherwise ship directly in chat mode.
2. Monitor optimization request failure rates and latency via logs.
3. Collect qualitative UX feedback on optimized prompt quality before enabling advanced controls.

---

## 14. Future Extensibility

1. Configurable `top_k` (currently fixed to 5 by requirement).
2. User-selectable optimization style presets (concise, detailed, coding-focused, planning-focused).
3. Language controls (force output language, bilingual output).
4. Optional "preview diff" between original and optimized prompt before replacement.
5. Caching extraction/search context per input hash to reduce repeated latency.
6. Telemetry hooks for prompt quality scoring (if privacy policy allows).

---

## 15. Assumptions and Unknowns (Explicit)

1. Step 1 identified insertion points and layering but did not provide exact existing method signatures for LLM/vector services; implementation must bind to existing concrete interfaces discovered during coding.
2. Exact cancellation propagation mechanism for this new RPC is not confirmed; minimal safe behavior is stale-response ignore + loading reset.
3. Existing frontend error display utility for API wrapper failures must be reused; if multiple patterns exist, follow dominant chat API pattern.
4. Existing maximum prompt length constant/location is unknown; must reuse project-native validation constant once discovered.

These unknowns must be resolved during implementation without violating AGENTS.md architecture constraints.
