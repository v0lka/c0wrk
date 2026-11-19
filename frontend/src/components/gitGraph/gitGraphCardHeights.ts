// Pure height + slice math for the Git-graph page's expandable commit cards.
//
// Every expansion height is a DETERMINISTIC function of the card's UI state
// (fetch/loading/loaded per section) — no DOM measurement anywhere. The SVG
// lane gutter and the virtualizer both consume the same numbers, so rows,
// edges and scroll offsets stay aligned while cards expand inline. The
// rendered card mirrors this module section-for-section (each section gets
// an explicit pixel height derived from the same constants), which makes the
// DOM height provably equal to the computed height.
//
// Constants describe the CSS the card actually renders:
//   text-xs + leading-4      → 16px per line (message, placeholders)
//   text-xs + leading-relaxed → 19.5px per diff row (Unified/SplitDiffTable)
//
// Long content never grows a section: the message and each expanded file
// diff are CAPPED containers with internal scrolling (overflow-y-auto), so
// the height contributed to the row is min(cap, natural estimate). Soft
// line-wrapping inside a diff row can make its natural height exceed the
// one-row-per-line estimate; the capped container then scrolls internally
// while the reserved (estimate) height stays stable — determinism over
// pixel-perfection, by design.

import { ROW_SPACING } from '@/components/GitPanel/gitGraphRender'
import { parseHunkRaw, buildSideBySidePairs, type ReviewFileDiffLike } from '@/components/review/diffParsing'
import type { CommitFile } from '@/types/models'

// ── Inline expansion geometry ─────────────────────────────────────────
//
// This module OWNS the expansion geometry: the Git panel's rows no longer
// expand inline (that lives exclusively here, in the file viewer's graph
// page), so the constants are defined at their only point of use.

/** Height of a single changed-file row inside an expanded card. */
export const FILE_LINE_HEIGHT = 16
/** Padding between the message block and the file list. */
export const EXPANDED_TOP_PAD = 4
/** Bottom padding closing the expansion. */
export const EXPANDED_BOTTOM_PAD = 6
/** Height of the "Loading files…" line while files are being fetched. */
export const LOADING_LINE_HEIGHT = 19
/** Height of the files-list empty-state line (merge / root / no files). */
export const NO_FILES_HEIGHT = 19

// ── Full-message block ────────────────────────────────────────────────

/** Rendered height of one wrapped commit-message line (text-xs leading-4). */
export const MESSAGE_LINE_HEIGHT = 16
/** Vertical padding around the message block (py-1 = 4px top + 4px bottom). */
export const MESSAGE_PAD_Y = 8
/** Cap of the scrollable full-message block; longer messages scroll inside. */
export const MESSAGE_MAX_HEIGHT = 96

// ── Per-file diff block ───────────────────────────────────────────────

/** Rendered height of one diff table row (text-xs leading-relaxed = 12×1.625). */
export const DIFF_ROW_HEIGHT = 19.5
/** Cap of the scrollable diff area; larger diffs scroll inside. */
export const DIFF_AREA_MAX_HEIGHT = 240
/** Height of a placeholder line ("Loading diff…", error, merge/root notices). */
export const STATUS_LINE_HEIGHT = 24

/** UI state of ONE file's diff inside an expanded card. */
export interface FileDiffUiState {
  /** Whether the file's diff body is shown. */
  expanded: boolean
  /** The commit's diff has not arrived yet (single getCommitDiff per commit). */
  loading: boolean
  /** The commit's diff fetch failed. */
  failed: boolean
  /** This file's slice of the commit diff; undefined while loading/absent. */
  slice: ReviewFileDiffLike | undefined
  /** The shared unified/split rendering mode — part of the height state. */
  diffViewMode: 'unified' | 'split'
}

/** Everything the card's height depends on. */
export interface CardExpansionState {
  /** Full-message fetch in flight (no cached message yet). */
  messageLoading: boolean
  /** Full-message fetch failed. */
  messageFailed: boolean
  /** Cached full commit message; undefined until the first fetch resolves. */
  fullMessage: string | undefined
  /** Changed-files fetch in flight / not yet fetched. */
  filesLoading: boolean
  /** Fetched changed files; undefined while loading. */
  files: CommitFile[] | undefined
  /** Per-path diff state; only entries present here can contribute height. */
  fileDiffs: Record<string, FileDiffUiState>
}

