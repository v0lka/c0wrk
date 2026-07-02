import { useState, useMemo, useCallback, useEffect, useRef } from 'react'
import {
  GitBranch,
  Check,
  Plus,
  Loader2,
  Search,
  AlertCircle,
} from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { getBranches, checkoutBranch, createBranch } from '@/api/git'
import { cn } from '@/lib/utils'

/**
 * Modal picker for switching and creating local git branches.
 *
 * - Opens when `gitPanelStore.isBranchPickerOpen` is true.
 * - Lists local branches (from GetBranches), highlighting the current one.
 * - Provides a filter input to narrow the list by name.
 * - Provides a "New Branch" field + button that calls CreateBranch and
 *   checks out the new branch immediately.
 * - Clicking a branch calls CheckoutBranch and closes the picker. The
 *   backend emits `git:status_changed` on success, which keeps the store
 *   in sync (handled by useGitStatusEvents).
 */
export function BranchPicker() {
  const isOpen = useGitPanelStore((s) => s.isBranchPickerOpen)
  const closeBranchPicker = useGitPanelStore((s) => s.closeBranchPicker)
  const currentBranch = useGitPanelStore((s) => s.branch)

  const [branches, setBranches] = useState<string[]>([])
  const [filter, setFilter] = useState('')
  const [newBranchName, setNewBranchName] = useState('')
  const [loadingBranches, setLoadingBranches] = useState(false)
  const [checkingOut, setCheckingOut] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const filterRef = useRef<HTMLInputElement>(null)

  // Load the branch list every time the picker opens.
  useEffect(() => {
    if (!isOpen) return
    setFilter('')
    setNewBranchName('')
    setError(null)
    setLoadingBranches(true)
    getBranches()
      .then((result) => {
        setBranches(result.map((b) => b.name))
      })
      .catch((err) => {
        setError(
          err instanceof Error ? err.message : 'Failed to load branches',
        )
        setBranches([])
      })
      .finally(() => setLoadingBranches(false))
    // Focus the filter input shortly after opening.
    requestAnimationFrame(() => filterRef.current?.focus())
  }, [isOpen])

  const filteredBranches = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return branches
    return branches.filter((b) => b.toLowerCase().includes(q))
  }, [branches, filter])

  const handleCheckout = useCallback(
    async (name: string) => {
      if (name === currentBranch) {
        closeBranchPicker()
        return
      }
      setCheckingOut(name)
      setError(null)
      try {
        await checkoutBranch(name)
        // Backend emits git:status_changed → store updates automatically.
        closeBranchPicker()
      } catch (err) {
        setError(
          err instanceof Error ? err.message : 'Failed to switch branch',
        )
      } finally {
        setCheckingOut(null)
      }
    },
    [currentBranch, closeBranchPicker],
  )

  const handleCreateBranch = useCallback(async () => {
    const name = newBranchName.trim()
    if (!name || creating) return
    setCreating(true)
    setError(null)
    try {
      await createBranch(name)
      // Backend emits git:status_changed → store updates automatically.
      closeBranchPicker()
    } catch (err) {
      setError(
        err instanceof Error ? err.message : 'Failed to create branch',
      )
    } finally {
      setCreating(false)
    }
  }, [newBranchName, creating, closeBranchPicker])

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' && newBranchName.trim()) {
      e.preventDefault()
      handleCreateBranch()
    }
  }

  return (
    <Dialog
      open={isOpen}
      onOpenChange={(open) => {
        if (!open) closeBranchPicker()
      }}
    >
      <DialogContent
        showCloseButton
        aria-describedby={undefined}
        className="max-w-sm p-0"
      >
        <DialogHeader className="px-4 pt-4 pb-2">
          <DialogTitle className="flex items-center gap-2 text-sm">
            <GitBranch className="size-4" />
            Switch Branch
          </DialogTitle>
        </DialogHeader>

        {/* Filter */}
        <div className="px-4 pb-2">
          <div className="relative">
            <Search className="absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              ref={filterRef}
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter branches..."
              className="h-8 pl-7 text-xs"
            />
          </div>
        </div>

        {/* Branch list */}
        <div className="max-h-[240px] min-h-[80px] overflow-y-auto px-2 pb-2">
          {loadingBranches ? (
            <div className="flex items-center justify-center py-6 text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
            </div>
          ) : filteredBranches.length === 0 ? (
            <div className="px-2 py-6 text-center text-xs text-muted-foreground">
              {branches.length === 0 ? 'No branches found' : 'No matching branches'}
            </div>
          ) : (
            <ul className="flex flex-col gap-0.5">
              {filteredBranches.map((name) => {
                const isCurrent = name === currentBranch
                const isBusy = checkingOut === name
                return (
                  <li key={name}>
                    <button
                      type="button"
                      onClick={() => handleCheckout(name)}
                      disabled={isCurrent || checkingOut !== null || creating}
                      className={cn(
                        'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors',
                        'focus:outline-none focus:ring-1 focus:ring-ring',
                        isCurrent
                          ? 'bg-primary/10 text-primary'
                          : 'hover:bg-muted text-foreground',
                        !isCurrent &&
                          (checkingOut !== null || creating) &&
                          'cursor-not-allowed opacity-50',
                      )}
                    >
                      <GitBranch className="size-3.5 shrink-0 text-muted-foreground" />
                      <span className="flex-1 truncate font-mono">{name}</span>
                      {isBusy ? (
                        <Loader2 className="size-3.5 animate-spin" />
                      ) : isCurrent ? (
                        <Check className="size-3.5" />
                      ) : null}
                    </button>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        {/* New branch */}
        <div className="border-t border-border px-4 py-3">
          <div className="mb-1.5 text-xs font-medium text-muted-foreground">
            Create new branch
          </div>
          <div className="flex items-center gap-1.5">
            <Input
              value={newBranchName}
              onChange={(e) => {
                setNewBranchName(e.target.value)
                if (error) setError(null)
              }}
              onKeyDown={handleKeyDown}
              placeholder="branch-name"
              className="h-8 text-xs"
            />
            <Button
              variant="secondary"
              size="sm"
              onClick={handleCreateBranch}
              disabled={!newBranchName.trim() || creating || checkingOut !== null}
              title="Create and switch to new branch"
            >
              {creating ? (
                <Loader2 className="size-3.5 animate-spin" />
              ) : (
                <Plus className="size-3.5" />
              )}
              New
            </Button>
          </div>
        </div>

        {/* Error */}
        {error && (
          <div className="flex items-center gap-1.5 border-t border-destructive/20 px-4 py-2 text-xs text-destructive">
            <AlertCircle className="size-3.5 shrink-0" />
            <span className="truncate">{error}</span>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
