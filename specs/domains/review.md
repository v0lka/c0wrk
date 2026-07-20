# Code Review Domain

> GitHub-PR-style review mode for code changes produced by a session task.

## Overview

The review feature lets the user inspect uncommitted changes (staged + unstaged vs HEAD) in a dedicated review page rendered inside the File Viewer, leave per-hunk and general comments, then either Approve (stage all + close) or Submit (send comments as a follow-up message and auto-loop until approved).

## Components

### Backend (Go)

- **`backend/review/persistence.go`** — `SQLiteReviewStore` persists the review buffer per session. Tables: `review_comments` (general + hunk-scoped, FK→sessions ON DELETE CASCADE) and `review_state` (status: active/submitted/approved).
- **`backend/review/persistence_fork.go`** — `CloneReview`/`CloneReviewTx` copy the review buffer (status + comments) to another session for [session forking](session-lifecycle.md#session-forking). `CloneReviewTx` runs on a caller-supplied transaction so the clone commits or rolls back atomically with the rest of the fork
- **`backend/frontend_api_review.go`** — RPCs: `GetReview`, `SaveReviewGeneralComment`, `SaveReviewHunkComment`, `DeleteReviewComment`, `SetReviewStatus`, `ClearReviewComments`, `ClearReview`, `GetReviewDiff`.
- **`core/workspace/git.go`** — `ParseReviewDiff` parses `git diff -U5 HEAD` into per-file hunk snapshots (`ReviewFileDiff{Path, OldPath?, Hunks[]}`).

### Frontend (React/Zustand)

- **`stores/reviewStore.ts`** — Zustand store with per-session review state (`bySession`), `reviewPageOpen`, `activeReviewSession`, `reviewLoopActive` (persisted), `promptShownForTask` (persisted). Comment data is NOT persisted in the store — it lives in the backend (single source of truth).
- **`api/review.ts`** — RPC wrappers with type guards.
- **`components/review/`** — `ReviewPage` (fetches `GetReviewDiff` on mount), `FileReviewBlock`, `HunkReviewBlock` (diff display + inline comment), `ReviewHeader` (Comment All + Approve/Submit button), `useReviewActions` hook.
- **`components/chat/ReviewPromptBlock.tsx`** — Inline chat block shown after `task_complete` with changes.

### Activation paths

1. **Review button** in `ChangesToolbar` — manual entry (Step 5).
2. **Post-task prompt** — `review_prompt` DisplayItem injected on successful `task_complete` when git status is non-empty and the prompt hasn't been shown for this task (Step 6).

### Lifecycle

```
task_complete (success, has changes)
  ├─ reviewLoopActive? → auto-reopen ReviewPage (no prompt)
  └─ not in loop? → inject review_prompt block (Yes/No)
       ├─ Yes → open ReviewPage + enterReviewLoop
       └─ No → dismiss

ReviewPage:
  ├─ 0 comments → "Approve" → stageAll + ClearReview + close
  └─ ≥1 comment → "Submit" → send comments as a clean user message
       (general + per-hunk, no instruction prefix) via SendMessage with
       reviewMode=true → ClearComments → enterReviewLoop → close
       └─ next task_complete → auto-reopen with fresh diff (loop repeats until Approve)
```

The actionable framing is **not** embedded in the message text. `reviewMode=true`
threads through `Manager.SendMessage` → `core.HandleOptions.ReviewMode` →
`ReviewModeKey` context key → a "Code Review Feedback" section in the Conductor
system prompt (`core/prompts/code_review_mode.md`). That section directs the
agent to treat the user's comments as actionable change requests and edit code,
keeping the displayed user message as the verbatim review comments.

### Persistence

- `reviewLoopActive` and `promptShownForTask` are persisted via zustand persist (localStorage).
- Comment data (general + hunk) is persisted in SQLite via the backend RPCs.
- On session switch, `useReviewRestore` hook reloads comments and reconciles stale loop state.

## RPC contract

| RPC                              | Arguments                          | Returns                  |
| -------------------------------- | ---------------------------------- | ------------------------ |
| `GetReviewDiff`                  | —                                  | `[]ReviewFileDiff`       |
| `GetReview`                      | `sessionId`                        | `*Review`                |
| `SaveReviewGeneralComment`       | `sessionId, body`                  | `void`                   |
| `SaveReviewHunkComment`          | `sessionId, filePath, hunkId, body`| `string` (comment id)    |
| `DeleteReviewComment`            | `id`                               | `void`                   |
| `SetReviewStatus`                | `sessionId, status`                | `void`                   |
| `ClearReviewComments`            | `sessionId`                        | `void`                   |
| `ClearReview`                    | `sessionId`                        | `void`                   |

## Invariants

- `GetReviewDiff` always returns an empty slice (never an error) for no project, non-git, or clean tree.
- The review pseudo-path `c0wrk:review` is intercepted in `FileViewerContent` before file loading.
- Button label is derived from comment count: "Approve" when 0, "Submit" when ≥1.
- `reviewLoopActive` survives app restart; stale loop state (task failed mid-loop) is reconciled on session restore.
- Session forking clones the review buffer onto the fork via `CloneReviewTx`, running inside the fork's transaction so the review copy commits or rolls back with the rest of the fork. A source with no review data is a no-op. The general comment keeps its deterministic id (`generalCommentID`); hunk comments get fresh UUIDs, so the fork owns independent comment rows.
