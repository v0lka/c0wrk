// Shared git-branch CRUD + remote-operation logic for the git panel's branch
// lists.
//
// Extracted so LocalBranchRow / RemoteBranchRow (and their future parent list)
// share a single source of truth for every branch operation: checkout, rename,
// delete (safe/force), merge, rebase, push, checkout-remote and delete-remote.
// Each operation tracks an in-flight (busy) indicator and a shared error, and
// every mutation goes through `@/api/*` wrappers — the backend emits
// `git:status_changed` after each, which `useGitStatusEvents` picks up.
//
// Rename is stateful (mirrors `useSessionActions`): the caller renders the
// rename input inline and this hook owns the value + async commit.
//
// Delete is gated on confirmation (mirrors `SessionActionConfirmDialog`): a
// local delete asks the user to pick `-d` (safe) vs `-D` (force); a remote
// delete just asks for a single confirm.

import { useState, useRef, useCallback } from 'react'
import type { RefObject } from 'react'
import {
  checkoutBranch,
  renameBranch,
  deleteBranch,
  merge,
  rebase,
  pushBranch,
  checkoutRemoteBranch,
  deleteRemoteBranch,
} from '@/api/git'
import { logger } from '@/lib/logger'

/** The branch operation currently in flight (drives per-row spinners). */
export type BranchActionKind =
  | 'checkout'
  | 'rename'
  | 'delete'
  | 'merge'
  | 'rebase'
  | 'push'
  | 'checkoutRemote'
  | 'deleteRemote'

/** A destructive branch delete held for user confirmation before execution. */
export type PendingBranchDelete =
  | { kind: 'local'; name: string }
  | { kind: 'remote'; name: string; remote: string }

export interface BranchActions {
  /** Last action error, surfaced by the parent (inline banner). */
  error: string | null
  clearError: () => void
  /** Last successful remote-operation output (e.g. `git push` progress). */
  output: string | null
  clearOutput: () => void
  /** Name of the branch currently being operated on, or null. */
  busyBranch: string | null
  /** The action currently in-flight on `busyBranch`, or null. */
  busyAction: BranchActionKind | null
  /** True while any action is in-flight. */
  isBusy: boolean

  // Inline rename state (the caller renders the input).
  renamingBranch: string | null
  renameValue: string
  setRenameValue: (value: string) => void
  renameRef: RefObject<HTMLInputElement | null>
  startRename: (branch: string) => void
  commitRename: () => Promise<void>
  cancelRename: () => void

  // Low-level operations (each wraps one API call with busy/error tracking).
  // Operations that only report success/failure return a boolean; `push` and
  // `deleteRemote` return the backend's combined stdout+stderr (or null).
  checkout: (name: string) => Promise<boolean>
  rename: (oldName: string, newName: string) => Promise<boolean>
  deleteLocal: (name: string, force: boolean) => Promise<boolean>
  mergeBranch: (name: string) => Promise<boolean>
  rebaseBranch: (name: string) => Promise<boolean>
  push: (name: string) => Promise<string | null>
  checkoutRemote: (remoteBranch: string) => Promise<boolean>
  deleteRemote: (name: string, remote: string) => Promise<string | null>

  // Delete confirmation flow.
  pendingDelete: PendingBranchDelete | null
  requestDeleteLocal: (name: string) => void
  requestDeleteRemote: (name: string, remote: string) => void
  confirmDelete: (mode: 'safe' | 'force') => Promise<void>
  cancelDelete: () => void
}

