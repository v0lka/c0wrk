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

import { useState, useRef, useCallback } from 'react'
import type { RefObject } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import {
  createSession,
  renameSession,
  archiveSession,
  deleteSession,
  forkSession,
  pinSession,
} from '@/api/sessions'
import { logger } from '@/lib/logger'

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
}

export function useSessionActions(): SessionActions {
  const addSession = useSessionStore((s) => s.addSession)
  const removeSession = useSessionStore((s) => s.removeSession)
  const updateSession = useSessionStore((s) => s.updateSession)
  const setActiveSessionId = useSessionStore((s) => s.setActiveSessionId)

  const [createError, setCreateError] = useState<string | null>(null)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const renameRef = useRef<HTMLInputElement>(null)

  const handleNewSession = useCallback(async () => {
    setCreateError(null)
    try {
      const session = await createSession()
      addSession(session)
      setActiveSessionId(session.id)
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      logger.error('Failed to create session:', error)
      setCreateError(message)
    }
  }, [addSession, setActiveSessionId])

  const handleDelete = useCallback(
    async (id: string) => {
      try {
        await deleteSession(id)
        removeSession(id)
      } catch (error) {
        logger.error('Failed to delete session:', error)
      }
    },
    [removeSession],
  )

  const handleArchive = useCallback(
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
        setActiveSessionId(forked.id)
      } catch (error) {
        logger.error('Failed to fork session:', error)
      }
    },
    [addSession, setActiveSessionId],
  )

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
  }
}
