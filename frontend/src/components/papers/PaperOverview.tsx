// Overview section — the paper's identity card, its source anchors (E1
// entry points) and the critical layer as widgets (E2).

import type { PaperAnchor, PaperRecord } from '@/api/papers'
import type { CriticalLayer } from '@/lib/paperWidgets'
import { cn } from '@/lib/utils'
import { CriticalLayerWidgets } from './CriticalLayerWidgets'
import { PaperAnchorList } from './PaperAnchorList'

const BADGE_CLASS =
  'inline-flex items-center rounded px-1 py-0 text-[10px] font-medium uppercase tracking-wide'

function verdictTone(verdict: PaperRecord['verdict']): string {
  switch (verdict) {
    case 'accepted':
      return 'bg-success/15 text-success'
    case 'rejected':
      return 'bg-destructive/15 text-destructive'
    case 'uncertain':
      return 'bg-warning/15 text-warning'
    default:
      return 'bg-muted text-muted-foreground'
  }
}

function confidenceTone(confidence: PaperRecord['confidence']): string {
  switch (confidence) {
    case 'high':
      return 'bg-success/15 text-success'
    case 'medium':
      return 'bg-info/15 text-info'
    case 'low':
      return 'bg-muted text-muted-foreground'
    default:
      return 'bg-muted text-muted-foreground'
  }
}

function Meta({ label, value }: { label: string; value: string }) {
  if (value === '') return null
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="truncate text-foreground">{value}</dd>
    </>
  )
}

function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <h4 className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
      {children}
    </h4>
  )
}

interface PaperOverviewProps {
  paper: PaperRecord
  layer: CriticalLayer
  /** Index of the anchor whose last resolution failed (null = none). */
  missedIndex: number | null
  onAnchorSelect: (anchor: PaperAnchor, index: number) => void
}

export function PaperOverview({
  paper,
  layer,
  missedIndex,
  onAnchorSelect,
}: PaperOverviewProps) {
  const identifiers = paper.identifiers
    .map((id) => (id.scheme !== '' ? `${id.scheme}:${id.value}` : id.value))
    .filter((value) => value !== '')

  return (
    <div
      data-testid="paper-overview"
      className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto custom-scrollbar px-3 py-2"
    >
      <section data-testid="paper-identity" className="flex flex-col gap-1">
        <h3 className="text-[13px] font-semibold text-foreground">
          {paper.title !== '' ? paper.title : paper.slug}
        </h3>
        {paper.authors.length > 0 && (
          <p className="text-[11px] text-muted-foreground">{paper.authors.join(', ')}</p>
        )}
        <div className="flex flex-wrap items-center gap-1">
          {paper.mode !== '' && (
            <span data-testid="paper-mode" className={cn(BADGE_CLASS, 'bg-info/15 text-info')}>
              {paper.mode}
            </span>
          )}
          {paper.reading !== '' && (
            <span
              data-testid="paper-reading"
              className={cn(BADGE_CLASS, 'bg-muted text-muted-foreground')}
            >
              {paper.reading}
            </span>
          )}
          {paper.verdict !== '' && (
            <span data-testid="paper-verdict" className={cn(BADGE_CLASS, verdictTone(paper.verdict))}>
              {paper.verdict}
            </span>
          )}
          {paper.confidence !== '' && (
            <span
              data-testid="paper-confidence"
              className={cn(BADGE_CLASS, confidenceTone(paper.confidence))}
            >
              {paper.confidence}
            </span>
          )}
        </div>
        <dl className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 text-[11px]">
          <Meta label="ID" value={paper.id} />
          <Meta label="Year" value={paper.year > 0 ? String(paper.year) : ''} />
          <Meta label="Venue" value={paper.venue} />
          <Meta label="Identifiers" value={identifiers.join(' · ')} />
        </dl>
      </section>

      {paper.linked_research.length > 0 && (
        <section data-testid="paper-research-links">
          <SectionHeading>Research links</SectionHeading>
          <div className="flex flex-wrap gap-1">
            {paper.linked_research.map((link, i) => (
              <span
                key={i}
                className={cn(BADGE_CLASS, 'bg-muted normal-case text-muted-foreground')}
              >
                {link.hypothesis_id}
                {link.research_id !== '' && (
                  <span className="ml-1 opacity-70">{link.research_id}</span>
                )}
              </span>
            ))}
          </div>
        </section>
      )}

      <section data-testid="paper-anchor-section">
        <SectionHeading>Anchors</SectionHeading>
        <PaperAnchorList
          anchors={paper.anchors}
          missedIndex={missedIndex}
          onSelect={onAnchorSelect}
        />
      </section>

      <section data-testid="paper-critical-section">
        <SectionHeading>Critical layer</SectionHeading>
        <CriticalLayerWidgets layer={layer} />
      </section>
    </div>
  )
}