export function useBranchActions(): BranchActions {
  const [error, setError] = useState<string | null>(null)
  const [output, setOutput] = useState<string | null>(null)
  const [busy, setBusy] = useState<{ branch: string; action: BranchActionKind } | null>(null)
  const [renamingBranch, setRenamingBranch] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [pendingDelete, setPendingDelete] = useState<PendingBranchDelete | null>(null)
  const renameRef = useRef<HTMLInputElement>(null)
  // Synchronous re-entrancy guard. `busy` state updates are async, so a
  // second call in the same tick (e.g. Enter + blur both committing a rename)
  // could otherwise slip past the `isBusy` check and run two git operations
  // concurrently — the second would fail spuriously (already-renamed/checked-
  // out branch). `busyRef` is set/cleared synchronously around the await.
  const busyRef = useRef(false)

  // Single in-flight gate shared by every operation. Errors are captured in
  // `error` (and logged) rather than thrown, matching the existing git/session
  // action hooks — callers observe success via the returned value and via
  // `git:status_changed`/store sync. Returns the wrapped API result on
  // success, or null when the operation was skipped (busy) or failed.
  const run = useCallback(
    async <T,>(branch: string, action: BranchActionKind, fn: () => Promise<T>): Promise<T | null> => {
      if (busyRef.current) return null
      busyRef.current = true
      setBusy({ branch, action })
      setError(null)
      setOutput(null)
      try {
        return await fn()
      } catch (err) {
        const message =
          err instanceof Error ? err.message : `Failed to ${action} ${branch}`
        logger.error(`branch action ${action} failed:`, err)
        setError(message)
        return null
      } finally {
        busyRef.current = false
        setBusy(null)
      }
    },
    [],
  )

  const checkout = useCallback(
    async (name: string) => (await run(name, 'checkout', () => checkoutBranch(name))) !== null,
    [run],
  )

  const rename = useCallback(
    async (oldName: string, newName: string) =>
      (await run(oldName, 'rename', () => renameBranch(oldName, newName))) !== null,
    [run],
  )

  const deleteLocal = useCallback(
    async (name: string, force: boolean) =>
      (await run(name, 'delete', () => deleteBranch(name, force))) !== null,
    [run],
  )

  const mergeBranch = useCallback(
    async (name: string) => (await run(name, 'merge', () => merge(name))) !== null,
    [run],
  )

  const rebaseBranch = useCallback(
    async (name: string) => (await run(name, 'rebase', () => rebase(name))) !== null,
    [run],
  )

  const push = useCallback(
    async (name: string) => {
      const out = await run(name, 'push', () => pushBranch(name))
      if (out) setOutput(out)
      return out
    },
    [run],
  )

  const checkoutRemote = useCallback(
    async (remoteBranch: string) =>
      (await run(remoteBranch, 'checkoutRemote', () => checkoutRemoteBranch(remoteBranch))) !== null,
    [run],
  )

  const deleteRemote = useCallback(
    async (name: string, remote: string) => {
      const out = await run(name, 'deleteRemote', () => deleteRemoteBranch(name, remote))
      if (out) setOutput(out)
      return out
    },
    [run],
  )

  // --- Inline rename flow ---

  const startRename = useCallback((branch: string) => {
    setRenamingBranch(branch)
    setRenameValue(branch)
    setTimeout(() => renameRef.current?.focus(), 50)
  }, [])

  const commitRename = useCallback(async () => {
    if (!renamingBranch) {
      setRenamingBranch(null)
      return
    }
    const trimmed = renameValue.trim()
    if (trimmed && trimmed !== renamingBranch) {
      await rename(renamingBranch, trimmed)
    }
    setRenamingBranch(null)
  }, [renamingBranch, renameValue, rename])

  const cancelRename = useCallback(() => setRenamingBranch(null), [])

  // --- Delete confirmation flow ---

  const requestDeleteLocal = useCallback((name: string) => {
    setPendingDelete({ kind: 'local', name })
  }, [])

  const requestDeleteRemote = useCallback((name: string, remote: string) => {
    setPendingDelete({ kind: 'remote', name, remote })
  }, [])

  const confirmDelete = useCallback(
    async (mode: 'safe' | 'force') => {
      if (!pendingDelete) return
      const pending = pendingDelete
      setPendingDelete(null)
      if (pending.kind === 'local') {
        await deleteLocal(pending.name, mode === 'force')
      } else {
        await deleteRemote(pending.name, pending.remote)
      }
    },
    [pendingDelete, deleteLocal, deleteRemote],
  )

  const cancelDelete = useCallback(() => setPendingDelete(null), [])
  const clearError = useCallback(() => setError(null), [])
  const clearOutput = useCallback(() => setOutput(null), [])

  return {
    error,
    clearError,
    output,
    clearOutput,
    busyBranch: busy?.branch ?? null,
    busyAction: busy?.action ?? null,
    isBusy: busy !== null,
    renamingBranch,
    renameValue,
    setRenameValue,
    renameRef,
    startRename,
    commitRename,
    cancelRename,
    checkout,
    rename,
    deleteLocal,
    mergeBranch,
    rebaseBranch,
    push,
    checkoutRemote,
    deleteRemote,
    pendingDelete,
    requestDeleteLocal,
    requestDeleteRemote,
    confirmDelete,
    cancelDelete,
  }
}
