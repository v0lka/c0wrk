// Trusted repositories dialog — opened from Settings → Security via the
// "Trusted repos" button.
//
// Lists the repository roots the user marked trusted through the
// "Untrusted git configuration detected" toast ("Trust this repo" persists
// them into security.trusted_git_repos), with a per-entry Remove action.
// Removing a root re-enables the intake warning for that repository. The
// dialog is read-mostly: entries are ADDED only from the toast, where the
// scanned path is known; here stale or mistaken entries get pruned.

import { useCallback, useEffect, useState } from 'react'
import { FolderGit2, Loader2, Trash2 } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { getTrustedGitRepos, removeTrustedGitRepo } from '@/api/gitConfigRisk'
import { logger } from '@/lib/logger'

interface TrustedReposDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function TrustedReposDialog({ open, onOpenChange }: TrustedReposDialogProps) {
  // null = loading (distinct from [] = none trusted).
  const [repos, setRepos] = useState<string[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [removing, setRemoving] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    setRepos(null)
    try {
      setRepos(await getTrustedGitRepos())
    } catch (err) {
      logger.error('Failed to load trusted repositories:', err)
      setError(err instanceof Error ? err.message : String(err))
      setRepos([])
    }
  }, [])

  // Fresh list on every open — entries can also change via the toast while
  // the dialog is closed.
  useEffect(() => {
    if (open) void load()
  }, [open, load])

  const handleRemove = useCallback(
    async (path: string) => {
      if (removing !== null) return
      setRemoving(path)
      setError(null)
      try {
        await removeTrustedGitRepo(path)
        setRepos((prev) => (prev ?? []).filter((p) => p !== path))
      } catch (err) {
        logger.error('Failed to remove trusted repository:', err)
        setError(err instanceof Error ? err.message : String(err))
      } finally {
        setRemoving(null)
      }
    },
    [removing],
  )

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md" data-testid="trusted-repos-dialog">
        <DialogHeader>
          <DialogTitle>Trusted repositories</DialogTitle>
          <DialogDescription>
            Repositories whose &ldquo;untrusted git configuration&rdquo; warning you dismissed with
            &ldquo;Trust this repo&rdquo;. Removing an entry re-enables the warning for that
            repository. Git subprocess hardening stays active for every repository either way.
          </DialogDescription>
        </DialogHeader>

        {error && (
          <p className="text-xs text-destructive" data-testid="trusted-repos-error">
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
            <FolderGit2 className="h-6 w-6 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              No trusted repositories. Dismiss a git-configuration warning with
              &ldquo;Trust this repo&rdquo; and it will appear here.
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
                  data-testid={`trusted-repo-remove-${repos.indexOf(path)}`}
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
