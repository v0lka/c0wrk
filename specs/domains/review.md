# Code Review Domain

> GitHub-PR-style review mode for code changes produced by a session task.

## Overview

The review feature lets the user inspect uncommitted changes (staged + unstaged vs HEAD) in a dedicated review page rendered inside the File Viewer, leave per-hunk and general comments, then either Approve (stage all + close) or Submit (send comments as a follow-up message and auto-loop until approved).

## Components

### Backend (Go)

- **`backend/review/persistence.go`** — `SQLiteReviewStore` persists the review buffer per session. Tables: `review_comments` (general, hunk-scoped, and file-scoped comments; FK→sessions ON DELETE CASCADE) and `review_state` (status: active/submitted/approved).
- **`backend/review/persistence_fork.go`** — `CloneReview`/`CloneReviewTx` copy the review buffer (status + comments) to another session for [session forking](session-lifecycle.md#session-forking). `CloneReviewTx` runs on a caller-supplied transaction so the clone commits or rolls back atomically with the rest of the fork
- **`backend/frontend_api_review.go`** — RPCs: `GetReview`, `SaveReviewGeneralComment`, `SaveReviewHunkComment`, `SaveReviewFileComment`, `DeleteReviewComment`, `SetReviewStatus`, `ClearReviewComments`, `ClearReview`, `GetReviewDiff`, `GetCommitDiff`.
- **`core/workspace/git.go`** — `BuildReviewDiff` combines `git diff -U5 HEAD` (staged + unstaged tracked changes) with a per-file `git diff --no-index` for every untracked file; `ParseReviewDiff` parses the combined output into per-file hunk snapshots. The wire types (`ReviewFileDiff{Path, OldPath?, Hunks[]}`, `ReviewHunk{Raw, OldStart, OldCount, NewStart, NewCount}`) live in `core/workspace/types.go`.

### Frontend (React/Zustand)

- **`stores/reviewStore.ts`** — Zustand store with the in-memory per-session review state (`bySession`: status, general comment, hunk/file comments, `loaded`), `reviewPageOpen`, `activeReviewSession`, and the two PERSISTED slices (`reviewLoopActive` per session and `diffViewMode`, `'unified' | 'split'`; persist key `c0wrk-review`, whose `migrate` drops the removed `promptShownForTask` key). Comment data is NOT persisted in the store — it lives in the backend (single source of truth).
- **`api/review.ts`** — RPC wrappers with type guards.
- **`components/review/`** — `ReviewPage` (fetches `GetReviewDiff` on mount and re-fetches it, coalesced, on `git:status_changed`/`workspace:tree_changed`; with a `commitSha` it renders the read-only `GetCommitDiff` view, which never re-fetches), `FileReviewBlock`, `HunkReviewBlock` (diff display + inline comment, picking the table by `diffViewMode`), `UnifiedDiffTable`/`SplitDiffTable` (the two diff renderers), `HunkComboBox` (the hunk jump list), `ReviewHeader` (Comment All, the `DiffViewModeToggle`, and the Approve/Submit button), the `useReviewActions` hook, and the pure helpers `diffParsing.ts` (the `sameReviewDiff` re-fetch no-op guard) and `hunkCodeHighlight.ts`.
- **`hooks/events/useChatEvents.ts`** — the `task_complete` handler that performs the review-loop reopen (the one post-task review hook the frontend owns; silent mode suppresses it).
- **`hooks/events/useReviewRestore.ts`** — the session-switch reload of the review buffer and the stale-loop reconciliation.

### Activation paths

1. **Review button** in `ChangesToolbar` — the only entry point into the interactive review loop (manual): it opens the `c0wrk:review` pseudo-path tab, loads the session's review buffer, and sets the backend status to `active`.
2. **Commit SHA click** in the Git panel's History tab (`GitHistoryTab`) — the read-only commit review: the `c0wrk:commit:<sha>` pseudo-path renders `ReviewPage` in commit mode (no session, no comments, no loop, no re-fetch).

The automatic post-task prompt (`review_prompt` card injected on `task_complete`) was removed by [ADR-060](../decisions/060-remove-post-task-review-prompt.md): it fired on nearly every task against a perpetually dirty tree and trained the user to dismiss it unread. The one automatic behavior that remains is the **review-loop reopen** below, which only applies while the user is already in a review loop. Legacy `review_prompt` rows persisted before the removal are dropped at history load ([ADR-062](../decisions/062-hide-legacy-review-prompt-rows.md)) by `isLegacyReviewPromptRow` (`frontend/src/lib/chatUtils.ts`) through the filter in `frontend/src/lib/sessionHistoryReload.ts`; a row that ever bypasses the filter still converts to the muted `status` service line with its stable `review-prompt-{prompt_id}` id.

### Lifecycle

```
Manual entry: ChangesToolbar Review button → open ReviewPage (c0wrk:review)

Read-only commit review: Git History SHA click → ReviewPage (c0wrk:commit:<sha>)

task_complete (success, CODE project, has changes)
  └─ reviewLoopActive && !silent mode? → auto-reopen ReviewPage with fresh diff
       (no prompt; silent mode performs no post-task UI interception at all)

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

- `reviewLoopActive` is persisted via zustand persist (localStorage).
- Comment data (general + hunk) is persisted in SQLite via the backend RPCs.
- On session switch, `useReviewRestore` hook reloads comments and reconciles stale loop state.

## RPC contract

| RPC                              | Arguments                          | Returns                  |
| -------------------------------- | ---------------------------------- | ------------------------ |
| `GetReviewDiff`                  | —                                  | `[]ReviewFileDiff`       |
| `GetCommitDiff`                  | `sha`                              | `[]ReviewFileDiff`       |
| `GetReview`                      | `sessionId`                        | `*Review`                |
| `SaveReviewGeneralComment`       | `sessionId, body`                  | `void`                   |
| `SaveReviewHunkComment`          | `sessionId, filePath, hunkId, body`| `string` (comment id)    |
| `SaveReviewFileComment`          | `sessionId, filePath, body`        | `string` (comment id)    |
| `DeleteReviewComment`            | `id`                               | `void`                   |
| `SetReviewStatus`                | `sessionId, status`                | `void`                   |
| `ClearReviewComments`            | `sessionId`                        | `void`                   |
| `ClearReview`                    | `sessionId`                        | `void`                   |

> The former `SaveReviewPrompt` RPC was removed by [ADR-060](../decisions/060-remove-post-task-review-prompt.md) together with the post-task prompt it persisted.

## Invariants

- `GetReviewDiff` always returns an empty slice (never an error) for no project, non-git, or clean tree.
- The review pseudo-path `c0wrk:review` is intercepted in `FileViewerContent` before file loading.
- The commit-review pseudo-path `c0wrk:commit:<sha>` is intercepted in the same place and renders `ReviewPage` read-only: the diff comes from `GetCommitDiff`, the header shows the commit SHA with no comment/approve/submit controls, and the working-tree refetch path stays inert (a commit diff is immutable).
- `diffViewMode` is one app-wide value (`'unified'` default) shared by every review surface, and it survives app restart in the same persisted slice as `reviewLoopActive`; both diff tables render the same highlighted lines.
- Button label is derived from comment count: "Approve" when 0, "Submit" when ≥1.
- `reviewLoopActive` survives app restart; stale loop state (task failed mid-loop) is reconciled on session restore.
- Session forking clones the review buffer onto the fork via `CloneReviewTx`, running inside the fork's transaction so the review copy commits or rolls back with the rest of the fork. A source with no review data is a no-op. General and file comments keep their deterministic ids (`generalCommentID` / `fileCommentID`); hunk comments get fresh UUIDs, so the fork owns independent comment rows.
