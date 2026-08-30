# ADR-030: Session context restore — saved-first, effective activity, restart context memory

## Status

Accepted

## Context

Each context (CHAT mode = the `__no_project__` pseudo-project, or a CODE project) has exactly one active session. Three long-standing behaviors made the app restore the *wrong* session/context:

1. **Restore was latest-first, not saved-first.** `project_ui_state.saved_session_id` was written only when switching *away* from a project, so on app restart it was stale by construction. The frontend restore (`useProjectSwitchState`) therefore deliberately preferred "latest session by activity", leaving the saved branch effectively dead — the session the user actually had open was never the thing restored.
2. **"Latest" was not activity.** `sessions.last_active_at` was bumped only by `FrontendAPI.SendMessage`, so ordering reflected the last user send, not the last session event (assistant output, task completion, terminal command). All timestamps are RFC3339 seconds, so ties were common and resolved arbitrarily (unspecified SQLite order → the frontend's first-max reduce). Archived sessions were never filtered out of auto-selection either.
3. **Restart forgot the context.** Startup was hard-coded CODE-first (most recently active real project); the mode (CHAT vs CODE) and the exact project/session active at exit were not persisted anywhere.

User requirement: every mode and project remembers which session was open in it, restores exactly that session on switch, and the app restores the exact context (mode + project + session) on restart; "latest" fallback must mean the newest *event* in the session; deleted/archived remembered sessions fall back to latest-live, then a new session.

## Decision

1. **Persist the open session at selection time.** Every explicit user pick (`SessionList`/`SessionSelector`) goes through `sessionStore.selectSession`, which sets the active ID and fires `SaveProjectActiveSession(projectID, sessionID)` — a targeted RPC that updates ONLY `saved_session_id` (never clobbering viewer-owned `open_tabs`/`active_file`). Restore paths use `setActiveSessionId` and never echo back. The switch-away snapshot is kept as a second writer.
2. **Restore saved-first, archived-aware, in one shared resolver.** Both frontend restore entry points (`useProjectSwitchState`, `useSessionLoader`) and the backend fallback chain resolve: saved session (exists, not archived) → latest non-archived by activity → create new. Archived sessions are never auto-selected, even with the freshest activity; list RPCs keep returning them (the sidebar needs them for unarchive).
3. **Effective activity is computed read-side.** `ListSessions`/`ListSessionsByProject` expose `SessionInfo.last_active_at` as `MAX(last session_messages.created_at, last terminal_commands.created_at) → stored last_active_at → created_at`, ordered `pinned DESC, effective_activity DESC, created_at DESC, id ASC` for determinism. The stored column and its write path are unchanged — no migration, retroactively correct for existing databases.
4. **Restart restores the exact context.** `SwitchProject` persists the destination project id (including `__no_project__`) as `last_active_project_id` in a new `app_state` KV table. On startup `useProjectLoader` reopens it when the project still exists (CHAT included); otherwise it degrades to the previous CODE-first heuristic, then the Create Project dialog. The restore RPC is best-effort and never blocks startup.

## Consequences

- Switching modes/projects and restarting the app now reproduce exactly the session/context the user left; the saved pointer is authoritative because it is written when the selection happens, not when it ends.
- "Latest" fallback is truthful (newest event) and deterministic (explicit tie-breaks); a terminal-only session ranks by its real usage.
- Session-only selection changes can no longer wipe persisted viewer tabs (previously possible through the full-state upsert).
- Costs: an extra fire-and-forget RPC per user session pick (negligible, best-effort); effective-activity computation adds two index-backed scalar subqueries per listed session (session counts are modest); `app_state` is a new schema object to reason about (single key today).
- Startup is no longer CODE-first by default — users who quit in CHAT land in CHAT. Fresh installs (no `app_state` row, no real projects) keep the original Create Project onboarding.

## Alternatives Considered

- **Write-side activity maintenance** (bump `last_active_at` from `EventPersister.Persist`, throttled): keeps reads trivial, but adds a hot write path, needs throttling state, and leaves all existing databases stale until each session's next event. Read-side computation is always truthful and retroactive.
- **Millisecond/nanosecond timestamp migration** to kill second-precision ties: mixed-precision TEXT comparison is fragile across old/new rows; deterministic tie-breakers (`created_at DESC, id ASC`) achieve stable ordering without a migration.
- **Persist last-active context in `window_state.json`** (existing desktop-layer file): window geometry is a desktop concern written on resize/shutdown; the project/session context is backend domain state already living in SQLite — a KV table keeps one persistence story and transactional proximity to the data it references.
- **Keep CODE-first startup**: rejected — it violates the core requirement (quit in CHAT → restart in CHAT); it remains the fallback when nothing valid was persisted.
- **Restore archived saved sessions**: rejected per product decision — archiving is an explicit "put away"; auto-selection must skip archived rows everywhere.
