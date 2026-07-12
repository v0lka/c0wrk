// Pure render constants + helpers for the unified history+graph view.
//
// Extracted from the former GitGraph.tsx and GitCommitRow.tsx so the merged
// GitHistoryTab, its SVG gutter, and its commit rows share one source of
// truth. No React/DOM dependencies — fully unit-testable in isolation.

import type { CommitFile } from '@/types/models'

// ── Graph grid geometry ────────────────────────────────────────────────

/** Vertical pitch between commit rows (the collapsed two-line commit height). */
export const ROW_SPACING = 32
/**
 * Vertical offset from the top of a row to the center of the first (commit
 * message) line. The SVG lane node is drawn here so it aligns with the
 * message line, not the geometric center of the taller two-line row.
 * pt-1 (4px) + half of text-sm leading-none (14px / 2 = 7px) = 11px.
 */
export const NODE_OFFSET = 11
/** Horizontal pitch between lanes. */
export const LANE_SPACING = 22
/** Left padding before the first lane. */
export const LEFT_PAD = 12
/** Radius of a normal commit node. */
export const NODE_R = 4
/** Radius of a merge commit node (drawn with a background-colored ring). */
export const MERGE_R = 5

// ── Inline expansion geometry ──────────────────────────────────────────

/** Height of a single changed-file row inside an expanded commit. */
export const FILE_LINE_HEIGHT = 16
/** Padding above the expanded file list. */
export const EXPANDED_TOP_PAD = 4
/** Padding below the expanded file list. */
export const EXPANDED_BOTTOM_PAD = 6
/** Height of the "Loading files…" line while files are being fetched. */
export const LOADING_LINE_HEIGHT = 19
/** Height of the "No files" line when a commit changed nothing. */
export const NO_FILES_HEIGHT = 19

// ── Lane colors (cycled per lane from design-token CSS variables) ───────

const LANE_VARS = [
  '--color-info',
  '--color-success',
  '--color-warning',
  '--color-highlight',
  '--color-hljs-keyword',
  '--color-hljs-literal',
]

/** CSS variable name for the given lane's branch color. */
export function laneVar(lane: number): string {
  return LANE_VARS[lane % LANE_VARS.length]!
}

/** X coordinate (px) of the given lane's center. */
export function xFor(lane: number): number {
  return LEFT_PAD + lane * LANE_SPACING
}

/** Cubic-bezier path between two grid points (vertical-leaning curve). */
export function edgePath(x1: number, y1: number, x2: number, y2: number): string {
  const ym = (y1 + y2) / 2
  return `M ${x1} ${y1} C ${x1} ${ym} ${x2} ${ym} ${x2} ${y2}`
}

/** Color a ref decoration: HEAD → highlight, tag → warning, branch → info. */
export function refColor(ref: string): string {
  if (ref.startsWith('HEAD')) return 'text-highlight'
  if (ref.startsWith('tag:')) return 'text-warning'
  return 'text-info'
}

/** Map a single-letter commit-file status to a theme text color. */
export function fileStatusColor(status: string): string {
  switch (status) {
    case 'A':
      return 'text-success'
    case 'D':
      return 'text-destructive'
    case 'R':
    case 'C':
      return 'text-info'
    case 'M':
      return 'text-warning'
    default:
      return 'text-muted-foreground'
  }
}

/**
 * Pixel height of the expanded file list for a commit, given its load state
 * and fetched files. Deterministic (no DOM measurement) so the SVG lane
 * layout can route edges around the gap.
 */
export function expandedContentHeight(
  loading: boolean,
  files: CommitFile[] | undefined,
): number {
  const body = loading
    ? LOADING_LINE_HEIGHT
    : files && files.length > 0
      ? files.length * FILE_LINE_HEIGHT
      : NO_FILES_HEIGHT
  return EXPANDED_TOP_PAD + body + EXPANDED_BOTTOM_PAD
}