/**
 * Height of the full-message block. A cached message renders in a capped
 * scrollable box (py-1 + one 16px line per newline-separated line); while
 * loading or after a failure a single 24px placeholder line renders instead.
 */
export function messageAreaHeight(s: Pick<CardExpansionState, 'messageLoading' | 'messageFailed' | 'fullMessage'>): number {
  if (s.fullMessage !== undefined) {
    const lines = s.fullMessage.split('\n').length
    return Math.min(MESSAGE_MAX_HEIGHT, MESSAGE_PAD_Y + lines * MESSAGE_LINE_HEIGHT)
  }
  return STATUS_LINE_HEIGHT
}

/**
 * Height of ONE file's diff body. Collapsed files contribute nothing.
 * Loading, failed and hunk-less slices render a single placeholder line;
 * a loaded slice contributes min(cap, one row per rendered diff row ×
 * DIFF_ROW_HEIGHT) — the capped container scrolls internally beyond the
 * cap. The rendered row count mirrors the actual tables exactly: unified
 * renders one row per raw hunk line; split zips each del/add pair into one
 * side-by-side row (buildSideBySidePairs), so the mode is part of the state.
 */
export function fileDiffAreaHeight(st: FileDiffUiState | undefined): number {
  if (!st || !st.expanded) return 0
  if (st.loading || st.failed || !st.slice || st.slice.hunks.length === 0) {
    return STATUS_LINE_HEIGHT
  }
  const rows =
    st.diffViewMode === 'split'
      ? splitModeRowCount(st.slice)
      : st.slice.hunks.reduce((n, hunk) => n + hunk.raw.split('\n').length, 0)
  return Math.min(DIFF_AREA_MAX_HEIGHT, rows * DIFF_ROW_HEIGHT)
}

/** Rendered row count of the side-by-side table for one file's slice. */
function splitModeRowCount(slice: ReviewFileDiffLike): number {
  const lines = slice.hunks.flatMap((hunk) => parseHunkRaw(hunk.raw, hunk.old_start, hunk.new_start))
  return buildSideBySidePairs(lines).length
}

/**
 * Height of the changed-files section: a single placeholder line while
 * loading or empty ("No diff (merge commit)" / "No files (root commit)" /
 * "No files" are one 19px line each, matching the Git panel's loading and
 * no-files geometry), otherwise one 16px row per file plus each expanded
 * file's diff body.
 */
export function filesSectionHeight(s: Pick<CardExpansionState, 'filesLoading' | 'files' | 'fileDiffs'>): number {
  if (s.filesLoading || s.files === undefined) return LOADING_LINE_HEIGHT
  if (s.files.length === 0) return NO_FILES_HEIGHT
  let h = 0
  for (const file of s.files) {
    h += FILE_LINE_HEIGHT + fileDiffAreaHeight(s.fileDiffs[file.path])
  }
  return h
}

/**
 * Total height of the expansion below the two-line header: message block,
 * 4px top pad, files section, 6px bottom pad. (The two-line header itself
 * is the fixed ROW_SPACING and is NOT included here.)
 */
export function expandedCardHeight(s: CardExpansionState): number {
  return (
    messageAreaHeight(s) +
    EXPANDED_TOP_PAD +
    filesSectionHeight(s) +
    EXPANDED_BOTTOM_PAD
  )
}

/**
 * Reserved pixel height of a whole card row: the fixed two-line header
 * (ROW_SPACING) plus the expansion when the card is open.
 */
export function cardHeight(s: CardExpansionState, expanded: boolean): number {
  return ROW_SPACING + (expanded ? expandedCardHeight(s) : 0)
}

/**
 * Extract one file's diff from a commit's full diff (ONE getCommitDiff per
 * commit, sliced per file). Matches by post-commit path first, then by
 * old_path so a renamed file (files list reports the destination) still
 * finds its hunks. Returns undefined when the commit diff has no entry for
 * the path (e.g. merge commits, whose diff-tree emits no patch at all).
 */
export function sliceDiffByPath(diff: ReviewFileDiffLike[], path: string): ReviewFileDiffLike | undefined {
  return diff.find((f) => f.path === path) ?? diff.find((f) => f.old_path === path)
}
