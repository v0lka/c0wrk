# Prompt Suggestions

## Purpose

Proactive follow-up prompts for the chat input: when a task finishes successfully (`full`) or partially (`partial`), the backend generates a small set of suggested next prompts and the frontend offers them inside the input field, navigated with `↑`/`↓` and inserted with `→`. Two kinds — task follow-ups (`next_task`, project window of 10 sessions) and goal-advance prompts (`goal_advance`, the triggering session's transcript in v1). The whole subsystem is fail-open: it can never delay, block, or break the chat path. Decisions D1–D9 are recorded in [ADR-088](../decisions/088-prompt-suggestions.md); this document is the behavioral contract.

## Status

Implemented (ADR-088), with v1 deltas recorded in place (each carries an inline **v1 delta** note): the config gate `prompt_suggestions.enabled` (nil = on) shipped as the documented opt-out the ADR had deferred (see § Configuration); accept/dismiss reporting is not wired on the frontend (see § UX contract); generation inputs are simplified — no `<context>` block and a one-session `goal_advance` window (see § Generation inputs); the `is_nudge` stamp set covers resume nudges and live interjections only (see § Generation inputs); navigation clamps, the set survives a send, and `Esc` is not bound (see § UX contract). Generation uses two prompts, one per delivery context (see `core/prompts/` below).

## Key Files

- `backend/session/persistence.go` — the `prompt_suggestions` table + lookup index (schema in ADR-088 D7), CRUD on the shared session store over `core.PromptSuggestion` rows (TEXT↔typed conversion at the SQLite boundary), including the store-level `SetPromptSuggestionStatus` (accept/dismiss persistence; no frontend caller yet)
- `backend/session/prompt_suggest.go` — the trigger: the finish-hook outcome gate (D2), one fail-open generation pass per kind on a tracked background goroutine, atomic persistence of the pass, and the session-scoped `prompt_suggestions` event
- `backend/frontend_api_suggestions.go` — the RPC surface: `GetPromptSuggestions` (restore, routed per § Flow) and `SetPromptSuggestionsEnabled` (the config gate: persist + apply in one call)
- `core/prompt_suggest.go` — the one-shot service call (`ServiceKindPromptSuggest`): input assembly (the D3 window, `is_nudge` filter D5, language directive D6), LLM round-trip, response parsing; owns the canonical `PromptSuggestion` row type the pipeline and the wire share
- `core/prompts/prompt_suggest_continuation.md` + `core/prompts/prompt_suggest_new_session.md` — the two generation prompts (embedded via `prompts.go`; a test pins every `.md` file as referenced): continuation for a session with a transcript, new-session for the project `next_task` hand-off
- `frontend/src/api/suggestions.ts` — RPC wrappers (`@/api/*` only)
- `frontend/src/stores/promptSuggestionsStore.ts` — single-slot active-session suggestion set + position index (`usePromptSuggestions`/`usePromptSelectedIndex` selectors); cleared on session switch/delete
- `frontend/src/hooks/usePromptSuggestionsSync.ts` — refetch wiring: reloads the active session's set on session/project change and on the `prompt_suggestions` event (refetches rather than applies the payload)
- `frontend/src/hooks/useChatInputController.ts` — suggestion mode: the ↑↓→ interception (D8), the hint placeholder
- `frontend/src/components/settings/PromptSuggestionsSettings.tsx` — the Settings → General toggle for the gate

## Core Types

```go
// core/prompt_suggest.go
type SuggestionKind string // "next_task" | "goal_advance"

type PromptSuggestion struct {
    ID        string
    SessionID string
    TaskID    string // triggering task; "" for kind-less/session-level rows
    Kind      SuggestionKind
    Text      string
    Status    string // "active" | "accepted" | "dismissed" | "stale"
    CreatedAt time.Time
}

const (
    nextTaskWindowSessions = 10 // D3; goal_advance reads exactly the
                                // triggering session in v1 (cap 1, D4 delta)
    maxSuggestionsPerPass  = 3  // D7
)
```

```typescript
// frontend/src/types/events.ts — the wire row (GetPromptSuggestions and the
// prompt_suggestions event); mirrors core.PromptSuggestion (the struct lives
// in core/prompt_suggest.go; the backend session store persists it with
// kind/status/created_at as TEXT and converts at the boundary)
interface PromptSuggestionRow {
  id: string; session_id: string; task_id: string
  kind: string; text: string; status: string; created_at: string
}

// frontend/src/stores/promptSuggestionsStore.ts — the single-slot snapshot:
// { sessionId, projectId, source, suggestions: PromptSuggestionRow[],
//   selectedIndex, loading }
// selectedIndex is the ↑↓ selection into suggestions (-1 only for an
// empty set); the store never re-filters or re-routes backend rows.
```

## Flow

```
task finishes ──▶ outcome gate (D2): finish ∈ {full, partial}? ──no──▶ stop (silent)
                          │yes
                          ▼
        one fail-open pass per kind (v1 runs BOTH on every qualifying
        finish — no goal-state kind resolution; the read path routes by
        session content, § Generation inputs):
                          │                          │
             goal_advance │                          │ next_task (real project)
                          ▼                          ▼
        inputs (D3/D4/D5): the triggering      inputs (D3/D4/D5): the last
        session's transcript (is_nudge         10 project sessions by pure
        messages dropped)                      recency, pinned ignored
                                               (is_nudge messages dropped)
                          └─────┬──────┘
                                ▼
              async one-shot service call (D9, ServiceKindPromptSuggest,
              serviceLLMRequestTimeout; language = history language, D6)
                                ▼
              parse → atomic replace: prior active rows of
              (session, kind) → stale; insert ≤ 3 rows (D7)
                                ▼
              session event `prompt_suggestions` (full active set)
                                ▼
        ┌── ChatInput suggestion mode (empty input only, D8) ──┐
        │  ↑ previous   ↓ next   → insert as draft             │
        │  (clamped)    (clamped)                              │
        │  typing clears · Enter never captured                │
        └──────┬───────────────────────────┬───────────────────┘
        inserted row                     next generation
        (draft stays in the input)       (replaces the set)
```

## Behavior

### Triggers (D2)

| Task outcome | Generation? |
| --- | --- |
| `full` | yes |
| `partial` | yes |
| `failed` / `aborted` / `cancelled` | no |
| pause / resume | no (resume continues the same task; finish still gated) |

Exactly one generation pass per triggering task: the outcome gate consumes the finish event once; retries of the bookkeeping path must not double-fire (generation is idempotent per (task, kind) — a second pass replaces the first).

The gate reads the TYPED execution status (`result.Status`), not the completion string: `Cancelled` and `Paused` map to the same "partial" completion as degraded outcomes but finished nothing, so they never trigger (`promptSuggestOutcomeTriggers`, `backend/session/prompt_suggest.go`); only `full` and genuinely-partial finishes do, as one fail-open pass on a tracked background goroutine.

### Generation inputs

| Kind | Window | Filter |
| --- | --- | --- |
| `next_task` | last **10** sessions of the project, pure recency (pinned sessions do not displace recent ones) (D3) | `is_nudge` dropped (D5) |
| `goal_advance` | exactly the **triggering session** (D4 **v1 delta**; the 5-session same-goal window is a deferred extension point) | `is_nudge` dropped (D5) |

**v1 delta:** no `<context>` block is passed — the D3 trajectory-summary and D4 goal-condition inputs are deferred extension points; the transcripts are the whole input. Both passes run on every qualifying finish (the `goal_advance` pass always, the `next_task` pass for real project sessions — the No-Project sentinel is no project); the kind the user sees is routed at READ time by session content: `GetPromptSuggestions` serves a session with a transcript its own `goal_advance` set and an empty/brand-new session the project's freshest `next_task` set.

`is_nudge` (D5): a boolean stamped into the persisted message metadata at ingestion for (a) messages injected by the resume path's `nudge` parameter (`Step.UserNudge`) and (b) live user interjections sent while a task is running (the send path stamps every non-fresh classification — `mergeIsNudgeMetadata`). **v1 delta:** short contentless continuation prompts typed by the user ("continue", "продолжай", kin) are NOT stamped — a fresh classification after a finish is never stamped, so such messages can still enter a window; stamping them needs a content classifier at ingestion and is the open D5 follow-up. The flag is metadata-only — rendering and execution never consult it; only the suggestion input builder drops it. Windows are chronological, project-scoped, and per-session bounded; no embedding retrieval in v1.

### Language (D6)

The generation prompt directs the model to write suggestions in the dominant language of the supplied history. No setting, no detector — the history is the signal.

### UX contract (D8)

Suggestion mode is active **only** when the input is empty AND an active set exists; outside it, all keys keep native behavior.

| Key | In suggestion mode | Effect on data |
| --- | --- | --- |
| `↑` | previous suggestion (clamped at the start of the set) | none |
| `↓` | next suggestion (clamped at the end of the set) | none |
| `→` | insert text into the input as an editable draft; exits mode | none in v1 |
| any typing | clears the chip, normal typing | none |
| `Esc` | not bound in v1 (native behavior) | none |
| `Enter` | never captured (empty input cannot send) | none |

**v1 deltas:** the arrows CLAMP at the ends of the set — they do not wrap (the ADR's "(wraps)" is deferred); the selection always designates the row the placeholder previews — a fresh set rests ON the top row (the resting placeholder shows the proactive prompt), so the first ↓ moves to a visibly different row and ↑ at the top is a no-op clamp (an unselected "-1 rest" would render identically to row 0 and make the first press look dead); **v1 delta:** the placeholder is visually two-part — the previewed suggestion with its muted position marker (`2/3`, `suggestionPositionLabel` → a dimmed suffix span on the preview line — the D8 chip's "text + position"), then a BLANK line, then the italic usage block (`↑↓ browse · → insert` / `Or just type a message…`): the surface is a DOM node (`buildSuggestionPlaceholderNode`, fed to CodeMirror's `placeholder()`, which accepts an `HTMLElement`), because under the wrap's `white-space: pre-line` a `'\n\n'` text run collapses to a single break (text alone cannot render the empty line) and CSS cannot italicize part of a text node; the node is memoized on primitive inputs because the placeholder compartment reconfigures by string value / node identity; `Esc` is not bound — dismissing the set (rows → `dismissed`) awaits the report RPC below; and the set is NOT cleared on send — it stays visible until the next generation replaces it (over the `prompt_suggestions` event) or the session switches, so the freshest available suggestions remain offered while the input sits empty. **v1 delta:** accept/dismiss reporting is not wired on the frontend — `SetPromptSuggestionStatus` exists only at the store level (`backend/session/persistence.go`), so `→` inserts the draft and exits the mode without marking the row `accepted`, and leaving the mode is a UI-only selection reset; the report RPC is the open D7/D8 follow-up. Session open/switch restores the active set via `GetPromptSuggestions`; a fresh set arrives over the session-scoped `prompt_suggestions` event (payload: the full active set for the session, both kinds) and is consumed by refetching, not by applying the payload (`usePromptSuggestionsSync`). The brand-new-session surface rides the session's REAL id: since [ADR-091](../decisions/091-eager-session-creation.md) every "New Session" gesture eagerly creates the session row, and `GetPromptSuggestions` routes a fresh session (empty transcript) straight into the new_session branch — the chat input serves the project set under it (placeholder hint + ↑↓→) exactly as under any empty named session; a No-Project (CHAT) window still resets to nothing. The EMPTY session id survives only as a valid RPC contract shape for a no-active-session caller (the eager flow's transient session-less states — fresh app start, last session deleted): `usePromptSuggestionsSync` addresses those with the empty id, which the backend echoes into the same new_session branch. A session-less surface subscribes to no session event — there is no session row to scope one to — so a generation landing while no session is active becomes visible on the next switch or auto-creation.

## Error Handling

Fail-open at every layer (D9): outcome-gate mismatch, empty window, LLM error/timeout, unparsable response, persistence failure, retrieval failure — each is a Debug log and "no suggestions". The send path and the finish path never wait on, observe, or fail because of this subsystem. A stale `accepted` report for an already-replaced row is a no-op.

## Invariants

- Suggestions are generated only for task outcomes `full` and `partial` — never after `failed`, `aborted`, or `cancelled` (D2).
- At most one `active` suggestion set per (session, kind) exists; a fresh pass atomically stales the previous one (D7).
- `is_nudge` messages never enter a generation window (D5).
- `next_task` never reads beyond 10 sessions (pure recency — pinned sessions do not displace recent ones); `goal_advance` reads exactly the triggering session in v1 (D3/D4).
- Suggestion text matches the language of the history it was generated from (D6).
- `↑`/`↓`/`→` are intercepted only in suggestion mode (empty input + active set); `Enter` is never captured (D8). The arrows clamp at both ends of the set (D8 v1 delta).
- No failure in the subsystem can delay, block, or error a message send or task finish (D9).
- The only config surface is the `prompt_suggestions.enabled` gate (pointer-bool, nil = on); the 10-session window and the per-pass cap (3) remain compile-time constants (D3/D7/D9).

## Configuration

One key: `prompt_suggestions.enabled` — the opt-out the ADR deferred, shipped with the implementation. Pointer-bool per the house convention: the section or key absent reads as **on**, an explicit `false` opts out entirely (the outcome gate never fires — no generation pass, no LLM call, no store write, no event). Documented in `config.example.yaml`; toggled at runtime from Settings → General via `SetPromptSuggestionsEnabled` (persist + apply in one call, no restart); the config read path resolves the effective value through `PromptSuggestionsConfig.SuggestionsEnabled()` (nil = on). The 10-session window, the per-pass cap (3), and the fail-open posture remain compile-time constants — the gate changes nothing about boundedness.

## Extension Points

- **New kind**: add the enum value, a window definition, and a trigger predicate — table, pipeline, and UX are kind-agnostic (D1).
- **The D3/D4 `<context>` inputs**: the triggering task's trajectory summary (next_task) and the goal's success condition (goal_advance) — v1 removed the context plumbing (the prompt bullets and the `PromptSuggestInput` field); re-adding it is a leading render block plus input field, no persistence or UX changes; `goal_advance`'s 5-session same-goal window rides the same extension (D3/D4 v1 deltas).
- **Goal-state kind routing**: resolve the goal kind from the triggering task's goal state instead of always running both passes (D4 v1 delta).
- **`is_nudge` for typed continuations**: stamp short contentless user-typed continuations ("continue", "продолжай") at ingestion via a content classifier (D5 v1 delta).
- **Retrieval-backed inputs**: swap the chronological window for embedding retrieval without touching persistence or UX (rejected for v1, kept cheap to add).
- **Per-kind caps**: `maxSuggestionsPerPass` can become per-kind if telemetry shows one kind wants more surface.
- **D8 completions**: wrap navigation, clear-on-send, `Esc`-dismiss, and the accept/dismiss report RPC (`SetPromptSuggestionStatus` already persists per-row statuses) (D8 v1 deltas).

## Related Specs

- [ADR-088](../decisions/088-prompt-suggestions.md) — decisions D1–D9 this contract implements
- [goal-mode.md](goal-mode.md) — the goal loop `goal_advance` suggestions feed (ADR-019)
- [ADR-086](../decisions/086-goal-mode-suggestion.md) — the blocking sibling: goal-mode activation offers at routing time
- [session-lifecycle.md](session-lifecycle.md) — task outcomes, the resume/nudge path the `is_nudge` stamp rides, persistence layout
- [event-catalog.md](../contracts/event-catalog.md) — the `prompt_suggestions` session event
- [desktop-frontend.md](../contracts/desktop-frontend.md) — the RPC surface the suggestions methods join via embedding
