import { useState, useEffect } from 'react'
import { ShieldAlert, ShieldCheck, GitCommitHorizontal } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { CommitSuppression } from '@/api/git'

interface CommitSuppressedDialogProps {
  /** The Suppressed payload of the withheld commit; null keeps the dialog closed. */
  suppressed: CommitSuppression | null
  /** Repository path the trust decision would be recorded for (shown, not trusted blindly). */
  repoPath: string
  /** True while the follow-up commit RPC is in flight — disables all actions. */
  isSubmitting: boolean
  /** Trust the repository (TrustGitRepo) and re-run the commit through it. */
  onTrust: () => void
  /**
   * Commit through the hardened baseline (force=true), skipping the
   * repository's hooks/signing. `skipDialog=true` also persists the
   * per-project "don't ask again" flag.
   */
  onForceCommit: (skipDialog: boolean) => void
  /** Dismiss without committing (the commit stays withheld). */
  onCancel: () => void
}

/**
 * Dialog shown when the backend withheld a commit because the untrusted
 * repository arms commit hooks and/or signing (CommitResult.Suppressed).
 *
 * The hardened commit baseline neutralizes hooks and signing — committing
 * through it would silently skip programs the repository installed. Instead
 * of committing silently, this dialog explains what would have run and
 * offers the two explicit ways out:
 *
 * - "Trust this repo & commit" — record the repository as trusted
 *   (its hooks/signing then execute; that is what trust means) and re-run
 *   the commit through it.
 * - "Commit without hooks/signing" — proceed with the hardened commit
 *   (force=true). The optional checkbox persists a per-project "don't ask
 *   again" flag so future suppressed commits in this project go straight
 *   to the hardened commit.
 *
 * Presentational by design: the RPC sequence (trust → re-commit) lives in
 * CommitSection, mirroring BranchDeleteConfirmDialog.
 */
export function CommitSuppressedDialog({
  suppressed,
  repoPath,
  isSubmitting,
  onTrust,
  onForceCommit,
  onCancel,
}: CommitSuppressedDialogProps) {
  const [skipNextTime, setSkipNextTime] = useState(false)

  // Reset the checkbox whenever a fresh suppression opens the dialog, so a
  // previous decision never leaks into the next question.
  useEffect(() => {
    if (suppressed !== null) setSkipNextTime(false)
  }, [suppressed])

  const hooks = suppressed?.hooks ?? []
  const signingRepo = suppressed?.signing_repo === true
  const signingGlobal = suppressed?.signing_global === true

  return (
    <Dialog open={suppressed !== null} onOpenChange={(open) => { if (!open && !isSubmitting) onCancel() }}>
      <DialogContent
        className="sm:max-w-md"
        data-testid={suppressed !== null ? 'commit-suppressed-dialog' : undefined}
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ShieldAlert className="size-4 shrink-0 text-warning" />
            Commit withheld
          </DialogTitle>
          <DialogDescription asChild>
            <div className="space-y-2 text-left">
              <span className="block">
                This repository is not trusted, and a commit here would run programs the repository
                installed. Nothing has been committed yet. What git would run:
              </span>
              <ul className="ml-1 list-none space-y-1 text-xs">
                {hooks.map((hook) => (
                  <li key={hook} className="flex items-center gap-1.5 font-mono">
                    <GitCommitHorizontal className="size-3 shrink-0 text-muted-foreground" />
                    {hook}
                  </li>
                ))}
                {signingRepo && (
                  <li className="flex items-center gap-1.5 font-mono">
                    <GitCommitHorizontal className="size-3 shrink-0 text-muted-foreground" />
                    commit.gpgsign (repo config)
                  </li>
                )}
                {signingGlobal && (
                  <li className="flex items-center gap-1.5 font-mono">
                    <GitCommitHorizontal className="size-3 shrink-0 text-muted-foreground" />
                    commit.gpgsign (global config)
                  </li>
                )}
              </ul>
              <span className="block text-xs">
                Untrusted repositories commit through a hardened baseline that neutralizes hooks
                and signing — the alternative would silently skip them. Trust the repository to
                let its own hooks and signing run, or commit hardened now.
                {repoPath !== '' && (
                  <>
                    {' '}
                    Trusting records <span className="font-mono">{repoPath}</span>.
                  </>
                )}
              </span>
            </div>
          </DialogDescription>
        </DialogHeader>

        {/* Custom footer (not DialogFooter): stacks the checkbox above the
            action row deterministically — DialogFooter's flex-col-reverse
            would place the checkbox below the buttons on narrow panels. */}
        <div className="flex flex-col gap-2">
          <label
            className={cn(
              'flex cursor-pointer items-center gap-2 rounded-md px-1 py-1 text-xs text-muted-foreground',
              isSubmitting && 'cursor-not-allowed opacity-60',
            )}
          >
            <input
              type="checkbox"
              checked={skipNextTime}
              disabled={isSubmitting}
              onChange={(e) => setSkipNextTime(e.target.checked)}
              data-testid="commit-suppressed-skip-checkbox"
              className="size-3.5 accent-primary"
            />
            Don&apos;t ask again for this project (always commit without hooks/signing)
          </label>

          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <Button variant="outline" size="sm" onClick={onCancel} disabled={isSubmitting}>
              Cancel
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onForceCommit(skipNextTime)}
              disabled={isSubmitting}
              title="Commit through the hardened baseline; the repository's hooks and signing are skipped"
              data-testid="commit-suppressed-force-btn"
            >
              Commit without hooks/signing
            </Button>
            <Button
              size="sm"
              onClick={onTrust}
              disabled={isSubmitting}
              title="Mark this repository trusted, then commit (its hooks and signing will run)"
              data-testid="commit-suppressed-trust-btn"
            >
              <ShieldCheck className="size-3.5" />
              Trust this repo &amp; commit
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
