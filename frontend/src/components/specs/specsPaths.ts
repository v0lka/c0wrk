// Pure path/tree helpers for the SPECS panel (ADR-106). No React, no DOM —
// fully unit-testable. Every path that reaches the file viewer is built
// here, and the workspace:tree_changed relevance filter lives here too, so
// the components stay thin orchestrators.

import { normalizePath } from '@/lib/localFileLink'
import type { SpecADRMeta, SpecFile } from '@/types/models'

/** Normalize a path for comparison/joining: watcher and backend paths may
 *  carry OS separators (backslashes on Windows); everything here reasons
 *  over forward slashes. */
function toSlashPath(p: string): string {
  return p.replace(/\\/g, '/')
}

/**
 * Resolve a specs-root-relative reference (verbatim from a Markdown link or
 * table cell — possibly carrying a `./` prefix, a `#anchor` suffix, or a
 * `../` escape that leaves the specs tree) against the ABSOLUTE specs root,
 * lexically, through the app's CANONICAL resolver (`normalizePath` in
 * lib/localFileLink.ts, mirrored by core/pathabs.go — ADR-095): the root's
 * dialect is PRESERVED (POSIX `/…`, Windows drive `C:/…`, UNC
 * `//server/share/…` — a hand-rolled segment walk would collapse the UNC
 * anchor's empty leading segments), and `..` clamps at the dialect's anchor
 * instead of popping above the root. Leading-slash relPaths are anchored
 * under the root; a DRIVE-rooted relPath (`C:/x.md` in a table cell) is an
 * absolute input normalizePath passes through verbatim — the same carve-out
 * `resolveFileRefPath` gives chat `@file` refs, on the same user-triggered,
 * uncontained-by-design viewer read path.
 * `fileViewerStore.openFile` takes the resulting absolute path.
 */
export function joinSpecPath(root: string, relPath: string): string {
  const rel = toSlashPath(relPath).split('#')[0]!.replace(/^\/+/, '')
  return normalizePath(root, rel)
}

/** Extract the changed-path list from a `workspace:tree_changed` payload,
 * or null when the payload carries none. Accepts the two shapes sibling
 * events use: a `paths: string[]` array (`files:dropped`) and a
 * comma-separated `paths: string` (`research:file_changed`). A malformed
 * value is treated as absent (fail-open — see isSpecsRelevantChange). */
function extractEventPaths(payload: unknown): string[] | null {
  if (typeof payload !== 'object' || payload === null) return null
  const raw = (payload as Record<string, unknown>)['paths']
  if (raw === undefined || raw === null) return null
  if (Array.isArray(raw)) {
    return raw.every((p) => typeof p === 'string') ? (raw as string[]) : null
  }
  if (typeof raw === 'string') {
    return raw
      .split(',')
      .map((p) => p.trim())
      .filter((p) => p !== '')
  }
  return null
}

/** Whether one changed path touches the specs tree. With a known specs root
 *  the test is containment; before the first status lands (root '') the
 *  best signal is a `specs` path segment. */
function isInsideSpecs(changedPath: string, specsRoot: string): boolean {
  const p = toSlashPath(changedPath)
  if (specsRoot !== '') {
    const root = toSlashPath(specsRoot).replace(/\/+$/, '')
    return p === root || p.startsWith(`${root}/`)
  }
  return /(^|\/)specs(\/|$)/.test(p)
}

/**
 * The workspace:tree_changed relevance filter (ADR-106 D3): refresh only
 * when the change touches specs/ paths. Both PRODUCTION payload shapes
 * carry no path list at all — the fs watcher emits `{research_scoped?}` and
 * the agent-write emitter emits nothing — so a payload without paths means
 * "cannot scope" and fails OPEN (refresh): GetSpecsStatus is one light
 * read-only parse, and missing a specs/ edit would leave the panel stale.
 * When a future payload does carry paths, the filter scopes the refetch to
 * specs/-touching changes.
 */
export function isSpecsRelevantChange(payload: unknown, specsRoot: string): boolean {
  const paths = extractEventPaths(payload)
  if (paths === null) return true
  return paths.some((p) => isInsideSpecs(p, specsRoot))
}

/** One node of the nested specs tree (ADR-106): a directory of the specs
 *  root — the root files group (''), a top-level section directory
 *  ('domains'), or a nested subdirectory ('domains/frontend'). */
