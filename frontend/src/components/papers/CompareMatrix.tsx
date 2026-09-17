// CompareMatrix — the paper Compare section's first-class renderer for a
// multi-paper comparison artifact.
//
// The comparison lives at `<research-root>/comparisons/<slug>.md` (written by
// the study-paper Compare intent). This component parses it (see
// @/lib/paperComparison) and renders the papers-under-comparison table, the
// dimension × paper matrix, and the template's fairness / agreement / gaps /
// synthesis-verdict sections as real UI instead of a wall of Markdown. Pure
// presentation: all parsing happens in the (unit-tested) lib.

import { useMemo } from 'react'
import { GitCompare } from 'lucide-react'

import { parseComparison, type ParsedComparison } from '@/lib/paperComparison'

interface CompareMatrixProps {
  /** The comparison artifact's slug (its file name without `.md`). */
  slug: string
  /** The raw artifact Markdown. */
  content: string
}

const CELL_CLASS = 'border border-border px-1.5 py-1 align-top text-[10px] text-foreground'

function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <h4 className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
      {children}
    </h4>
  )
}

/** The papers-under-comparison table (one row per paper). */
function PapersTable({ papers }: { papers: ParsedComparison['papers'] }) {
  return (
    <section data-testid="comparison-papers">
      <SectionHeading>Papers under comparison</SectionHeading>
      <div className="overflow-x-auto custom-scrollbar">
        <table className="w-full border-collapse">
          <thead>
            <tr className="text-left text-[10px] uppercase tracking-wide text-muted-foreground">
              <th className={CELL_CLASS}>ID</th>
              <th className={CELL_CLASS}>Short name</th>
              <th className={CELL_CLASS}>Citation</th>
              <th className={CELL_CLASS}>Summary</th>
            </tr>
          </thead>
          <tbody>
            {papers.map((paper, i) => (
              <tr key={i} data-testid="comparison-paper-row">
                <td className={CELL_CLASS}>{paper.id !== '' ? paper.id : '—'}</td>
                <td className={CELL_CLASS}>{paper.shortName}</td>
                <td className={CELL_CLASS}>{paper.citation}</td>
                <td className={CELL_CLASS}>{paper.summary}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

/** The dimension × paper comparison matrix. */
function MatrixTable({
  columns,
  rows,
}: {
  columns: string[]
  rows: ParsedComparison['matrix']
}) {
  return (
    <section data-testid="comparison-grid-section">
      <SectionHeading>Comparison matrix</SectionHeading>
      <div className="overflow-x-auto custom-scrollbar">
        <table data-testid="comparison-grid" className="w-full border-collapse">
          <thead>
            <tr className="text-left text-[10px] uppercase tracking-wide text-muted-foreground">
              <th className={CELL_CLASS}>Dimension</th>
              {columns.map((column, i) => (
                <th key={i} className={CELL_CLASS}>
                  {column}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, i) => (
              <tr key={i} data-testid="comparison-grid-row">
                <th scope="row" className={`${CELL_CLASS} text-left font-medium text-muted-foreground`}>
                  {row.dimension !== '' ? row.dimension : '—'}
                </th>
                {columns.map((_, c) => (
                  <td
                    key={c}
                    data-testid="comparison-grid-cell"
                    className={CELL_CLASS}
                  >
                    {row.cells[c] ?? ''}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function BulletSection({
  testId,
  title,
  items,
}: {
  testId: string
  title: string
  items: string[]
}) {
  return (
    <section data-testid={testId}>
      <SectionHeading>{title}</SectionHeading>
      <ul className="flex flex-col gap-0.5">
        {items.map((item, i) => (
          <li key={i} data-testid={`${testId}-item`} className="text-[11px] text-foreground">
            {item}
          </li>
        ))}
      </ul>
    </section>
  )
}

/** A two/three-column table rendered from a header + rows pair. */
function SimpleTable({
  testId,
  title,
  header,
  rows,
}: {
  testId: string
  title: string
  header: string[]
  rows: string[][]
}) {
  return (
    <section data-testid={testId}>
      <SectionHeading>{title}</SectionHeading>
      <div className="overflow-x-auto custom-scrollbar">
        <table className="w-full border-collapse">
          <thead>
            <tr className="text-left text-[10px] uppercase tracking-wide text-muted-foreground">
              {header.map((h, i) => (
                <th key={i} className={CELL_CLASS}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, i) => (
              <tr key={i} data-testid={`${testId}-row`}>
                {row.map((value, c) => (
                  <td key={c} className={CELL_CLASS}>
                    {value}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

/**
 * Render one comparison artifact. An artifact with nothing recognizable shows
 * an honest empty state instead of a blank region.
 */
export function CompareMatrix({ slug, content }: CompareMatrixProps) {
  const parsed = useMemo(() => parseComparison(content), [content])

  const empty =
    !parsed.hasMatrix &&
    parsed.papers.length === 0 &&
    parsed.fairness.length === 0 &&
    parsed.agreements.length === 0 &&
    parsed.gaps.length === 0 &&
    parsed.verdict.length === 0

  if (empty) {
    return (
      <p
        data-testid="comparison-empty"
        className="px-3 py-4 text-center text-[11px] text-muted-foreground"
      >
        No comparison matrix found in this artifact.
      </p>
    )
  }

  return (
    <article
      data-testid="comparison-matrix"
      data-slug={slug}
      className="flex min-w-0 flex-col gap-3 px-3 py-2"
    >
      {parsed.topic !== '' && (
        <h3
          data-testid="comparison-topic"
          className="flex items-center gap-1 text-[12px] font-semibold text-foreground"
        >
          <GitCompare className="size-3.5 shrink-0 text-info" />
          <span className="truncate">{parsed.topic}</span>
        </h3>
      )}

      {parsed.papers.length > 0 && <PapersTable papers={parsed.papers} />}

      {parsed.hasMatrix && <MatrixTable columns={parsed.columns} rows={parsed.matrix} />}

      {parsed.fairness.length > 0 && (
        <BulletSection testId="comparison-fairness" title="Fairness & comparability" items={parsed.fairness} />
      )}

      {parsed.agreements.length > 0 && (
        <SimpleTable
          testId="comparison-agreements"
          title="Agreement / disagreement"
          header={['Point', 'Agree / disagree', 'Papers & anchors']}
          rows={parsed.agreements.map((a) => [a.point, a.verdict, a.papers])}
        />
      )}

      {parsed.gaps.length > 0 && (
        <BulletSection testId="comparison-gaps" title="Gaps none address" items={parsed.gaps} />
      )}

      {parsed.verdict.length > 0 && (
        <SimpleTable
          testId="comparison-verdict"
          title="Synthesis verdict"
          header={['Field', 'Value']}
          rows={parsed.verdict.map((v) => [v.field, v.value])}
        />
      )}
    </article>
  )
}
