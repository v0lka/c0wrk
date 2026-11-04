// Tier-1 deterministic findings (ADR-105 D5): the four parse-time
// integrity categories. Renders NOTHING when clean — the header's badge is
// the clean signal; this list appears only when there is something to see.

import { AlertTriangle } from 'lucide-react'
import type { SpecFinding } from '@/types/models'
import { findingKindLabel } from './specsPaths'

/** One finding row: the kind label (the four tier-1 categories, generic for
 *  a future tier's unknown kind), the file it is about (clickable — opens
 *  in the file viewer through the panel's onOpen callback), and the detail. */
function FindingRow({
  finding,
  onOpen,
}: {
  finding: SpecFinding
  onOpen: (relPath: string) => void
}) {
  return (
    <div className="flex items-start gap-1.5 text-xs leading-relaxed">
      <AlertTriangle className="mt-0.5 size-3 shrink-0 text-warning" />
      <span className="shrink-0 rounded bg-warning/10 px-1 text-xs text-warning">
        {findingKindLabel(finding.kind)}
      </span>
      <button
        type="button"
        data-testid="specs-finding-file"
        data-file={finding.file}
        title={finding.file}
        onClick={() => onOpen(finding.file)}
        className="shrink-0 rounded px-1 text-info underline decoration-info/40 underline-offset-2 transition-colors hover:bg-muted"
      >
        {finding.file.split('/').pop() ?? finding.file}
      </button>
      <span className="min-w-0 flex-1 break-words text-muted-foreground">
        {finding.detail}
      </span>
    </div>
  )
}

/** The findings section: every parse-time finding, in parse order. */
export function SpecsFindings({
  findings,
  onOpen,
}: {
  findings: SpecFinding[]
  onOpen: (relPath: string) => void
}) {
  if (findings.length === 0) return null
  return (
    <section data-testid="specs-findings" className="flex flex-col gap-0.5">
      <div className="flex items-center gap-1 px-0.5 text-xs uppercase tracking-wide text-muted-foreground">
        <AlertTriangle className="size-3" />
        Findings
      </div>
      {findings.map((finding, i) => (
        <FindingRow
          key={`${finding.kind}-${finding.file}-${i}`}
          finding={finding}
          onOpen={onOpen}
        />
      ))}
    </section>
  )
}
