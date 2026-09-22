// Shared git-branch CRUD + remote-operation logic for the git panel's branch
// lists.
//
// Extracted so LocalBranchRow / RemoteBranchRow (and their future parent list)
// share a single source of truth for every branch operation: checkout, rename,
// delete (safe/force), merge, rebase, push, checkout-remote and delete-remote.
// Each operation tracks an in-flight (busy) indicator and records its outcome
// (success or failure) via {@link runGitOperation} — the Git panel's operation
// console is the single surface for results. Every mutation goes through
// `@/api/*` wrappers — the backend emits `git:status_changed` after each,
// which `useGitStatusEvents` picks up.
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
import { runGitOperation } from '@/lib/gitOperation'
import type { GitOperationKind } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'

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

/** Record descriptor for a single branch operation. */
interface BranchOperation {
  kind: GitOperationKind
  label: string
}

export interface BranchActions {
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

  // Low-level operations (each wraps one API call with busy/record tracking).
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

  // Single in-flight gate shared by every operation. The operation is run
  // through {@link runGitOperation}, which records its outcome for the active
  // project and never throws — callers observe success via the returned value
  // and via `git:status_changed`/store sync. Returns the wrapped API result on
  // success, or null when the operation was skipped (busy / no active project)
  // or failed.
  const run = useCallback(
    async <T,>(
      branch: string,
      action: BranchActionKind,
      op: BranchOperation,
      fn: () => Promise<T>,
    ): Promise<T | null> => {
      // The Git panel only exists for an active project; without one there is
      // nothing to record against, so skip rather than run unrecorded.
      const projectId = useProjectStore.getState().activeProjectId
      if (!projectId || busyRef.current) return null
      busyRef.current = true
      setBusy({ branch, action })
      try {
        const outcome = await runGitOperation({
          projectId,
          kind: op.kind,
          label: op.label,
          fn,
        })
        return outcome.ok ? outcome.result : null
      } finally {
        busyRef.current = false
        setBusy(null)
      }
    },
    [],
  )

  const checkout = useCallback(
    async (name: string) =>
      (await run(
        name,
        'checkout',
        { kind: 'checkout', label: `Checked out ${name}` },
        () => checkoutBranch(name),
      )) !== null,
    [run],
  )

  const rename = useCallback(
    async (oldName: string, newName: string) =>
      (await run(
        oldName,
        'rename',
        { kind: 'branch-rename', label: `Renamed ${oldName} to ${newName}` },
        () => renameBranch(oldName, newName),
      )) !== null,
    [run],
  )

  const deleteLocal = useCallback(
    async (name: string, force: boolean) =>
      (await run(
        name,
        'delete',
        { kind: 'branch-delete', label: `Deleted branch ${name}` },
        () => deleteBranch(name, force),
      )) !== null,
    [run],
  )

  const mergeBranch = useCallback(
    async (name: string) =>
      (await run(
        name,
        'merge',
        { kind: 'merge', label: `Merged ${name} into current` },
        () => merge(name),
      )) !== null,
    [run],
  )

  const rebaseBranch = useCallback(
    async (name: string) =>
      (await run(
        name,
        'rebase',
        { kind: 'rebase', label: `Rebased current onto ${name}` },
        () => rebase(name),
      )) !== null,
    [run],
  )

  const push = useCallback(
    async (name: string) =>
      run(
        name,
        'push',
        { kind: 'branch-push', label: `Pushed ${name}` },
        () => pushBranch(name),
      ),
    [run],
  )

  const checkoutRemote = useCallback(
    async (remoteBranch: string) =>
      (await run(
        remoteBranch,
        'checkoutRemote',
        { kind: 'branch-checkout-remote', label: `Checked out ${remoteBranch}` },
        () => checkoutRemoteBranch(remoteBranch),
      )) !== null,
    [run],
  )

  const deleteRemote = useCallback(
    async (name: string, remote: string) =>
      run(
        name,
        'deleteRemote',
        { kind: 'branch-delete-remote', label: `Deleted remote branch ${name}` },
        () => deleteRemoteBranch(name, remote),
      ),
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

  return {
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
