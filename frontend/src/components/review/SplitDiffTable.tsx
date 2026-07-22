import { useMemo } from 'react'
import { DiffLine, LINE_BG, buildSideBySidePairs } from './diffParsing'

interface SplitDiffTableProps {
  diffLines: DiffLine[]
  highlightedLines: string[]
}

/**
 * Side-by-side diff view rendered as a single `<table>`.
 *
 * A single table is intentional: the browser keeps vertical scroll
 * synchronous across both columns automatically, so no manual scroll-sync
 * logic is required. Each row has four cells:
 *
 *   oldNum | oldContent | newNum | newContent
 *
 * Per-cell background tints distinguish the line type:
 *  - del content (left side)  → `bg-destructive/10`
 *  - add content (right side) → `bg-success/10`
 *  - context                  → neutral (no tint)
 *  - header / noNewline       → the row spans both sides with its `LINE_BG` style
 *
 * A padded cell (its side has no line) renders an empty spacer and shows no
 * number. Syntax highlighting is preserved via `dangerouslySetInnerHTML`,
 * exactly as the unified table does.
 */
export function SplitDiffTable({ diffLines, highlightedLines }: SplitDiffTableProps) {
  // Memoized: `diffLines` is referentially stable (memoized in the parent),
  // so the alignment only re-runs when the parsed hunk actually changes.
  const rows = useMemo(() => buildSideBySidePairs(diffLines), [diffLines])

  return (
    <div className="overflow-x-auto font-mono text-xs leading-relaxed">
      {/* table-fixed + colgroup guarantee the two content columns split the
       * remaining width 50/50, so left and right sides are always equally
       * wide regardless of line length. */}
      <table className="w-full border-collapse table-fixed">
        <colgroup>
          <col className="w-10" />
          <col />
          <col className="w-10" />
          <col />
        </colgroup>
        <tbody>
          {rows.map((row, i) => {
            const { leftIdx, rightIdx } = row

            // Resolve indices to lines (null where padded). Indexing can
            // yield undefined under noUncheckedIndexedAccess, so coerce.
            const leftLine = leftIdx === null ? null : diffLines[leftIdx] ?? null
            const rightLine = rightIdx === null ? null : diffLines[rightIdx] ?? null
            const leftHtml = leftIdx === null ? null : highlightedLines[leftIdx] ?? null
            const rightHtml = rightIdx === null ? null : highlightedLines[rightIdx] ?? null

            // Same line on both sides (context / header / noNewline).
            if (leftLine !== null && leftIdx === rightIdx) {
              if (leftLine.type === 'context') {
                // Context appears on both sides, neutral background.
                return (
                  <tr key={i}>
                    {numCell(leftLine.oldNum)}
                    {contentCell(leftHtml, '')}
                    {numCell(leftLine.newNum)}
                    {contentCell(leftHtml, '')}
                  </tr>
                )
              }
              // header / noNewline — spans both sides with its LINE_BG style.
              return (
                <tr key={i} className={LINE_BG[leftLine.type]}>
                  <td colSpan={4} className="pr-3 whitespace-pre-wrap break-all">
                    <span dangerouslySetInnerHTML={{ __html: leftHtml || ' ' }} />
                  </td>
                </tr>
              )
            }

            // del (left) and/or add (right), possibly with a padded side.
            return (
              <tr key={i}>
                {numCell(leftLine?.oldNum ?? null)}
                {contentCell(leftHtml, 'bg-destructive/10')}
                {numCell(rightLine?.newNum ?? null)}
                {contentCell(rightHtml, 'bg-success/10')}
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

/** A gutter number cell, or an empty spacer when `num` is null. */
function numCell(num: number | null) {
  if (num === null) {
    return <td className="select-none w-10" />
  }
  return (
    <td className="select-none text-right pr-2 pl-2 text-muted-foreground/50 w-10 tabular-nums align-top">
      {num}
    </td>
  )
}

/**
 * A code content cell. `html` is the pre-highlighted markup (or null for a
 * padded empty side); `bg` is the per-cell background class.
 *
 * A padded side (html === null) renders a blank cell with NO background tint.
 * This keeps a purely-added file from showing a red left column (and a purely
 * deleted file from showing a green right column) — only cells that hold an
 * actual line are tinted, matching GitHub's split-view behaviour.
 */
function contentCell(html: string | null, bg: string) {
  if (html === null) {
    return <td className="whitespace-pre-wrap break-all" />
  }
  return (
    <td
      className={`pr-3 whitespace-pre-wrap break-all ${bg}`}
      dangerouslySetInnerHTML={{ __html: html || ' ' }}
    />
  )
}
