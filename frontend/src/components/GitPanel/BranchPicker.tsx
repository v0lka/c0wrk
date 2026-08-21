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
import { getBranches } from '@/api/git'
import { useBranchActions } from '@/hooks/useBranchActions'
import type { BranchActionKind } from '@/hooks/useBranchActions'
import { LocalBranchRow } from './LocalBranchRow'
import { RemoteBranchRow } from './RemoteBranchRow'
import { BranchDeleteConfirmDialog } from './BranchDeleteConfirmDialog'
import { NewBranchSection } from './NewBranchSection'

/**
 * Modal picker for switching, creating and managing git branches.
 *
 * - Opens when `gitPanelStore.isBranchPickerOpen` is true.
 * - Lists local and remote branches from `gitPanelStore.branches` (refreshed
 *   from GetBranches on open) using the shared {@link LocalBranchRow}/
 *   {@link RemoteBranchRow} components, which expose the full operation set
 *   (checkout / push / merge / rebase / rename / delete for local;
 *   checkout-remote / delete-remote for remote).
 * - Provides a filter input to narrow the list by name.
 * - Provides a "New Branch" field + button (with optional base selector) that
 *   calls CreateBranch and checks out the new branch immediately.
 * - Checkout runs through the shared {@link useBranchActions} hook (single
 *   busy/error track); the picker closes on success and stays open on error.
 */
