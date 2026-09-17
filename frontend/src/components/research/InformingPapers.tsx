// InformingPapers / DanglingPaperLinks — the rendered faces of the reverse
// paper ← hypothesis projection (lib/papersByHypothesis.ts).
//
// `InformingPapers` is the hypothesis card's read-only section: "Informing
// papers (N)" listing every paper whose card names this H-NNN, each opening
// the paper's reader tab on click. `DanglingPaperLinks` is the research
// dashboard's warning: paper cards that name an H-NNN resolving to no known
// hypothesis — surfaced instead of silently dropped.
//
// Both subscribe to the same memoized index (no object/array allocated inside
// a Zustand selector — see the lib module) and open papers through the file
// viewer's synthetic paper tab (`openPaper(slug)`).

import { useCallback } from 'react'
import { AlertTriangle, FileText } from 'lucide-react'
import { useDanglingPaperLinks, useInformingPapers } from '@/lib/papersByHypothesis'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import type { PaperRecord } from '@/api/papers'

/** Open a paper's reader workspace tab (the synthetic `c0wrk:paper:<slug>`
 *  pseudo-path); `openPaper` uncollapses the file viewer. */
function useOpenPaper(): (paper: PaperRecord) => void {
  return useCallback((paper: PaperRecord) => {
    if (paper.slug === '') return
    const store = useFileViewerStore.getState()
    store.setCollapsed(false)
    store.openPaper(paper.slug)
  }, [])
}

function paperLabel(paper: PaperRecord): string {
  return paper.title !== '' ? paper.title : paper.slug
}

/**
 * The "Informing papers (N)" list for one hypothesis. Renders nothing when the
 * hypothesis has no informing paper, so a card without the reverse link stays
 * uncluttered.
 */
export function InformingPapers({ hypothesisId }: { hypothesisId: string }) {
  const papers = useInformingPapers(hypothesisId)
  const openPaper = useOpenPaper()

  if (papers.length === 0) return null

  return (
    <section data-testid="informing-papers" className="flex flex-col gap-1">
      <h4 className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
        Informing papers ({papers.length})
      </h4>
      <ul className="flex flex-col gap-0.5">
        {papers.map((paper) => (
          <li key={paper.id}>
            <button
              type="button"
              data-testid={`informing-paper-${paper.id}`}
              onClick={() => openPaper(paper)}
              title={`Open ${paperLabel(paper)}`}
              aria-label={`Open paper ${paperLabel(paper)}`}
              className="flex w-full items-baseline gap-1.5 rounded px-1 py-0.5 text-left text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            >
              <FileText className="size-3 shrink-0 self-center" />
              <span className="shrink-0 font-mono text-[10px]">{paper.id}</span>
              <span className="min-w-0 truncate">{paperLabel(paper)}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  )
}

/**
 * A compact warning listing paper cards that name an H-NNN resolving to no
 * known hypothesis (across the active research root). Renders nothing when
 * there are no dangling links.
 */
export function DanglingPaperLinks() {
  const dangling = useDanglingPaperLinks()
  const openPaper = useOpenPaper()

  if (dangling.size === 0) return null

  const rows: { id: string; paper: PaperRecord }[] = []
  for (const [id, papers] of dangling) {
    for (const paper of papers) rows.push({ id, paper })
  }

  return (
    <div
      data-testid="dangling-paper-links"
      className="flex flex-col gap-1 rounded-md border border-warning/30 bg-warning/10 px-2 py-1"
    >
      <div className="flex items-center gap-1.5 text-[11px] text-warning">
        <AlertTriangle className="size-3 shrink-0" />
        <span>
          {rows.length} paper link{rows.length === 1 ? '' : 's'} reference an unknown
          hypothesis
        </span>
      </div>
      <ul className="flex flex-col gap-0.5">
        {rows.map(({ id, paper }) => (
          <li key={`${id}:${paper.id}`} className="flex items-baseline gap-1.5 text-[11px]">
            <button
              type="button"
              data-testid={`dangling-paper-${paper.id}-${id}`}
              onClick={() => openPaper(paper)}
              title={`Open ${paperLabel(paper)}`}
              aria-label={`Open paper ${paperLabel(paper)}`}
              className="shrink-0 font-mono text-[10px] text-warning underline-offset-2 hover:underline"
            >
              {paper.id}
            </button>
            <span className="font-mono text-[10px] text-muted-foreground">{id}</span>
            <span className="min-w-0 truncate text-muted-foreground">{paperLabel(paper)}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
