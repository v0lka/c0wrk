import { DiffLine, LINE_BG } from './diffParsing'

interface UnifiedDiffTableProps {
  diffLines: DiffLine[]
  highlightedLines: string[]
}

/**
 * Single-table unified diff view (oldNum | newNum | content).
 *
 * This is a faithful extraction of the diff table that previously lived
 * inline in `HunkReviewBlock`. Rendering is byte-identical: the same
 * three-column layout, the same `LINE_BG` row tinting, and the same
 * `dangerouslySetInnerHTML` syntax-highlight injection (one `<table>` so
 * the whole hunk scrolls as one block).
 *
 * Presentational only — the caller pre-parses `diffLines` (via
 * `parseHunkRaw`) and pre-highlights `highlightedLines` so this component
 * has no parsing or highlighting concerns.
 */
export function UnifiedDiffTable({ diffLines, highlightedLines }: UnifiedDiffTableProps) {
  return (
    <div className="overflow-x-auto custom-scrollbar font-mono text-xs leading-relaxed">
      <table className="w-full border-collapse">
        <tbody>
          {diffLines.map((line, i) => (
            <tr key={i} className={LINE_BG[line.type]}>
              <td className="select-none text-right pr-2 pl-2 text-muted-foreground/50 w-10 tabular-nums align-top">
                {line.oldNum ?? ''}
              </td>
              <td className="select-none text-right pr-2 text-muted-foreground/50 w-10 tabular-nums align-top">
                {line.newNum ?? ''}
              </td>
              <td
                className="pr-3 whitespace-pre-wrap break-all"
                dangerouslySetInnerHTML={{ __html: highlightedLines[i] || ' ' }}
              />
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
