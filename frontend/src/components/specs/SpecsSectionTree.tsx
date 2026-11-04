// The specs tree (ADR-105): a real nested tree over the parsed file list —
// top-level section nodes (ROOT for the INDEX.md/META.md group, then the
// section directories) with subdirectory nodes built from the path segments
// and the files as leaves. Every file row opens in the file viewer through
// the onOpen callback (specs-root-relative; the caller resolves it against
// the absolute root).
//
// Expansion is caller-owned: `expandedNodes` maps node ids to their expanded
// flag, an ABSENT key meaning `defaultExpanded` (false — the sidebar panel's
// everything-collapsed default). Without onToggleNode every node renders as
// a plain always-expanded label.

import { ChevronDown, ChevronRight, FileText, FolderOpen } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SpecFile } from '@/types/models'
import { buildSpecsTree, type SpecsTreeNode } from './specsPaths'

/** The decisions/ prefix of a DECISIONS-section leaf — the only leaves the
 *  date column renders on (ADR-108). */
const DECISIONS_PREFIX = 'decisions/'

/** Ordering mode for the top-level section groups: parse order (first
 *  appearance — the panel's default) or alphabetical by name (the root
 *  files group sorts first — it is the INDEX). */
export type SpecsSectionSort = 'appearance' | 'alpha'

/** Alphabetical top-level order: the root files group ('' id) first, then
 *  sections by name. Applied only when the caller asks for it. */
function sortTopNodesAlpha(nodes: SpecsTreeNode[]): SpecsTreeNode[] {
  return [...nodes].sort((a, b) => {
    if (a.id === '') return -1
    if (b.id === '') return 1
    return a.label.localeCompare(b.label)
  })
}

function fileLabel(file: SpecFile): string {
  return file.title !== undefined && file.title !== '' ? file.title : file.path
}

/** One file leaf: the title (or path when the file carries no heading) with
 *  the relative path on the tooltip, plus a Key Files count chip when the
 *  spec lists any. A DECISIONS leaf with a known git add-date carries it in
 *  its own right-aligned compact column (ADR-108): fixed width so dated
 *  rows align, blue-tinted tabular digits, absent entirely for undated
 *  records (a non-git workspace or an uncommitted file) so no dead space
 *  narrows the title. */
function FileRow({
  file,
  date,
  onOpen,
}: {
  file: SpecFile
  date?: string
  onOpen: (relPath: string) => void
}) {
  const keyFiles = file.key_files ?? []
  return (
    <button
      type="button"
      data-testid="specs-file"
      data-path={file.path}
      title={file.path}
      onClick={() => onOpen(file.path)}
      className="flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left text-xs text-foreground transition-colors hover:bg-muted"
    >
      <FileText className="size-3 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate">{fileLabel(file)}</span>
      {keyFiles.length > 0 && (
        <span
          className="shrink-0 rounded bg-secondary px-1 text-xs text-muted-foreground"
          title={`${keyFiles.length} key files`}
        >
          {keyFiles.length}
        </span>
      )}
      {file.path.startsWith(DECISIONS_PREFIX) && date !== undefined && (
        <span
          data-testid="specs-file-date"
          className="w-[64px] shrink-0 text-right text-xs leading-none tabular-nums text-info/80"
          title={`Added ${date}`}
        >
          {date}
        </span>
      )}
    </button>
  )
}

/** One directory node: the header (accented at the top level — icon,
 *  semibold, file count; plain below) and, when expanded, its child
 *  directories and file leaves indented one 16px step per level. Each node
 *  resolves its OWN expansion from the caller's set. */
