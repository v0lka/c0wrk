import { useMemo } from 'react'
import { cn } from '@/lib/utils'
import { buildBranchTree } from '@/lib/branchTree'
import { useCollapsibleSections } from '@/hooks/useCollapsibleSections'
import type { BranchActionKind } from '@/hooks/useBranchActions'
import type { Branch } from '@/types/models'
import { BranchSection, type BranchDraftSelect } from './BranchListRows'

export interface BranchListProps {
  /** All branches (local + remote), unfiltered — split by `kind` internally. */
  branches: Branch[]
  /** Filter predicate over the full branch name; false hides the row (and empties tree dirs). */
  filter?: (name: string) => boolean
  /**
   * Rendering surface: `menu` = dropdown (collapsible Radix headers, sections
   * separated by menu separators — must render inside DropdownMenuContent);
   * `dialog` = modal picker (static label headers in the ul, no separators).
   */
  variant: 'menu' | 'dialog'
  /** Render each section as a `/`-slash tree instead of a flat list. */
  tree?: boolean
  /** A search filter is active — force-expands every section and tree dir. */
  searchActive?: boolean
  /** The action currently in-flight on the named branch (null = idle). */
  inFlightFor?: (name: string) => BranchActionKind | null
  /** True while a different row's operation is in-flight — dims & blocks the row. */
  disabledFor?: (name: string) => boolean
  /** Managed-session pin (ADR-080) — local rows only; omit in draft mode. */
  pinnedFor?: (name: string) => boolean
  /** Managed-focus gate (ADR-080) — null/undefined when the checkout is allowed. */
  checkoutBlockedReasonFor?: (name: string) => string | null
  /** Draft-mode row handlers; a missing side falls back to normal (non-draft) rows. */
  draftSelect?: {
    local?: BranchDraftSelect
    remote?: BranchDraftSelect
  }
  // ── row callbacks (full branch name / full remote ref) ──
  onCheckout: (name: string) => void
  onRename: (name: string) => void
  onMerge: (name: string) => void
  onRebase: (name: string) => void
  onPush: (name: string) => void
  onDeleteLocal: (name: string) => void
  onCheckoutRemote: (remoteBranch: string) => void
  onDeleteRemote: (name: string, remote: string) => void
  /** Empty-state text when no branches exist at all. */
  noBranchesLabel?: string
  /** Empty-state text when the filter matches nothing. */
  noMatchingLabel?: string
  /** Suppress the built-in empty state — the host renders sibling content. */
  hideEmptyState?: boolean
}

/**
 * The Local/Remote branch list shared by BranchDropdown (menu variant) and
 * BranchPicker (dialog variant): it splits `branches` by kind, applies the
 * filter, and renders the two sections through the shared
 * {@link BranchSection}. All per-row state (inFlight / disabled / pinned /
 * blocked reason / draft selection) arrives as functions of the full branch
 * name and is threaded through verbatim.
 *
 * With `tree`, each section renders via {@link buildBranchTree}: directory
 * rows (chevron + folder; click or Enter/Space toggles) indent children by
 * `depth * 12px`,
 * and a remote ref's remote prefix (`origin`) emerges as the top-level
 * directory. A tree leaf's visible label is its last `/`-segment — the dir
 * rows above carry the remaining segments; the accessible name
 * (`aria-label`) and every row payload stay the full branch name. A branch
 * that is both a leaf and a directory prefix renders once — as the
 * directory's own leaf payload inside its expanded dir row. Filtering feeds
 * only visible names into the tree, so dirs whose rows all match nothing
 * are pruned. Sections and dirs collapse through `useCollapsibleSections`
 * (section keys `local`/`remote`, dir keys `dir:<path>`); an active search
 * force-expands everything.
 */
export function BranchList({
  branches,
  filter,
  variant,
  tree = false,
  searchActive = false,
  inFlightFor,
  disabledFor,
  pinnedFor,
  checkoutBlockedReasonFor,
  draftSelect,
  onCheckout,
  onRename,
  onMerge,
  onRebase,
  onPush,
  onDeleteLocal,
  onCheckoutRemote,
  onDeleteRemote,
  noBranchesLabel = 'No branches',
  noMatchingLabel = 'No matching branches',
  hideEmptyState = false,
}: BranchListProps) {
  // Section + directory collapse state is ephemeral (component lifetime).
  // Section keys mirror the labels; dir keys are `dir:<fullPath>`, which
  // cannot collide with them.
  const { isSectionExpanded, toggleSection } = useCollapsibleSections()

  const localBranches = useMemo(
    () => branches.filter((b) => b.kind === 'local'),
    [branches],
  )
  const remoteBranches = useMemo(
    () => branches.filter((b) => b.kind === 'remote'),
    [branches],
  )

  const visibleLocal = useMemo(
    () => localBranches.filter((b) => (filter ?? (() => true))(b.name)),
    [localBranches, filter],
  )
  const visibleRemote = useMemo(
    () => remoteBranches.filter((b) => (filter ?? (() => true))(b.name)),
    [remoteBranches, filter],
  )

  const localByName = useMemo(
    () => new Map(visibleLocal.map((b) => [b.name, b])),
    [visibleLocal],
  )
  const remoteByName = useMemo(
    () => new Map(visibleRemote.map((b) => [b.name, b])),
    [visibleRemote],
  )

  // Trees are built from VISIBLE names only — a directory whose branches all
  // fail the filter disappears instead of rendering empty.
  const localTree = useMemo(
    () => (tree ? buildBranchTree(visibleLocal.map((b) => b.name)) : null),
    [tree, visibleLocal],
  )
  const remoteTree = useMemo(
    () => (tree ? buildBranchTree(visibleRemote.map((b) => b.name)) : null),
    [tree, visibleRemote],
  )

  const sectionProps = {
    variant,
    tree,
    searchActive,
    isExpanded: isSectionExpanded,
    onToggle: toggleSection,
    inFlightFor,
    disabledFor,
    pinnedFor,
    checkoutBlockedReasonFor,
    draftSelect,
    onCheckout,
    onRename,
    onMerge,
    onRebase,
    onPush,
    onDeleteLocal,
    onCheckoutRemote,
    onDeleteRemote,
  }

  // ── Assembly ───────────────────────────────────────────────────────────────

  const hasVisible = visibleLocal.length > 0 || visibleRemote.length > 0
  const blocks = (
    <>
      {visibleLocal.length > 0 && (
        <BranchSection
          section="local"
          label="Local"
          visible={visibleLocal}
          byName={localByName}
          treeNodes={localTree}
          {...sectionProps}
        />
      )}
      {visibleRemote.length > 0 && (
        <BranchSection
          section="remote"
          label="Remote"
          visible={visibleRemote}
          byName={remoteByName}
          treeNodes={remoteTree}
          {...sectionProps}
        />
      )}
    </>
  )
  const emptyState =
    !hasVisible && !hideEmptyState ? (
      <div
        data-testid="branch-list-empty"
        className={cn(
          'px-2 text-center text-xs text-muted-foreground',
          variant === 'menu' ? 'py-3' : 'py-6',
        )}
      >
        {branches.length === 0 ? noBranchesLabel : noMatchingLabel}
      </div>
    ) : null

  if (variant === 'menu') {
    return (
      <>
        {blocks}
        {emptyState}
      </>
    )
  }
  return hasVisible ? <ul className="flex flex-col gap-0.5">{blocks}</ul> : emptyState
}
