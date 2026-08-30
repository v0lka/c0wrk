// Session restore — picks which session to activate when a project becomes
// active (project switch, mode toggle, or startup activation).
//
// Restore order:
//  1) The project's saved session (saved_session_id). It is persisted on
//     every explicit user selection (sessionStore.selectSession →
//     saveProjectActiveSession) and when switching away from a project, so it
//     is fresh and authoritative — it wins even over sessions with newer
//     activity. It is valid only while it still exists in the project's
//     session list and is not archived.
//  2) The latest NON-archived session by activity — the fallback for an
//     empty, deleted, or archived saved entry. Archived sessions are never
//     restore candidates, even when they carry the freshest activity
//     timestamp (the backend list RPCs do not filter them out).
//  3) null — the caller creates a new session (empty project).

import type { SessionInfo } from '@/types/models'

function getSessionActivityMs(session: SessionInfo): number {
  const timestamp = Date.parse(session.last_active_at || session.created_at)
  return Number.isNaN(timestamp) ? 0 : timestamp
}

/**
 * Latest session by activity among non-archived candidates. Iterates all
 * sessions rather than trusting list order. Returns null when no restorable
 * (non-archived) session exists.
 */
export function pickLatestRestorableSession(sessions: SessionInfo[]): SessionInfo | null {
  let latest: SessionInfo | null = null
  for (const session of sessions) {
    if (session.archived) continue
    if (latest === null || getSessionActivityMs(session) > getSessionActivityMs(latest)) {
      latest = session
    }
  }
  return latest
}

/**
 * Resolves the session to restore for a project: the saved session when it
 * still exists and is not archived, otherwise the latest non-archived
 * session by activity, otherwise null (caller creates a new session).
 */
export function resolveRestoreSession(sessions: SessionInfo[], savedId: string): SessionInfo | null {
  if (savedId) {
    const saved = sessions.find((session) => session.id === savedId)
    if (saved && !saved.archived) {
      return saved
    }
  }
  return pickLatestRestorableSession(sessions)
}
