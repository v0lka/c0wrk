# ADR-062: Hide Legacy `review_prompt` Rows

## Status

Accepted — supersedes D4 of [ADR-060](./060-remove-post-task-review-prompt.md) (legacy `review_prompt` rows are hidden at history load instead of rendering as muted status lines). The rest of ADR-060 stands.

## Context

ADR-060 removed the post-task review prompt and decided that the rows it had already persisted would *degrade gracefully*: they kept their stable ids and rendered as muted `status` service lines through the legacy role map ([rendering.md](../domains/frontend/rendering.md)).

In practice the removal happened while the feature was still active in the running build, so nearly every completed task had already persisted one row — 660 of them in the owner's database, each carrying the backend-owned body **"Uncommitted changes detected in this repository."** Because a row was written *after* `task_complete`, it is the **last** message of the session, so opening such a session shows a stale, meaningless status line at the very end of the chat that reads as if it were fresh output. A row carries no actionable content (its `decision` was already recorded when the prompt was answered) and the feature no longer exists, so surfacing it is pure noise.

## Decision

Drop legacy `review_prompt` rows at **history-load** time instead of rendering them:

1. **Frontend predicate.** `isLegacyReviewPromptRow(msg)` (`frontend/src/lib/chatUtils.ts`) matches `role === 'review_prompt'` on the pre-conversion `ChatMessage`. `ChatArea`'s history-load filter applies it together with the existing `isPersistableHistoryMessage` (the `event_unknown` filter) so the rows are removed before conversion to UI messages.
2. **No migration.** The rows stay in the database; hiding is read-side and transparent, and reversible without data loss.
3. **Defensive fallback kept.** The legacy role-map entry (`review_prompt` → `status`) and the stable-id derivation (`review-prompt-{prompt_id}`) remain so that a row which ever bypasses the filter still converts to a muted line rather than raw content — they are no longer the primary path.

## Consequences

**Positive**

- The stale "Uncommitted changes detected in this repository." line never appears again, including at the tail of sessions created before the removal.
- Consistent with how other non-displayable persisted rows are handled (`event_unknown` dropped at history load via `isPersistableHistoryMessage`).
- No schema change, no data deletion.

**Negative / accepted costs**

- The historical rows are no longer visible in the chat. Acceptable: they carried no actionable content, and their decision was already recorded in the row metadata at the time it was answered.

**Security impact:** none. This changes only what a defunct UI affordance renders; every security policy is untouched.

## Alternatives Considered

- **Filter in the backend (`GetSessionHistory` excludes the role).** Rejected: the frontend already owns legacy-row filtering (event_unknown, the routing/agent_metrics store-state rows), and keeping it there avoids a second filter location and keeps the history RPC faithful to the stored rows.
- **Match on content (`content === "Uncommitted changes detected in this repository."`) instead of role.** Rejected: the role is authoritative and robust to any future rewording of the body.
- **Delete the rows with a migration.** Rejected: an unnecessary destructive migration for a cosmetic fix; hiding at read is transparent and reversible.
- **Keep rendering as muted status lines (ADR-060 D4 as written).** Rejected: the line is emitted at the end of the chat and reads like fresh output, which is exactly the noise the removal set out to eliminate.

## References

- [ADR-060](./060-remove-post-task-review-prompt.md) — the removal (D4 superseded here)
- [specs/domains/frontend/rendering.md](../domains/frontend/rendering.md) — DisplayItem kinds and the legacy `review_prompt` role mapping
- [specs/domains/review.md](../domains/review.md) — the review feature (manual entry + loop)
