// Hardened repositories dialog — opened from Settings → Security via the
// "Hardened repos" button.
//
// Lists the repository roots the user marked hardened through the
// "Untrusted git configuration detected" toast ("Harden" persists them into
// security.harden_git_repos), with a per-entry Remove action. A hardened
// repository is always neutralized and can never become raw-git eligible;
// removing a root returns it to the default intake path (it re-warns on the
// next open if its config is still dangerous). The dialog is read-mostly:
// entries are ADDED only from the toast, where the scanned path is known; here
// stale or mistaken entries get pruned.

import { useCallback, useEffect, useState } from 'react'
import { Loader2, ShieldBan, Trash2 } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { getHardenGitRepos, removeHardenGitRepo } from '@/api/gitConfigRisk'
import { onGlobalEvent } from '@/api/runtime'
import { logger } from '@/lib/logger'

interface HardenReposDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function HardenReposDialog({ open, onOpenChange }: HardenReposDialogProps) {
  // null = loading (distinct from [] = none hardened).
  const [repos, setRepos] = useState<string[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [removing, setRemoving] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    setRepos(null)
    try {
      setRepos(await getHardenGitRepos())
    } catch (err) {
      logger.error('Failed to load hardened repositories:', err)
      setError(err instanceof Error ? err.message : String(err))
      setRepos([])
    }
  }, [])

  // Fresh list on every open — entries can also change via the toast while
  // the dialog is closed. While open, `config:updated` (emitted by the
  // backend after EVERY persisted config mutation, including the toast's
  // "Harden") re-runs the load so entries hardened mid-dialog appear without
  // a close/reopen. The dialog's own Remove also emits it — the redundant
  // reload merely re-syncs with the server truth.
  useEffect(() => {
    if (!open) return
    void load()
    return onGlobalEvent('config:updated', () => void load())
  }, [open, load])

  const handleRemove = useCallback(
    async (path: string) => {
      if (removing !== null) return
      setRemoving(path)
      setError(null)
      try {
        await removeHardenGitRepo(path)
        setRepos((prev) => (prev ?? []).filter((p) => p !== path))
      } catch (err) {
        logger.error('Failed to remove hardened repository:', err)
        setError(err instanceof Error ? err.message : String(err))
      } finally {
        setRemoving(null)
      }
    },
    [removing],
  )

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md" data-testid="harden-repos-dialog">
        <DialogHeader>
          <DialogTitle>Hardened repositories</DialogTitle>
          <DialogDescription>
            Repositories you marked &ldquo;Harden&rdquo; on the &ldquo;untrusted git
            configuration&rdquo; warning. They are always neutralized on every git invocation and
            can never become raw-git eligible — repository-defined hooks, filters and signing never
            run inside c0wrk for them. Removing an entry returns the repository to the default
            intake path (its warning re-appears on the next open if the configuration is still
            dangerous). A repository cannot be both trusted and hardened.
          </DialogDescription>
        </DialogHeader>

        {error && (
          <p className="text-xs text-destructive" data-testid="harden-repos-error">
            {error}
          </p>
        )}

        {repos === null ? (
          <div className="flex items-center justify-center gap-2 py-6">
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            <span className="text-sm text-muted-foreground">Loading…</span>
          </div>
        ) : repos.length === 0 ? (
          <div className="flex flex-col items-center gap-2 py-6 text-center">
            <ShieldBan className="h-6 w-6 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              No hardened repositories. Answer &ldquo;Harden&rdquo; on a git-configuration warning
              and the repository will appear here.
            </p>
          </div>
        ) : (
          <ul className="max-h-72 space-y-1.5 overflow-y-auto pr-1 custom-scrollbar">
            {repos.map((path) => (
              <li
                key={path}
                className="flex items-center justify-between gap-2 rounded-md border border-border bg-card/50 px-2.5 py-1.5"
              >
                <span className="min-w-0 truncate font-mono text-xs" title={path}>
                  {path}
                </span>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label={`Remove ${path}`}
                  onClick={() => void handleRemove(path)}
                  disabled={removing !== null}
                  data-testid={`harden-repo-remove-${repos.indexOf(path)}`}
                >
                  {removing === path ? (
                    <Loader2 className="size-3.5 animate-spin" />
                  ) : (
                    <Trash2 className="size-3.5" />
                  )}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </DialogContent>
    </Dialog>
  )
}
