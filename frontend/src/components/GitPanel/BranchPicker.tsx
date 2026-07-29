import { useState, useMemo, useCallback, useEffect, useRef } from 'react'
import {
  GitBranch,
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
import { Input } from '@/components/ui/input'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { getBranches, checkoutBranch } from '@/api/git'
import { BranchPickerRow } from './BranchPickerRow'
import { NewBranchSection } from './NewBranchSection'

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
  const pendingBranchBase = useGitPanelStore((s) => s.pendingBranchBase)
  const clearPendingBranchBase = useGitPanelStore((s) => s.clearPendingBranchBase)

  // Consume a pending branch base set externally (e.g. "Create › Branch" from
  // the commit context menu). It is read once when the picker opens and then
  // cleared so it doesn't persist into the next manual open. NewBranchSection
  // captures the value into local state (and a ref) on mount, so clearing the
  // shared store here synchronously is safe and avoids the timing-dependent
  // requestAnimationFrame deferral.
  useEffect(() => {
    if (isOpen && pendingBranchBase) {
      clearPendingBranchBase()
    }
  }, [isOpen, pendingBranchBase, clearPendingBranchBase])

  const [branches, setBranches] = useState<string[]>([])
  const [filter, setFilter] = useState('')
  const [loadingBranches, setLoadingBranches] = useState(false)
  const [checkingOut, setCheckingOut] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const filterRef = useRef<HTMLInputElement>(null)

  // Load the branch list every time the picker opens.
  useEffect(() => {
    if (!isOpen) return
    setFilter('')
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
      if (name === currentBranch.name) {
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

  const handleClearError = useCallback(() => setError(null), [])
  const handleError = useCallback((msg: string) => setError(msg), [])

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
        className="max-w-sm p-0 flex flex-col max-h-[calc(100vh-2rem)] overflow-y-auto"
      >
        <DialogHeader className="px-4 pt-4 pb-2 shrink-0">
          <DialogTitle className="flex items-center gap-2 text-sm">
            <GitBranch className="size-4" />
            Switch Branch
          </DialogTitle>
        </DialogHeader>

        {/* Filter */}
        <div className="px-4 pb-2 shrink-0">
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

        {/* Branch list — flex-1 so it shrinks when the base selector expands */}
        <div className="flex-1 min-h-[80px] overflow-y-auto px-2 pb-2">
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
              {filteredBranches.map((name) => (
                <li key={name}>
                  <BranchPickerRow
                    name={name}
                    isCurrent={name === currentBranch.name}
                    isBusy={checkingOut !== null}
                    currentBranchName={currentBranch.name}
                    onCheckout={handleCheckout}
                    onError={setError}
                    onActionSuccess={closeBranchPicker}
                  />
                </li>
              ))}
            </ul>
          )}
        </div>

        {/* New branch — key on isOpen so the field resets when reopened. */}
        <NewBranchSection
          key={isOpen ? 'open' : 'closed'}
          disabled={checkingOut !== null}
          currentBranch={currentBranch.name}
          pendingBase={pendingBranchBase}
          onClearError={handleClearError}
          onError={handleError}
          onCreated={closeBranchPicker}
        />

        {/* Error */}
        {error && (
          <div className="flex items-center gap-1.5 border-t border-destructive/20 px-4 py-2 text-xs text-destructive shrink-0">
            <AlertCircle className="size-3.5 shrink-0" />
            <span className="truncate">{error}</span>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
