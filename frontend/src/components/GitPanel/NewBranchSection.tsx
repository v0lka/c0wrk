import { useCallback, useEffect, useRef, useState } from 'react'
import { Plus, Loader2, ChevronRight } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'
import { createBranch, getBranchBases } from '@/api/git'
import { BaseSelector } from './BaseSelector'
import type { BranchBase } from '@/types/models'

interface NewBranchSectionProps {
  /** Disabled while a checkout/merge/rebase is in flight elsewhere. */
  disabled: boolean
  /** Name of the currently checked-out branch (for BaseSelector annotation). */
  currentBranch: string
  /** Clear the picker's shared error banner on input. */
  onClearError: () => void
  /** Surface a creation error to the picker's shared error banner. */
  onError: (message: string) => void
  /** Called after a branch is created — backend emits git:status_changed. */
  onCreated: () => void
  /**
   * Optional ref preselected as the start-point (e.g. a commit SHA set by the
   * history context menu). When set, "Choose base" auto-expands, bases load
   * immediately, and the matching base (or the raw SHA) is selected.
   */
  pendingBase?: string | null
}

/**
 * "Create new branch" footer of BranchPicker. Calls CreateBranch (which
 * checks out the new branch immediately) and closes the picker on success.
 *
 * An optional Collapsible section lets the user pick a base ref (local
 * branch, remote-tracking branch, tag, or recent commit) instead of the
 * default HEAD. Bases are loaded lazily on first expansion.
 */
export function NewBranchSection({
  disabled,
  currentBranch,
  onClearError,
  onError,
  onCreated,
  pendingBase,
}: NewBranchSectionProps) {
  const [name, setName] = useState('')
  const [creating, setCreating] = useState(false)
  // When a pending base is supplied (e.g. from the history context menu's
  // "Create › Branch"), open the base selector automatically so the user
  // sees the preselected start-point.
  const [chooseBase, setChooseBase] = useState(Boolean(pendingBase))
  const [bases, setBases] = useState<BranchBase[]>([])
  const [basesLoaded, setBasesLoaded] = useState(false)
  const [selectedBase, setSelectedBase] = useState(pendingBase ?? '')
  // Capture the pending base once on mount. The lazy-load effect below must
  // not depend on the `pendingBase` prop: BranchPicker clears the shared
  // store value after the picker opens, which would otherwise flip the prop
  // and re-trigger the bases fetch. Reading via a ref keeps the fetch to a
  // single call regardless of when the store is cleared.
  const initialBase = useRef(pendingBase ?? '')

  // Lazily load branch bases when the user first expands the selector.
  useEffect(() => {
    if (!chooseBase || basesLoaded) return
    getBranchBases()
      .then((result) => {
        setBases(result)
        // If the pending base was supplied as a raw SHA, it may not appear
        // in the fetched bases list (which only includes the 20 most recent
        // commits). Keep the raw value as the selection in that case so the
        // backend still receives it.
        const base = initialBase.current
        if (base && !result.some((b) => b.ref === base)) {
          setSelectedBase(base)
        }
      })
      .catch((err) => {
        onError(err instanceof Error ? err.message : 'Failed to load bases')
      })
      .finally(() => setBasesLoaded(true))
  }, [chooseBase, basesLoaded, onError])

  const handleCreate = useCallback(async () => {
    const trimmed = name.trim()
    if (!trimmed || creating || disabled) return
    setCreating(true)
    try {
      await createBranch(trimmed, chooseBase ? selectedBase : '')
      // Backend emits git:status_changed → store updates automatically.
      setName('')
      onCreated()
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to create branch')
    } finally {
      setCreating(false)
    }
  }, [name, creating, disabled, chooseBase, selectedBase, onCreated, onError])

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' && name.trim()) {
      e.preventDefault()
      void handleCreate()
    }
  }

  return (
    <div className="border-t border-border px-4 py-3 shrink-0">
      <div className="mb-1.5 text-xs font-medium text-muted-foreground">
        Create new branch
      </div>
      <div className="flex items-center gap-1.5">
        <Input
          value={name}
          onChange={(e) => {
            setName(e.target.value)
            onClearError()
          }}
          onKeyDown={handleKeyDown}
          placeholder="branch-name"
          className="h-8 text-xs"
        />
        <Button
          variant="secondary"
          size="sm"
          onClick={handleCreate}
          disabled={!name.trim() || creating || disabled}
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

      <Collapsible open={chooseBase} onOpenChange={setChooseBase}>
        <CollapsibleTrigger
          className={cn(
            'mt-2 flex w-full items-center gap-1.5 text-xs text-muted-foreground',
            'hover:text-foreground transition-colors',
          )}
        >
          <ChevronRight
            className={cn(
              'size-3.5 transition-transform',
              chooseBase && 'rotate-90',
            )}
          />
          <span>Choose base instead of HEAD</span>
        </CollapsibleTrigger>
        <CollapsibleContent className="mt-2">
          {basesLoaded ? (
            <>
              <BaseSelector
                bases={bases}
                currentBranch={currentBranch}
                selectedBase={selectedBase}
                onSelect={(ref) => {
                  setSelectedBase(ref)
                  onClearError()
                }}
              />
              {!selectedBase && (
                <div className="mt-2 text-xs text-muted-foreground">
                  No base selected — branch will be created from HEAD.
                </div>
              )}
            </>
          ) : (
            <div className="flex items-center justify-center py-3 text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
            </div>
          )}
        </CollapsibleContent>
      </Collapsible>
    </div>
  )
}
