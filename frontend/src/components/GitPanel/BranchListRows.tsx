import { Fragment, type ReactNode } from 'react'
import {
  branchLeafLabel,
  type BranchTreeDirNode,
  type BranchTreeNode,
} from '@/lib/branchTree'
import { CollapsibleSectionHeader } from '@/components/ui/CollapsibleSectionHeader'
import { DropdownMenuSeparator } from '@/components/ui/dropdown-menu'
import type { BranchActionKind } from '@/hooks/useBranchActions'
import type { Branch } from '@/types/models'
import { branchRegionId } from './branchRegionId'
import { CollapsibleDirRow } from './CollapsibleDirRow'
import { LocalBranchRow } from './LocalBranchRow'
import { RemoteBranchRow } from './RemoteBranchRow'

/** Draft-mode selection handlers for one section's rows (workspace-pick mode). */
export interface BranchDraftSelect {
  /** Row-level "already picked" marker, keyed by the full branch name / ref. */
  isSelected: (name: string) => boolean
  /** Row click — receives the full branch name (remote: full remote-tracking ref). */
  onSelect: (name: string) => void
  /** Draft block reason (tooltip + click gate); null/undefined = selectable. */
  blockedReasonFor?: (name: string) => string | null
}

interface BranchSectionProps {
  /** Which section this is — drives the collapse key and the menu separator. */
  section: 'local' | 'remote'
  /** Header text ("Local" / "Remote"). */
  label: string
  /** The section's visible (post-filter) branches, rendered as flat rows. */
  visible: Branch[]
  /** `name → branch` over `visible`; memoized by the host. */
  byName: Map<string, Branch>
  /** The section's tree over the visible names (null in flat mode). */
  treeNodes: BranchTreeNode[] | null
  /** `menu` = collapsible Radix headers + separators; `dialog` = static li labels. */
  variant: 'menu' | 'dialog'
  /** Render the section as a `/`-slash tree instead of a flat list. */
  tree: boolean
  /** A search filter is active — force-expands the section and every dir. */
  searchActive: boolean
  /** Collapse state accessors — the host's useCollapsibleSections instance
   *  (section keys `local`/`remote`, dir keys `dir:<fullPath>`). */
  isExpanded: (key: string, forceExpand?: boolean) => boolean
  onToggle: (key: string) => void
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
}

/**
 * One Local/Remote section of the branch list (BranchListRows): the header
 * chrome per variant plus the flat/tree rows — leaves through the shared
 * {@link LocalBranchRow}/{@link RemoteBranchRow}, tree dirs through
 * {@link CollapsibleDirRow}. All per-row state (inFlight / disabled / pinned
 * / blocked reason / draft selection) arrives as functions of the full
 * branch name and is threaded through verbatim; behavior contract:
 * `specs/domains/frontend/branch-list.md`.
 */
export function BranchSection({
  section,
  label,
  visible,
  byName,
  treeNodes,
  variant,
  tree,
  searchActive,
  isExpanded,
  onToggle,
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
}: BranchSectionProps) {
  const inFlightOf = (name: string): BranchActionKind | null =>
    inFlightFor ? inFlightFor(name) : null
  const disabledOf = (name: string): boolean =>
    disabledFor ? disabledFor(name) : false

  // ── Row renderers (shared by flat and tree layouts) ────────────────────────

  const rowElement = (b: Branch, displayName?: string) =>
    b.kind === 'local' ? (
      <LocalBranchRow
        branch={b}
        displayName={displayName}
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
        displayName={displayName}
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
   *  node's `fullPath` key. In tree mode the visible label is the last
   *  `/`-segment (the dir rows above carry the rest, like the Changes
   *  list's name-only files); flat mode shows the full name. */
  const renderLeafRow = (b: Branch, depth: number, key: string = b.name) => {
    const row = depth > 0 ? (
      <div style={{ paddingLeft: `${depth * 12}px` }}>{rowElement(b, tree ? branchLeafLabel(b.name) : undefined)}</div>
    ) : (
      rowElement(b, tree ? branchLeafLabel(b.name) : undefined)
    )
    return variant === 'dialog' ? (
      <li key={key}>{row}</li>
    ) : (
      <Fragment key={key}>{row}</Fragment>
    )
  }

  /** Directory row — chevron + folder; click or Enter/Space toggles the
   *  `dir:<path>` key (the shared CollapsibleDirRow; the menu stays open:
   *  plain div). */
  const renderDirRow = (node: BranchTreeDirNode, depth: number) => {
    const dirKey = `dir:${node.fullPath}`
    const expanded = isExpanded(dirKey, searchActive)
    const row = (
      <CollapsibleDirRow
        name={node.name}
        expanded={expanded}
        onToggle={() => onToggle(dirKey)}
        depth={depth}
        testId={`branch-dir-${node.fullPath}`}
        // Leaf-parity font: only the muted color marks the row as a container.
        labelClassName="text-xs"
      />
    )
    return variant === 'dialog' ? <li>{row}</li> : row
  }

  const leafByName = (name: string, depth: number) => {
    const b = byName.get(name)
    return b ? renderLeafRow(b, depth, `leaf:${name}`) : null
  }

  /** Depth-first flatten of a section tree into row elements. When expanded,
   *  a dir renders its own collision leaf first, then its children. */
  const renderTreeLevel = (
    nodes: BranchTreeNode[],
    depth: number,
  ): ReactNode[] => {
    const out: ReactNode[] = []
    for (const node of nodes) {
      if (node.isDir) {
        out.push(<Fragment key={node.fullPath}>{renderDirRow(node, depth)}</Fragment>)
        if (isExpanded(`dir:${node.fullPath}`, searchActive)) {
          if (node.leaf !== undefined) {
            out.push(leafByName(node.leaf, depth + 1))
          }
          out.push(...renderTreeLevel(node.children, depth + 1))
        }
      } else {
        out.push(leafByName(node.branch, depth))
      }
    }
    return out
  }

  const expanded = isExpanded(section, searchActive)
  const rows = tree && treeNodes ? renderTreeLevel(treeNodes, 0) : visible.map((b) => renderLeafRow(b, 0))

  if (variant === 'menu') {
    return (
      <Fragment>
        {/* Menu separator leads every section after the first — mirrors the
            dropdown's Local → Remote → Worktrees rhythm. */}
        {section === 'remote' && <DropdownMenuSeparator />}
        <CollapsibleSectionHeader
          label={label}
          noun="branches"
          expanded={expanded}
          onToggle={() => onToggle(section)}
          controlsId={branchRegionId(section)}
        />
        {expanded && (
          <div id={branchRegionId(section)} role="presentation">
            {rows}
          </div>
        )}
      </Fragment>
    )
  }

  return (
    <Fragment>
      <li className="px-2 py-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {label}
      </li>
      {rows}
    </Fragment>
  )
}
