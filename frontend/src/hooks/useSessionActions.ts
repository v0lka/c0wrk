// Shared session CRUD + rename logic for sidebar session lists.
//
// Both the dropdown SessionSelector (CODE mode) and the flat SessionList
// (CHAT mode) need the same operations: create / delete / archive / pin /
// fork / rename. Extracting them here keeps a single source of truth and
// avoids duplicating the API wiring and error handling.
//
// Rename is stateful: callers render the rename input themselves (the
// dropdown replaces itself with an input; the flat list renders the input
// inline in the row). This hook owns only the state + async commit.
//
// Archive/delete are gated on confirmation for busy sessions: a session with
// a running, paused, or unfinished task has its task cancelled by the backend
// first, which is destructive to the resumable state, so we ask first.

import { useState, useRef, useCallback } from 'react'
import type { RefObject } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useTerminalRegistryStore } from '@/stores/terminalRegistryStore'
import { useChatInputStore } from '@/stores/chatInputStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { isSessionBusy } from '@/hooks/useSessionStatusIndicator'
import {
  createSession,
  renameSession,
  archiveSession,
  deleteSession,
  forkSession,
  pinSession,
} from '@/api/sessions'
import { logger } from '@/lib/logger'

/** A destructive action held for user confirmation before execution. */
export interface PendingSessionAction {
  kind: 'archive' | 'delete'
  sessionId: string
  sessionName: string
  /** Current archived state — only meaningful for 'archive' (false = archiving). */
  isArchived: boolean
}

export interface SessionActions {
  /** Last session-creation error, surfaced inline in the list header. */
  createError: string | null
  clearCreateError: () => void
  /** Id of the session currently being renamed, or null. */
  renamingId: string | null
  renameValue: string
  setRenameValue: (value: string) => void
  /** Ref to attach to whichever rename input is currently mounted. */
  renameRef: RefObject<HTMLInputElement | null>
  startRename: (id: string, currentName: string) => void
  commitRename: () => Promise<void>
  cancelRename: () => void
  handleNewSession: () => Promise<void>
  handleDelete: (id: string) => Promise<void>
  handleArchive: (id: string, isArchived: boolean) => Promise<void>
  handlePin: (id: string, isPinned: boolean) => Promise<void>
  handleFork: (id: string) => Promise<void>
  /** Pending destructive action awaiting confirmation, or null. */
  pendingAction: PendingSessionAction | null
  /** Execute the pending action (user confirmed). */
  confirmPendingAction: () => Promise<void>
  /** Dismiss the pending action (user cancelled). */
  cancelPendingAction: () => void
}

