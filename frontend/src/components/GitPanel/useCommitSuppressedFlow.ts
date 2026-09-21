import { useState } from 'react'
import { useProjectStore } from '@/stores/projectStore'
import { commit, type CommitSuppression } from '@/api/git'
import { trustGitRepo } from '@/api/gitConfigRisk'

/**
 * A commit the backend withheld because the untrusted repository arms
 * commit hooks/signing. The message is captured alongside the suppression
 * payload so the dialog's follow-up actions re-commit EXACTLY what was
 * withheld, even if the draft (or the active project) changed meanwhile.
 */
export interface PendingSuppressedCommit {
  suppression: CommitSuppression
  message: string
  projectId: string
}

interface CommitSuppressedFlowOptions {
  /** Store setter: surface a flow error in the commit box's error slot. */
  setCommitError: (projectId: string, error: string | null) => void
  /** Store setter: record a successful commit (SHA + bounded output). */
  setCommitSuccess: (projectId: string, sha: string | null, output?: string) => void
  /** Store setter: persist the per-project "don't ask again" flag. */
  setSkipCommitSuppress: (projectId: string, skip: boolean) => void
}

/**
 * The suppression-decision flow behind CommitSection's Trust/continue
 * dialog: owns the withheld commit awaiting a decision, and the two ways
 * out of it (trust the repository and re-commit through it, or commit
 * through the hardened baseline with force=true).
 *
 * Every write targets the project captured at dialog-open time
 * (`pending.projectId`), never whichever project happens to be active when
 * an RPC settles — the same capture-at-click-time contract the commit and
 * generate handlers use.
 */
export function useCommitSuppressedFlow({
  setCommitError,
  setCommitSuccess,
  setSkipCommitSuppress,
}: CommitSuppressedFlowOptions) {
  // Direct store reference (null | ProjectInfo[]) — selector-stable.
  const projects = useProjectStore((s) => s.projects)

  // The withheld commit awaiting a decision (drives the dialog). Null when
  // the last commit attempt was not suppressed.
  const [pendingSuppressed, setPendingSuppressed] = useState<PendingSuppressedCommit | null>(null)
  // True while a dialog action's RPC sequence (trust→commit or force
  // commit) is in flight — disables the dialog's buttons.
  const [isDialogSubmitting, setIsDialogSubmitting] = useState(false)

  // The workspace path the dialog's trust action targets: the project the
  // withheld commit belonged to (captured at dialog-open time), not the
  // currently active one — a mid-dialog project switch must not re-target
  // the trust decision at a different repository.
  const pendingWorkspacePath = pendingSuppressed
    ? projects?.find((p) => p.id === pendingSuppressed.projectId)?.workspace_path ?? ''
    : ''

  /** Queue a freshly withheld commit for the dialog (or clear with null). */
  const setSuppressed = setPendingSuppressed

  /**
   * Dialog action: mark the repository trusted (TrustGitRepo with the
   * withheld project's workspace path — the backend normalizes it to the
   * repository work-tree root), then re-run the commit through it. Trust
   * means the repository's own hooks and signing EXECUTE.
   */
  const handleTrustAndCommit = async () => {
    const pending = pendingSuppressed
    if (pending === null || isDialogSubmitting) return
    const workspacePath =
      projects?.find((p) => p.id === pending.projectId)?.workspace_path ?? ''
    if (workspacePath === '') {
      setPendingSuppressed(null)
      setCommitError(pending.projectId, 'Cannot trust: project workspace path is unavailable')
      return
    }
    setIsDialogSubmitting(true)
    try {
      await trustGitRepo(workspacePath)
      const result = await commit(pending.message, false)
      if (result.suppressed) {
        // Still withheld (e.g. the workspace sits inside a larger work tree
        // the trust decision did not cover): keep the dialog open so the
        // user can fall back to the hardened commit explicitly.
        setPendingSuppressed({
          suppression: result.suppressed,
          message: pending.message,
          projectId: pending.projectId,
        })
        return
      }
      setPendingSuppressed(null)
      setCommitSuccess(pending.projectId, result.sha ?? '', result.output ?? '')
    } catch (err) {
      // Trust RPC or the re-commit failed: close the dialog and surface the
      // error in the commit box's error slot (nothing was committed).
      setPendingSuppressed(null)
      setCommitError(pending.projectId, err instanceof Error ? err.message : 'Commit failed')
    } finally {
      setIsDialogSubmitting(false)
    }
  }

  /**
   * Dialog action: commit through the hardened baseline (force=true) — the
   * repository's hooks and signing are skipped. With `skipDialog` the
   * per-project "don't ask again" flag is persisted first, so future
   * suppressed commits in this project commit hardened without the dialog.
   */
  const handleForceCommit = async (skipDialog: boolean) => {
    const pending = pendingSuppressed
    if (pending === null || isDialogSubmitting) return
    if (skipDialog) setSkipCommitSuppress(pending.projectId, true)
    setIsDialogSubmitting(true)
    try {
      // force=true: the backend commits hardened and never suppresses.
      const result = await commit(pending.message, true)
      setPendingSuppressed(null)
      setCommitSuccess(pending.projectId, result.sha ?? '', result.output ?? '')
    } catch (err) {
      setPendingSuppressed(null)
      setCommitError(pending.projectId, err instanceof Error ? err.message : 'Commit failed')
    } finally {
      setIsDialogSubmitting(false)
    }
  }

  /** Dialog action: dismiss without committing (the commit stays withheld). */
  const handleCancel = () => setPendingSuppressed(null)

  return {
    pendingSuppressed,
    pendingWorkspacePath,
    isDialogSubmitting,
    setSuppressed,
    handleTrustAndCommit,
    handleForceCommit,
    handleCancel,
  }
}
