import { useEffect } from 'react'
import { onNotificationClicked } from '@/api/notifications'
import { useActiveSessionsStore } from '@/stores/activeSessionsStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import { logger } from '@/lib/logger'
import type { SessionInfo } from '@/types/models'

/**
 * System-notification click navigation, mounted ONCE at the app root
 * (App.tsx) — the counterpart to the Go click callback
 * (desktop/notifications.go): the backend focuses the window and emits
 * `notification_clicked`; this hook only navigates.
 *
 * Navigation follows the live-sessions radar's exact pattern
 * (ActiveSessionsIndicator.handleSelect): switch project first when the
 * session lives in another one (switchProjectWithState restores that
 * project's UI state), then select the session.
 *
 * An unattributed banner (empty session id — e.g. the Settings preview) or
 * an unknown session id (not in the global session snapshot, even after one
 * immediate refreshNow) is a logged no-op: the window was already focused
 * by the Go callback, which is all the user asked of that click.
 */
export function useNotificationClicks(): void {
  // Stable across renders (useCallback with [] deps inside), so the effect
  // subscribes exactly once.
  const switchProjectWithState = useProjectSwitchState()

  useEffect(() => {
    return onNotificationClicked((data) => {
      void (async () => {
        if (!data.session_id) {
          logger.debug('[notifications] click without session routing; focus only')
          return
        }

        // Resolve the owning project id: the payload's own value first (our
        // banners always carry it), then the global session snapshot (covers
        // a sender that omits it). The snapshot may be null or stale; one
        // immediate refreshNow covers both before declaring the session
        // unknown. refreshNow never rejects.
        let projectId = data.project_id
        const findSession = (sessions: SessionInfo[] | null): SessionInfo | undefined =>
          sessions?.find((s) => s.id === data.session_id)

        let hit = findSession(useActiveSessionsStore.getState().sessions)
        if (!hit) {
          await useActiveSessionsStore.getState().refreshNow()
          hit = findSession(useActiveSessionsStore.getState().sessions)
        }
        if (hit) {
          projectId = projectId || hit.project_id
        }

        if (!hit || !projectId) {
          logger.info(
            `[notifications] click on unknown session "${data.session_id}"; not navigating`,
          )
          return
        }

        try {
          if (projectId !== useProjectStore.getState().activeProjectId) {
            await switchProjectWithState(projectId)
          }
          useSessionStore.getState().selectSession(data.session_id, projectId)
        } catch {
          // A failed project switch surfaces its own toast globally; nothing
          // else to do — the window focus from the Go callback already
          // happened.
        }
      })()
    })
  }, [switchProjectWithState])
}