export interface SpecsTreeNode {
  /** Stable expansion identity: the directory path relative to the specs
   *  root — '' for the root files group (INDEX.md / META.md), the first
   *  segment for a section, the joined segments for a nested directory. */
  id: string
  /** Display label: 'root' for the root files group, otherwise the bare
   *  directory segment. */
  label: string
  /** Child directories, sorted by label. */
  children: SpecsTreeNode[]
  /** Files directly inside this directory, in input order (the backend
   *  parse emits paths sorted, so this is path order). */
  files: SpecFile[]
  /** Total number of files in the whole subtree (the header count chip). */
  count: number
}

/** Build the nested specs tree from the parsed file list (ADR-106). The
 *  nesting derives from the PATH segments: a top-level directory becomes a
 *  section node, deeper segments become nested subdirectory nodes, and the
 *  file itself is a leaf of its innermost directory. Files at the root of
 *  the specs tree (INDEX.md, META.md — no directory segment) form the root
 *  group. Files whose base name starts with '_' are EXCLUDED — the
 *  template prefix the backend parse skips; the exclusion is repeated here
 *  so a stale or future parse cannot leak templates into the tree. Section
 *  order is first appearance (the parse is path-sorted, so this is
 *  alphabetical by path); child directories sort by label. */
export function buildSpecsTree(files: SpecFile[]): SpecsTreeNode[] {
  const rootFiles: SpecFile[] = []
  const sections = new Map<string, SpecsTreeNode>()
  const sectionOrder: string[] = []
  for (const file of files) {
    const segments = toSlashPath(file.path).split('/')
    const base = segments[segments.length - 1] ?? ''
    if (base.startsWith('_')) continue
    if (segments.length === 1) {
      rootFiles.push(file)
      continue
    }
    const dirs = segments.slice(0, -1)
    const section = dirs[0]!
    let node: SpecsTreeNode | undefined = sections.get(section)
    if (!node) {
      const created: SpecsTreeNode = {
        id: section,
        label: section,
        children: [],
        files: [],
        count: 0,
      }
      sections.set(section, created)
      sectionOrder.push(section)
      node = created
    }
    for (let i = 1; i < dirs.length; i++) {
      const dirId = dirs.slice(0, i + 1).join('/')
      let child: SpecsTreeNode | undefined = node.children.find((c) => c.id === dirId)
      if (!child) {
        child = { id: dirId, label: dirs[i]!, children: [], files: [], count: 0 }
        node.children.push(child)
      }
      node = child
    }
    node.files.push(file)
  }

  // Depth-first subtree counts + child-directory ordering. The root group's
  // count is its own file count (it has no subdirectories).
  const finalize = (node: SpecsTreeNode): number => {
    node.children.sort((a, b) => a.label.localeCompare(b.label))
    let total = node.files.length
    for (const child of node.children) total += finalize(child)
    node.count = total
    return total
  }
  const topNodes: SpecsTreeNode[] = []
  if (rootFiles.length > 0) {
    topNodes.push({
      id: '',
      label: 'root',
      children: [],
      files: rootFiles,
      count: rootFiles.length,
    })
  }
  for (const section of sectionOrder) {
    const node = sections.get(section)!
    finalize(node)
    topNodes.push(node)
  }
  return topNodes
}

/** The decision rows' git add-dates keyed by their specs-root-relative
 *  file path (ADR-109): the sidebar tree renders each DECISIONS leaf's
 *  date in its own right-aligned column, and this is the lookup it reads.
 *  Undated records (a non-git workspace, an uncommitted file) are absent
 *  from the map — the row renders without the date column instead of
 *  reserving an empty one. */
export function adrDatesByFile(adrs: SpecADRMeta[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const adr of adrs) {
    if (adr.date === undefined || adr.date === '') continue
    out[adr.file] = adr.date
  }
  return out
}

/** Human label for a finding kind: the tier-1 categories of ADR-106 D5
 *  (as completed by ADR-109), plus a generic passthrough for a future
 *  tier's unknown kind (the RPC boundary passes unknown kinds through
 *  instead of dropping them — the label degrades instead of the finding
 *  losing its place). */
const FINDING_KIND_LABELS: Record<string, string> = {
  PATH_MISSING: 'Missing path',
  CROSS_REF_BROKEN: 'Broken cross-ref',
  DUPLICATE_ADR_NUMBER: 'Duplicate ADR number',
  INDEX_STALE: 'INDEX stale',
  ROOT_FILE_MISSING: 'Missing root file',
  ADR_NAME_MALFORMED: 'Malformed ADR name',
  ADR_NUMBER_MISMATCH: 'ADR number mismatch',
  SUPERSEDED_TARGET_MISSING: 'Missing successor',
  SUPERSEDED_CYCLE: 'Supersession cycle',
}

export function findingKindLabel(kind: string): string {
  return FINDING_KIND_LABELS[kind] ?? kind.replace(/_/g, ' ').toLowerCase()
}