function TreeNode({
  node,
  depth,
  adrDates,
  expandedNodes,
  defaultExpanded,
  collapsible,
  onToggleNode,
  onOpen,
}: {
  node: SpecsTreeNode
  depth: number
  adrDates?: Record<string, string>
  expandedNodes?: Record<string, boolean>
  defaultExpanded: boolean
  collapsible: boolean
  onToggleNode?: (id: string) => void
  onOpen: (relPath: string) => void
}) {
  const expanded = collapsible
    ? (expandedNodes?.[node.id] ?? defaultExpanded)
    : true
  const accent = depth === 0
  const headerClass = accent
    ? 'flex items-center gap-1 px-0.5 py-0.5 text-xs font-semibold uppercase tracking-wide text-foreground'
    : 'flex items-center gap-1 px-0.5 py-0.5 text-xs text-muted-foreground'
  return (
    <div>
      {collapsible ? (
        <button
          type="button"
          data-testid="specs-tree-group"
          data-node={node.id}
          aria-expanded={expanded}
          title={expanded ? `Collapse ${node.label}` : `Expand ${node.label}`}
          onClick={() => onToggleNode?.(node.id)}
          className={cn(
            headerClass,
            'w-full cursor-pointer rounded text-left transition-colors hover:text-foreground',
          )}
        >
          {expanded ? (
            <ChevronDown className="size-3 shrink-0" />
          ) : (
            <ChevronRight className="size-3 shrink-0" />
          )}
          <FolderOpen className="size-3 shrink-0" />
          <span className="min-w-0 truncate">{node.label}</span>
          <span className="text-muted-foreground/60">({node.count})</span>
        </button>
      ) : (
        <div className={headerClass}>
          <FolderOpen className="size-3" />
          <span className="min-w-0 truncate">{node.label}</span>
          <span className="text-muted-foreground/60">({node.count})</span>
        </div>
      )}
      {expanded && (
        // One 16px indent step per nesting level (zoom-safe: absolute
        // lengths are meant to scale with the UI zoom).
        <div className="ml-4 flex flex-col gap-0.5">
          {node.children.map((child) => (
            <TreeNode
              key={child.id}
              node={child}
              depth={depth + 1}
              adrDates={adrDates}
              expandedNodes={expandedNodes}
              defaultExpanded={defaultExpanded}
              collapsible={collapsible}
              onToggleNode={onToggleNode}
              onOpen={onOpen}
            />
          ))}
          {node.files.map((file) => (
            <FileRow
              key={file.path}
              file={file}
              date={adrDates?.[file.path]}
              onOpen={onOpen}
            />
          ))}
        </div>
      )}
    </div>
  )
}

/** The tree section: the nested directory tree via the pure builder, every
 *  leaf clickable. Expansion is caller-owned (expandedNodes keyed by node
 *  id; an absent key falls back to defaultExpanded) — without onToggleNode
 *  every node renders as a plain always-expanded label. */
export function SpecsSectionTree({
  files,
  onOpen,
  adrDates,
  sortSections = 'appearance',
  expandedNodes,
  onToggleNode,
  defaultExpanded = false,
}: {
  files: SpecFile[]
  onOpen: (relPath: string) => void
  /** Git add-dates keyed by the specs-root-relative file path (ADR-108):
   *  DECISIONS leaves look their date up here. Undated records stay
   *  column-free. */
  adrDates?: Record<string, string>
  sortSections?: SpecsSectionSort
  /** Expanded node ids keyed by node id (true = expanded). An absent key
   *  falls back to defaultExpanded, so {} renders everything collapsed. */
  expandedNodes?: Record<string, boolean>
  /** Called with the node id when a collapsible header is clicked. Its
   *  presence enables the collapsible headers. */
  onToggleNode?: (id: string) => void
  /** The fallback for nodes absent from expandedNodes (false = the
   *  everything-collapsed sidebar default). */
  defaultExpanded?: boolean
}) {
  let nodes = buildSpecsTree(files)
  if (sortSections === 'alpha') {
    nodes = sortTopNodesAlpha(nodes)
  }
  const collapsible = onToggleNode !== undefined
  return (
    <section data-testid="specs-tree" className="flex flex-col gap-1">
      {nodes.map((node) => (
        <TreeNode
          key={node.id}
          node={node}
          depth={0}
          adrDates={adrDates}
          expandedNodes={expandedNodes}
          defaultExpanded={defaultExpanded}
          collapsible={collapsible}
          onToggleNode={onToggleNode}
          onOpen={onOpen}
        />
      ))}
    </section>
  )
}
