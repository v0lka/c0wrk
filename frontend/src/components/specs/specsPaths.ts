// Pure path/label helpers for the SPECS panel. No React, no DOM — fully
// unit-testable. Every path that reaches the file viewer is built here, the
// `workspace:tree_changed` relevance filter lives here, and the labels the
// panel renders derive from the PAYLOAD (a framework's own finding token and
// declared message), never from a frontend vocabulary.

import { normalizePath } from '@/lib/localFileLink'
import type { SDDDocument, SDDFinding } from '@/types/models'

/** Normalize a path for comparison/joining: watcher and backend paths may
 *  carry OS separators (backslashes on Windows); everything here reasons over
 *  forward slashes. */
function toSlashPath(p: string): string {
  return p.replace(/\\/g, '/')
}

/**
 * Resolve a model path — the backend reports every node/document path as
 * WORKSPACE-relative, slash separated — against the ABSOLUTE workspace, so
 * `fileViewerStore.openFile` gets an absolute path. The resolution goes
 * through the app's CANONICAL resolver (`normalizePath` in
 * lib/localFileLink.ts, mirrored by core/pathabs.go — ADR-095): the
 * workspace's dialect is PRESERVED (POSIX `/…`, Windows drive `C:/…`, UNC
 * `//server/share/…`; a hand-rolled segment walk would collapse the UNC
 * anchor's empty leading segments), and `..` clamps at the dialect's anchor
 * instead of popping above the workspace. A DRIVE-rooted input is an absolute
 * path normalizePath passes through verbatim — the same carve-out
 * `resolveFileRefPath` gives chat `@file` refs, on the same user-triggered,
 * uncontained-by-design viewer read path.
 */
export function joinWorkspacePath(workspace: string, modelPath: string): string {
  const rel = toSlashPath(modelPath).split('#')[0]!.replace(/^\/+/, '')
  if (workspace === '') return rel
  return normalizePath(workspace, rel)
}

/** Extract the changed-path list from a `workspace:tree_changed` payload, or
 *  null when the payload carries none. Accepts the two shapes sibling events
 *  use: a `paths: string[]` array (`files:dropped`) and a comma-separated
 *  `paths: string` (`research:file_changed`). A malformed value is treated as
 *  absent (fail-open — see isSpecsRelevantChange). */
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

/**
 * The workspace:tree_changed relevance filter: refresh when the change
 * touches the ACTIVE framework's tree. Both PRODUCTION payload shapes carry
 * no path list at all — the fs watcher emits `{research_scoped?}` and the
 * agent-write emitter emits nothing — so a payload without paths means
 * "cannot scope" and a payload arriving before the first parse (no root yet)
 * means the same: both fail OPEN. GetSpecsStatus is one light read-only
 * parse, and DETECTION itself depends on files outside the roots (a skill
 * installed into `.claude/skills` can make a framework appear), so narrowing
 * the fallback further would leave the panel stale.
 */
export function isSpecsRelevantChange(payload: unknown, root: string): boolean {
  const paths = extractEventPaths(payload)
  if (paths === null) return true
  if (root === '') return true
  const normalizedRoot = toSlashPath(root).replace(/\/+$/, '')
  return paths.some((p) => {
    const changed = toSlashPath(p)
    return changed === normalizedRoot || changed.startsWith(`${normalizedRoot}/`)
  })
}

/** The decision documents' git add-dates keyed by their WORKSPACE-relative
 *  path: the tree renders each decision leaf's date in its own right-aligned
 *  column, and this is the lookup it reads. Undated records (a non-git
 *  workspace, an uncommitted file) are absent from the map — the row renders
 *  without the date column instead of reserving an empty one. */
export function documentDatesByPath(documents: SDDDocument[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const doc of documents) {
    if (doc.date === undefined || doc.date === '') continue
    out[doc.path] = doc.date
  }
  return out
}

/** Turn a framework's finding token into readable words
 *  ("CHANGE_PROPOSAL_MISSING" → "change proposal missing"). Only a FALLBACK:
 *  a finding that carries its check's declared `message` renders that
 *  instead. */
export function humanizeToken(token: string): string {
  return token.replace(/[_-]+/g, ' ').trim().toLowerCase()
}

/** The label of one finding: the check's DECLARED operator-facing message
 *  when the framework declares one, else the readable form of its own finding
 *  token. Nothing here knows a fixed vocabulary — a framework the frontend
 *  has never heard of renders its own words. */
export function findingLabel(finding: SDDFinding): string {
  if (finding.message !== undefined && finding.message !== '') return finding.message
  return humanizeToken(finding.kind)
}
