import { useMemo } from 'react'
import { Loader2 } from 'lucide-react'
import { parseHunkRaw, type DiffLine, type ReviewFileDiffLike } from '@/components/review/diffParsing'
import { detectHljsLanguage, highlightCodeLine } from '@/components/review/hunkCodeHighlight'
import { UnifiedDiffTable } from '@/components/review/UnifiedDiffTable'
import { SplitDiffTable } from '@/components/review/SplitDiffTable'
import { STATUS_LINE_HEIGHT } from './gitGraphCardHeights'

interface CommitFileDiffProps {
  /** File path — drives the highlight.js language detection. */
  filePath: string
  /** The file's slice of the commit diff (one getCommitDiff per commit, sliced by path). */
  slice: ReviewFileDiffLike | undefined
  /** The commit's diff has not arrived yet. */
  loading: boolean
  /** The commit's diff fetch failed. */
  failed: boolean
  /** Unified or split rendering — the shared reviewStore preference. */
  diffViewMode: 'unified' | 'split'
  /** Explicit pixel height (from the deterministic height math). */
  height: number
}

/**
 * The diff body of ONE changed file inside an expanded commit card.
 *
 * Renders the file's hunks with the SAME tables as the review page
 * (UnifiedDiffTable / SplitDiffTable + hunkCodeHighlight). All hunks are
 * parsed and concatenated into one flat line list, so both tables render a
 * single continuous block and the deterministic height estimate
 * (`Σ hunk.raw lines × DIFF_ROW_HEIGHT`) holds for the unwrapped case.
 *
 * The block has an explicit pixel height and scrolls internally
 * (overflow-y-auto) beyond it, so a large diff can never push the outer
 * row taller than the reserved height. Loading / failure / hunk-less
 * slices render a single fixed-height placeholder line.
 */
export function CommitFileDiff({ filePath, slice, loading, failed, diffViewMode, height }: CommitFileDiffProps) {
  const diffLines = useMemo<DiffLine[]>(() => {
    if (!slice) return []
    return slice.hunks.flatMap((hunk) => parseHunkRaw(hunk.raw, hunk.old_start, hunk.new_start))
  }, [slice])

  const highlightedLines = useMemo<string[]>(() => {
    const language = detectHljsLanguage(filePath)
    return diffLines.map((line) =>
      highlightCodeLine(
        line.text,
        line.type === 'add' || line.type === 'del' || line.type === 'context' ? language : null,
      ),
    )
  }, [diffLines, filePath])

  if (loading) {
    return (
      <div style={{ height: STATUS_LINE_HEIGHT }} className="flex items-center gap-1.5 px-3 text-xs leading-4 text-muted-foreground">
        <Loader2 className="size-3 animate-spin" /> Loading diff…
      </div>
    )
  }

  if (failed) {
    return (
      <div style={{ height: STATUS_LINE_HEIGHT }} className="flex items-center px-3 text-xs leading-4 text-destructive">
        Failed to load diff
      </div>
    )
  }

  if (!slice || slice.hunks.length === 0 || diffLines.length === 0) {
    return (
      <div style={{ height: STATUS_LINE_HEIGHT }} className="flex items-center px-3 text-xs leading-4 text-muted-foreground">
        No diff
      </div>
    )
  }

  return (
    <div
      style={{ height }}
      className="overflow-y-auto custom-scrollbar rounded border border-border/50"
    >
      {diffViewMode === 'split' ? (
        <SplitDiffTable diffLines={diffLines} highlightedLines={highlightedLines} />
      ) : (
        <UnifiedDiffTable diffLines={diffLines} highlightedLines={highlightedLines} />
      )}
    </div>
  )
}
