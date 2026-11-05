import { Fragment, useMemo, type ReactNode } from 'react'
import { ChevronDown, ChevronRight, Folder } from 'lucide-react'
import { cn } from '@/lib/utils'
import {
  buildBranchTree,
  type BranchTreeDirNode,
  type BranchTreeNode,
} from '@/lib/branchTree'
import { CollapsibleSectionHeader } from '@/components/ui/CollapsibleSectionHeader'
import { DropdownMenuSeparator } from '@/components/ui/dropdown-menu'
import { useCollapsibleSections } from '@/hooks/useCollapsibleSections'
import type { BranchActionKind } from '@/hooks/useBranchActions'
import type { Branch } from '@/types/models'
import { branchRegionId } from './branchRegionId'
import { LocalBranchRow } from './LocalBranchRow'
import { RemoteBranchRow } from './RemoteBranchRow'

// ─────────────────────────────────── Types ───────────────────────────────────

/** Draft-mode selection handlers for one section's rows (workspace-pick mode). */
export interface BranchDraftSelect {
  /** Row-level "already picked" marker, keyed by the full branch name / ref. */
  isSelected: (name: string) => boolean
  /** Row click — receives the full branch name (remote: full remote-tracking ref). */
  onSelect: (name: string) => void
  /** Draft block reason (tooltip + click gate); null/undefined = selectable. */
  blockedReasonFor?: (name: string) => string | null
}

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

// ───────────────────────────── Main Component ────────────────────────────────