export function useSessionActions(): SessionActions {
  const addSession = useSessionStore((s) => s.addSession)
  const removeSession = useSessionStore((s) => s.removeSession)
  const updateSession = useSessionStore((s) => s.updateSession)
  const selectSession = useSessionStore((s) => s.selectSession)

  const [createError, setCreateError] = useState<string | null>(null)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [pendingAction, setPendingAction] = useState<PendingSessionAction | null>(null)
  const renameRef = useRef<HTMLInputElement>(null)

  const handleNewSession = useCallback(async () => {
    setCreateError(null)
    try {
      const session = await createSession()
      addSession(session)
      // A newly created session becomes the visible active one — an explicit
      // activation, so persist it as the project's saved session right away
      // (otherwise an app restart would restore a different session).
      selectSession(session.id, session.project_id)
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      logger.error('Failed to create session:', error)
      setCreateError(message)
    }
  }, [addSession, selectSession])

  const performDelete = useCallback(
    async (id: string) => {
      try {
        await deleteSession(id)
        removeSession(id)
        // The backend stopped this session's terminal PTY in DeleteSession;
        // drop its (now-dead) instance from the app-lifetime registry.
        useTerminalRegistryStore.getState().removeInstances([id])
        // Drop the deleted session's chat-input slice (draft, optimize flag,
        // optimize error) so the per-session map stays bounded.
        useChatInputStore.getState().dropSessions([id])
        // Drop its pending-attachment slice + transient image-error banner
        // for the same reason (namesById stays — committed names remain
        // resolvable for tool cards).
        useAttachmentsStore.getState().dropSessions([id])
      } catch (error) {
        logger.error('Failed to delete session:', error)
      }
    },
    [removeSession],
  )

  const performArchive = useCallback(
    async (id: string, isArchived: boolean) => {
      try {
        await archiveSession(id)
        updateSession(id, { archived: !isArchived })
      } catch (error) {
        logger.error('Failed to archive session:', error)
      }
    },
    [updateSession],
  )

  const handleDelete = useCallback(
    async (id: string) => {
      // Deleting a busy session cancels its task first (destructive to any
      // resumable state), so ask for confirmation before proceeding.
      if (isSessionBusy(id)) {
        const name = useSessionStore.getState().sessions?.find((s) => s.id === id)?.name ?? id
        setPendingAction({ kind: 'delete', sessionId: id, sessionName: name, isArchived: false })
        return
      }
      await performDelete(id)
    },
    [performDelete],
  )

  const handleArchive = useCallback(
    async (id: string, isArchived: boolean) => {
      // Only archiving (not unarchiving) can cancel a running/unfinished task.
      if (!isArchived && isSessionBusy(id)) {
        const name = useSessionStore.getState().sessions?.find((s) => s.id === id)?.name ?? id
        setPendingAction({ kind: 'archive', sessionId: id, sessionName: name, isArchived })
        return
      }
      await performArchive(id, isArchived)
    },
    [performArchive],
  )

  const handlePin = useCallback(
    async (id: string, isPinned: boolean) => {
      try {
        await pinSession(id)
        updateSession(id, { pinned: !isPinned })
      } catch (error) {
        logger.error('Failed to pin session:', error)
      }
    },
    [updateSession],
  )

  const handleFork = useCallback(
    async (id: string) => {
      try {
        const forked = await forkSession(id)
        addSession(forked)
        // Forking activates the fork — persist it as the saved session (same
        // reasoning as handleNewSession).
        selectSession(forked.id, forked.project_id)
      } catch (error) {
        logger.error('Failed to fork session:', error)
      }
    },
    [addSession, selectSession],
  )

  const confirmPendingAction = useCallback(async () => {
    if (!pendingAction) return
    const { kind, sessionId, isArchived } = pendingAction
    setPendingAction(null)
    if (kind === 'delete') {
      await performDelete(sessionId)
    } else {
      await performArchive(sessionId, isArchived)
    }
  }, [pendingAction, performDelete, performArchive])

  const cancelPendingAction = useCallback(() => setPendingAction(null), [])

  const startRename = useCallback((id: string, currentName: string) => {
    setRenamingId(id)
    setRenameValue(currentName)
    setTimeout(() => renameRef.current?.focus(), 50)
  }, [])

  const commitRename = useCallback(async () => {
    if (!renamingId || !renameValue.trim()) {
      setRenamingId(null)
      return
    }
    try {
      await renameSession(renamingId, renameValue.trim())
      updateSession(renamingId, { name: renameValue.trim() })
    } catch (error) {
      logger.error('Failed to rename session:', error)
    }
    setRenamingId(null)
  }, [renamingId, renameValue, updateSession])

  const cancelRename = useCallback(() => setRenamingId(null), [])
  const clearCreateError = useCallback(() => setCreateError(null), [])

  return {
    createError,
    clearCreateError,
    renamingId,
    renameValue,
    setRenameValue,
    renameRef,
    startRename,
    commitRename,
    cancelRename,
    handleNewSession,
    handleDelete,
    handleArchive,
    handlePin,
    handleFork,
    pendingAction,
    confirmPendingAction,
    cancelPendingAction,
  }
}
