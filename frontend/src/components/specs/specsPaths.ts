// Pure path/filter/grouping helpers for the SPECS panel (ADR-105). No
// React, no DOM — fully unit-testable. Every path that reaches the file
// viewer is built here, and the workspace:tree_changed relevance filter
// lives here so the panel component stays a thin orchestrator.

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
 * lexically: `.` and `..` segments collapse without touching the disk.
 * `fileViewerStore.openFile` takes the resulting absolute path.
 */
export function joinSpecPath(root: string, relPath: string): string {
  const rel = toSlashPath(relPath).split('#')[0]!.replace(/^\/+/, '')
  const combined = `${toSlashPath(root).replace(/\/+$/, '')}/${rel}`
  const parts: string[] = []
  for (const seg of combined.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      parts.pop()
      continue
    }
    parts.push(seg)
  }
  return `/${parts.join('/')}`
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
 * The workspace:tree_changed relevance filter (ADR-105 D3): refresh only
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

/** One section of the specs tree: its directory name (`''` for the root
 *  files INDEX.md / META.md) and its files in parse order. */
export interface SpecSectionGroup {
  section: string
  files: SpecFile[]
}

/** Group spec files by their top-level section, preserving first-appearance
 *  order of the sections (the backend parse walks paths in sorted order, so
 *  sections arrive deterministically) and the files' order inside each. */
export function groupFilesBySection(files: SpecFile[]): SpecSectionGroup[] {
  const groups: SpecSectionGroup[] = []
  const bySection = new Map<string, SpecSectionGroup>()
  for (const file of files) {
    const section = file.section ?? ''
    let group = bySection.get(section)
    if (!group) {
      group = { section, files: [] }
      bySection.set(section, group)
      groups.push(group)
    }
    group.files.push(file)
  }
  return groups
}

/** ADR timeline order: newest (highest number) first. A non-numeric number
 *  sorts last; equal numbers keep their input order (stable). */
export function sortADRsNewestFirst(adrs: SpecADRMeta[]): SpecADRMeta[] {
  return [...adrs].sort((a, b) => {
    const an = Number.parseInt(a.number, 10)
    const bn = Number.parseInt(b.number, 10)
    const aNaN = Number.isNaN(an)
    const bNaN = Number.isNaN(bn)
    if (aNaN && bNaN) return 0
    if (aNaN) return 1
    if (bNaN) return -1
    return bn - an
  })
}

/** Human label for a finding kind: the four tier-1 categories of ADR-105
 *  D5, plus a generic passthrough for a future tier's unknown kind (the RPC
 *  boundary passes unknown kinds through instead of dropping them — the
 *  label degrades instead of the finding losing its place). */
const FINDING_KIND_LABELS: Record<string, string> = {
  PATH_MISSING: 'Missing path',
  CROSS_REF_BROKEN: 'Broken cross-ref',
  DUPLICATE_ADR_NUMBER: 'Duplicate ADR number',
  INDEX_STALE: 'INDEX stale',
}

export function findingKindLabel(kind: string): string {
  return FINDING_KIND_LABELS[kind] ?? kind.replace(/_/g, ' ').toLowerCase()
}