/**
 * The Local/Remote branch list shared by BranchDropdown (menu variant) and
 * BranchPicker (dialog variant): it splits `branches` by kind, applies the
 * filter, and renders the two sections through the shared
 * {@link LocalBranchRow}/{@link RemoteBranchRow} rows. All per-row state
 * (inFlight / disabled / pinned / blocked reason / draft selection) arrives
 * as functions of the full branch name and is threaded through verbatim.
 *
 * With `tree`, each section renders via {@link buildBranchTree}: directory
 * rows (chevron + folder; click or Enter/Space toggles) indent children by
 * `depth * 12px`,
 * and a remote ref's remote prefix (`origin`) emerges as the top-level
 * directory. A branch that is both a leaf and a directory prefix renders
 * once — as the directory's own leaf payload inside its expanded dir row.
 * Filtering feeds only visible names into the tree, so dirs whose rows all
 * match nothing are pruned. Sections and dirs collapse through
 * `useCollapsibleSections` (section keys `local`/`remote`, dir keys
 * `dir:<path>`); an active search force-expands everything.
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

  const inFlightOf = (name: string): BranchActionKind | null =>
    inFlightFor ? inFlightFor(name) : null
  const disabledOf = (name: string): boolean =>
    disabledFor ? disabledFor(name) : false

  // ── Row renderers (shared by flat and tree layouts) ────────────────────────

  const rowElement = (b: Branch) =>
    b.kind === 'local' ? (
      <LocalBranchRow
        branch={b}
        inFlight={inFlightOf(b.name)}
        disabled={disabledOf(b.name)}
        pinned={pinnedFor ? pinnedFor(b.name) : undefined}
        checkoutBlockedReason={
          checkoutBlockedReasonFor ? checkoutBlockedReasonFor(b.name) : undefined
        }
        onCheckout={onCheckout}
        onRename={onRename}
        onMerge={onMerge}
        onRebase={onRebase}
        onPush={onPush}
        onDelete={onDeleteLocal}
        draftSelect={
          draftSelect?.local
            ? {
                selected: draftSelect.local.isSelected(b.name),
                onSelect: draftSelect.local.onSelect,
              }
            : undefined
        }
        draftBlockedReason={
          draftSelect?.local?.blockedReasonFor
            ? draftSelect.local.blockedReasonFor(b.name)
            : undefined
        }
      />
    ) : (
      <RemoteBranchRow
        branch={b}
        inFlight={inFlightOf(b.name)}
        disabled={disabledOf(b.name)}
        checkoutBlockedReason={
          checkoutBlockedReasonFor ? checkoutBlockedReasonFor(b.name) : undefined
        }
        draftBlockedReason={
          draftSelect?.remote?.blockedReasonFor
            ? draftSelect.remote.blockedReasonFor(b.name)
            : undefined
        }
        draftSelect={
          draftSelect?.remote
            ? {
                selected: draftSelect.remote.isSelected(b.name),
                onSelect: draftSelect.remote.onSelect,
              }
            : undefined
        }
        onCheckoutRemote={onCheckoutRemote}
        onDeleteRemote={onDeleteRemote}
      />
    )

  /** A leaf row: the shared row component, indented by its tree depth and
   *  li-wrapped in the dialog variant (the menu renders rows bare). `key`
   *  overrides the default branch-name key — tree mode namespaces keys with
   *  a `leaf:` prefix so a carried collision leaf never shares its dir
   *  node's `fullPath` key. */
  const renderLeafRow = (b: Branch, depth: number, key: string = b.name) => {
    const row = depth > 0 ? (
      <div style={{ paddingLeft: `${depth * 12}px` }}>{rowElement(b)}</div>
    ) : (
      rowElement(b)
    )
    return variant === 'dialog' ? (
      <li key={key}>{row}</li>
    ) : (
      <Fragment key={key}>{row}</Fragment>
    )
  }

  /** Directory row — chevron + folder; click or Enter/Space toggles the
   *  `dir:<path>` key (mirrors the ChangesList tree rows; the menu stays
   *  open: plain div). The keyboard twin of the click keeps the row
   *  reachable: role="button" + tabIndex — there is no owning role="tree",
   *  so treeitem would be orphaned ARIA. */
  const renderDirRow = (node: BranchTreeDirNode, depth: number) => {
    const dirKey = `dir:${node.fullPath}`
    const expanded = isSectionExpanded(dirKey, searchActive)
    const row = (
      <div
        className="flex h-7 cursor-pointer select-none items-center gap-1.5 px-2 hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors"
        style={{ paddingLeft: `${depth * 12 + 8}px` }}
        onClick={() => toggleSection(dirKey)}
        role="button"
        tabIndex={0}
        aria-expanded={expanded}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            toggleSection(dirKey)
          }
        }}
        data-testid={`branch-dir-${node.fullPath}`}
      >
        {expanded ? (
          <ChevronDown className="size-3.5 shrink-0" />
        ) : (
          <ChevronRight className="size-3.5 shrink-0" />
        )}
        <Folder className="size-3.5 shrink-0" />
        <span className="min-w-0 truncate text-sm leading-none">{node.name}</span>
      </div>
    )
    return variant === 'dialog' ? <li>{row}</li> : row
  }

  const leafByName = (section: 'local' | 'remote', name: string, depth: number) => {
    const b = (section === 'local' ? localByName : remoteByName).get(name)
    return b ? renderLeafRow(b, depth, `leaf:${name}`) : null
  }

  /** Depth-first flatten of a section tree into row elements. When expanded,
   *  a dir renders its own collision leaf first, then its children. */
  const renderTreeLevel = (
    nodes: BranchTreeNode[],
    depth: number,
    section: 'local' | 'remote',
  ): ReactNode[] => {
    const out: ReactNode[] = []
    for (const node of nodes) {
      if (node.isDir) {
        out.push(<Fragment key={node.fullPath}>{renderDirRow(node, depth)}</Fragment>)
        if (isSectionExpanded(`dir:${node.fullPath}`, searchActive)) {
          if (node.leaf !== undefined) {
            out.push(leafByName(section, node.leaf, depth + 1))
          }
          out.push(...renderTreeLevel(node.children, depth + 1, section))
        }
      } else {
        out.push(leafByName(section, node.branch, depth))
      }
    }
    return out
  }

  // ── Section renderers ──────────────────────────────────────────────────────

  const renderSection = (
    key: 'local' | 'remote',
    label: string,
    visible: Branch[],
    treeNodes: BranchTreeNode[] | null,
  ) => {
    const expanded = isSectionExpanded(key, searchActive)
    const rows = tree && treeNodes ? renderTreeLevel(treeNodes, 0, key) : visible.map((b) => renderLeafRow(b, 0))

    if (variant === 'menu') {
      return (
        <Fragment key={key}>
          {/* Menu separator leads every section after the first — mirrors the
              dropdown's Local → Remote → Worktrees rhythm. */}
          {key === 'remote' && <DropdownMenuSeparator />}
          <CollapsibleSectionHeader
            label={label}
            noun="branches"
            expanded={expanded}
            onToggle={() => toggleSection(key)}
            controlsId={branchRegionId(key)}
          />
          {expanded && (
            <div id={branchRegionId(key)} role="presentation">
              {rows}
            </div>
          )}
        </Fragment>
      )
    }

    return (
      <Fragment key={key}>
        <li className="px-2 py-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {label}
        </li>
        {rows}
      </Fragment>
    )
  }

  // ── Assembly ───────────────────────────────────────────────────────────────

  const hasVisible = visibleLocal.length > 0 || visibleRemote.length > 0
  const blocks = (
    <>
      {visibleLocal.length > 0 &&
        renderSection('local', 'Local', visibleLocal, localTree)}
      {visibleRemote.length > 0 &&
        renderSection('remote', 'Remote', visibleRemote, remoteTree)}
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
