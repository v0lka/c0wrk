// Git-panel focus actions (ADR-080's GitPanelTarget consumption): the
// React-free module that moves the panel's focus target on the backend and
// mirrors the resolved state into gitPanelStore. Components import these
// plain async functions (single instances, no duplicated effects); the
// follow-the-session effect lives in useGitFocusSync, mounted once at the
// App root.

import { logger } from '@/lib/logger'
import { getGitPanelFocus, listProjectWorktrees, setGitPanelFocus } from '@/api/git'
import { getSessionWorkspace } from '@/api/workspace'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useSessionStore } from '@/stores/sessionStore'

/**
 * Applies are serialized through one chain and superseded before they run:
 * a rapid project→session switch queue can otherwise interleave the two
 * getSessionWorkspace/setGitPanelFocus round-trips out of order and land
 * the OLDER target last on the backend. `focusApplySeq` is bumped by every
 * session-default apply; an apply that is no longer the newest skips
 * itself. Explicit worktree switches (focusWorktree) join the same chain —
 * user intent, last click wins, order preserved.
 */
let focusApplySeq = 0
let focusApplyChain: Promise<unknown> = Promise.resolve()

function enqueueApply<T>(run: () => Promise<T>): Promise<T> {
  const result = focusApplyChain.then(run, run)
  focusApplyChain = result.catch(() => undefined)
  return result
}

/**
 * Read the session-default focus target: the ACTIVE session's execution
 * workspace (the managed tree of a managed session, the checkout for a
 * local session). Empty string = no session → the backend default (the
 * project checkout). A failed resolve degrades to the default target —
 * focus-following must never break the panel.
 */
async function sessionFocusTarget(): Promise<string> {
  const sessionId = useSessionStore.getState().activeSessionId
  if (!sessionId) return ''
  try {
    return await getSessionWorkspace(sessionId)
  } catch (err) {
    logger.warn('resolving the active session workspace for the git focus failed', err)
    return ''
  }
}

/**
 * Refresh the mirrored focus state: the resolved focus target and the
 * project's worktree list (the focus switcher's data). Failures are logged
 * and swallowed — a stale mirror never breaks the panel.
 */
export async function refreshGitFocus(): Promise<void> {
  try {
    const [focus, worktrees] = await Promise.all([getGitPanelFocus(), listProjectWorktrees()])
    const store = useGitPanelStore.getState()
    store.setFocus(focus)
    store.setWorktrees(worktrees)
  } catch (err) {
    logger.warn('refreshing the git panel focus failed', err)
  }
}

/**
 * Focus the Git panel on the current session's worktree — the default
 * target, re-applied on every project/session switch (useGitFocusSync) and
 * by the header focus button. Returns true when the apply ran (was not
 * superseded by a newer one).
 */
export async function focusSessionWorkspace(): Promise<boolean> {
  const seq = ++focusApplySeq
  return enqueueApply(async () => {
    if (seq !== focusApplySeq) return false
    const target = await sessionFocusTarget()
    try {
      await setGitPanelFocus(target)
    } catch (err) {
      logger.warn('focusing the git panel on the session worktree failed', err)
    }
    useGitPanelStore.getState().setFocusSessionPath(target || null)
    await refreshGitFocus()
    return true
  })
}

/**
 * Switch the Git panel's focus to an explicit worktree of the active
 * project (from the focus switcher). The backend validates the path and
 * refuses foreign trees. Project/session switches re-apply the session
 * default over any explicit choice.
 */
export async function focusWorktree(path: string): Promise<void> {
  await enqueueApply(async () => {
    try {
      await setGitPanelFocus(path)
    } catch (err) {
      logger.warn('switching the git panel focus failed', err)
      return
    }
    await refreshGitFocus()
  })
}

/** Clear the mirrored focus state (no active project / CHAT mode). */
export function clearGitFocusState(): void {
  const store = useGitPanelStore.getState()
  store.setFocus(null)
  store.setFocusSessionPath(null)
  store.setWorktrees([])
}