export function BranchPicker() {
  const isOpen = useGitPanelStore((s) => s.isBranchPickerOpen)
  const closeBranchPicker = useGitPanelStore((s) => s.closeBranchPicker)
  const currentBranch = useGitPanelStore((s) => s.branch)
  const branches = useGitPanelStore((s) => s.branches)
  const setBranches = useGitPanelStore((s) => s.setBranches)
  const pendingBranchBase = useGitPanelStore((s) => s.pendingBranchBase)
  const clearPendingBranchBase = useGitPanelStore((s) => s.clearPendingBranchBase)

  const actions = useBranchActions()
  // Destructure the stable operation callbacks so the checkout handlers below
  // can list them (rather than the recreated `actions` object) as useCallback
  // dependencies, keeping `react-hooks/exhaustive-deps` satisfied.
  const { checkout, checkoutRemote } = actions

  // Consume a pending branch base set externally (e.g. "Create › Branch" from
  // the commit context menu). The base must persist in the store while the
  // picker is open so that NewBranchSection can capture it on mount — Radix
  // Dialog's Presence state machine defers the actual DOM mount of
  // DialogContent by a synchronous re-render cycle, so clearing the base
  // eagerly on open (in a useEffect that runs before the content mounts)
  // would race and leave NewBranchSection with a null base. Instead, the
  // base is cleared only when the picker *closes*, ensuring it is available
  // throughout the open lifecycle and reset before the next manual open.
  useEffect(() => {
    if (!isOpen) {
      clearPendingBranchBase()
    }
  }, [isOpen, clearPendingBranchBase])

  const [filter, setFilter] = useState('')
  const [loadingBranches, setLoadingBranches] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const filterRef = useRef<HTMLInputElement>(null)

  // Refresh the store's branch list every time the picker opens. The list
  // itself is read from `gitPanelStore.branches` — the single source of truth
  // shared with BranchDropdown — so this effect only refreshes, it never forks
  // a second local copy.
  useEffect(() => {
    if (!isOpen) return
    setFilter('')
    setError(null)
    setLoadingBranches(true)
    getBranches()
      .then((result) => setBranches(result))
      .catch((err) => {
        setError(
          err instanceof Error ? err.message : 'Failed to load branches',
        )
        setBranches([])
      })
      .finally(() => setLoadingBranches(false))
    // Focus the filter input shortly after opening.
    requestAnimationFrame(() => filterRef.current?.focus())
  }, [isOpen, setBranches])

  const localBranches = useMemo(
    () => branches.filter((b) => b.kind === 'local'),
    [branches],
  )
  const remoteBranches = useMemo(
    () => branches.filter((b) => b.kind === 'remote'),
    [branches],
  )

  const filterFn = useCallback(
    (name: string) => {
      const q = filter.trim().toLowerCase()
      if (!q) return true
      return name.toLowerCase().includes(q)
    },
    [filter],
  )

  const visibleLocal = localBranches.filter((b) => filterFn(b.name))
  const visibleRemote = remoteBranches.filter((b) => filterFn(b.name))

  // Checkout (local + remote) closes on success and stays open on error. The
  // shared hook captures errors into `actions.error`; the boolean return tells
  // us whether the operation succeeded so we can close only on success.
  const handleCheckout = useCallback(
    async (name: string) => {
      const ok = await checkout(name)
      if (ok) closeBranchPicker()
    },
    [checkout, closeBranchPicker],
  )

  const handleCheckoutRemote = useCallback(
    async (remoteBranch: string) => {
      const ok = await checkoutRemote(remoteBranch)
      if (ok) closeBranchPicker()
    },
    [checkoutRemote, closeBranchPicker],
  )

  const handleClearError = useCallback(() => setError(null), [])
  const handleError = useCallback((msg: string) => setError(msg), [])

  // A row is busy when its own operation is running; otherwise it is blocked
  // while a different row's operation runs.
  const inFlightFor = (name: string): BranchActionKind | null =>
    actions.busyBranch === name ? actions.busyAction : null
  const disabledFor = (name: string): boolean =>
    actions.isBusy && actions.busyBranch !== name

  const displayedError = error ?? actions.error

  return (
    <Dialog
      open={isOpen}
      onOpenChange={(open) => {
        if (open) {
          actions.clearError()
          actions.clearOutput()
        } else {
          closeBranchPicker()
        }
      }}
    >
      <DialogContent
        showCloseButton
        aria-describedby={undefined}
        className="max-w-sm p-0 flex flex-col max-h-[calc(100vh-2rem)] overflow-y-auto custom-scrollbar"
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
        <div className="flex-1 min-h-[80px] overflow-y-auto custom-scrollbar px-2 pb-2">
          {actions.renamingBranch ? (
            <div className="px-1 py-1">
              <Input
                ref={actions.renameRef}
                value={actions.renameValue}
                onChange={(e) => actions.setRenameValue(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') void actions.commitRename()
                  if (e.key === 'Escape') actions.cancelRename()
                }}
                onBlur={() => void actions.commitRename()}
                placeholder={`Rename ${actions.renamingBranch}`}
                className="h-8 text-xs"
                autoFocus
              />
            </div>
          ) : loadingBranches ? (
            <div className="flex items-center justify-center py-6 text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
            </div>
          ) : visibleLocal.length === 0 && visibleRemote.length === 0 ? (
            <div className="px-2 py-6 text-center text-xs text-muted-foreground">
              {branches.length === 0 ? 'No branches found' : 'No matching branches'}
            </div>
          ) : (
            <ul className="flex flex-col gap-0.5">
              {visibleLocal.length > 0 && (
                <li className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                  Local
                </li>
              )}
              {visibleLocal.map((b) => (
                <li key={b.name}>
                  <LocalBranchRow
                    branch={b}
                    inFlight={inFlightFor(b.name)}
                    disabled={disabledFor(b.name)}
                    onCheckout={handleCheckout}
                    onRename={actions.startRename}
                    onMerge={actions.mergeBranch}
                    onRebase={actions.rebaseBranch}
                    onPush={actions.push}
                    onDelete={actions.requestDeleteLocal}
                  />
                </li>
              ))}
              {visibleRemote.length > 0 && (
                <li className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                  Remote
                </li>
              )}
              {visibleRemote.map((b) => (
                <li key={b.name}>
                  <RemoteBranchRow
                    branch={b}
                    inFlight={inFlightFor(b.name)}
                    disabled={disabledFor(b.name)}
                    onCheckoutRemote={handleCheckoutRemote}
                    onDeleteRemote={actions.requestDeleteRemote}
                  />
                </li>
              ))}
            </ul>
          )}
        </div>

        {/* New branch — key on isOpen so the field resets when reopened. */}
        <NewBranchSection
          key={isOpen ? 'open' : 'closed'}
          disabled={actions.isBusy}
          currentBranch={currentBranch.name}
          pendingBase={pendingBranchBase}
          onClearError={handleClearError}
          onError={handleError}
          onCreated={closeBranchPicker}
        />

        {/* Output (successful remote-op progress) */}
        {actions.output && (
          <div className="flex items-center gap-1.5 border-t border-success/20 px-4 py-2 text-xs text-success shrink-0">
            <span className="truncate">{actions.output}</span>
          </div>
        )}

        {/* Error */}
        {displayedError && (
          <div className="flex items-center gap-1.5 border-t border-destructive/20 px-4 py-2 text-xs text-destructive shrink-0">
            <AlertCircle className="size-3.5 shrink-0" />
            <span className="truncate">{displayedError}</span>
          </div>
        )}
      </DialogContent>

      <BranchDeleteConfirmDialog
        pending={actions.pendingDelete}
        onConfirm={actions.confirmDelete}
        onCancel={actions.cancelDelete}
      />
    </Dialog>
  )
}
