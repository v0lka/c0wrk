// Side-effect-only hook that moves the Git panel's focus target
// automatically: on every active-project or active-session change it
// re-applies the session-default focus (the active session's execution
// workspace — the managed tree of a managed session, the checkout for a
// local session) on the backend and refreshes the mirrored focus/worktree
// state. Mounted exactly once inside the Git panel (beside
// useGitStatusEvents), so the effect never races a second copy of itself.

import { useEffect } from 'react'
import { clearGitFocusState, focusSessionWorkspace } from '@/lib/gitFocus'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'

export function useGitFocusSync(): void {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const activeSessionId = useSessionStore((s) => s.activeSessionId)

  useEffect(() => {
    if (!activeProjectId) {
      // CHAT / No Project: no git panel target exists at all.
      clearGitFocusState()
      return
    }
    // Both switches move the focus: a project switch resets the backend's
    // relevant tree universe, a session switch changes the default target.
    // The apply is serialized + supersede-guarded inside focusSessionWorkspace,
    // so unmount/switch races cannot land an older target last.
    void focusSessionWorkspace()
  }, [activeProjectId, activeSessionId])
}
