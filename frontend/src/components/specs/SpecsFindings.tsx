// Tier-1 deterministic findings (ADR-106 D5, ADR-109): the nine parse-time
// integrity categories. Renders NOTHING when clean — the header's badge is
// the clean signal; this list appears only when there is something to see.

import { AlertTriangle } from 'lucide-react'
import type { SpecFinding } from '@/types/models'
import { CopyIconButton } from '@/components/CopyIconButton'
import { findingKindLabel } from './specsPaths'
import { findingLine } from './specsActions'

/** One finding card, cascading on two rows: row 1 carries the fix badge —
 *  a BUTTON (the icon + kind chip, the header badge's warning styling)
 *  dispatching the per-finding spec-update task — and the file link
 *  (clickable — opens in the file viewer through the panel's onOpen
 *  callback), row 2 the detail text (flex-1 — a long detail must never
 *  squeeze the row-1 controls) with the hover-revealed copy button pinned
 *  to its trailing edge: the card is the Tailwind hover `group`, the copy
 *  wrapper sits at opacity-0 until the card is hovered (or focused into —
 *  focus-within) and copies the verbatim findingLine — the exact bullet
 *  the dispatched fix prompt carries, so the clipboard never drifts from
 *  what the task sees (ADR-109 D7). The reveal is opacity-only and the
 *  wrapper always occupies its layout slot: hovering must not move a
 *  single pixel (the MessageFooter pattern). */
function FindingRow({
  finding,
  onOpen,
  onFixFinding,
}: {
  finding: SpecFinding
  onOpen: (relPath: string) => void
  onFixFinding: (finding: SpecFinding, newSession: boolean) => void
}) {
  return (
    <div
      data-testid="specs-finding"
      className="group flex flex-col gap-0.5 rounded bg-secondary/40 px-1.5 py-1"
    >
      <div className="flex items-center gap-1.5 text-xs">
        <button
          type="button"
          data-testid="specs-finding-fix"
          title="Fix this finding in the chat — Shift = new session"
          onClick={(e) => onFixFinding(finding, e.shiftKey)}
          className="inline-flex shrink-0 items-center gap-1 rounded bg-warning/10 px-1 text-xs text-warning transition-colors hover:bg-warning/20"
        >
          <AlertTriangle className="size-3" />
          {findingKindLabel(finding.kind)}
        </button>
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
      </div>
      <div className="flex items-end gap-1 text-xs">
        <div
          data-testid="specs-finding-detail"
          className="min-w-0 flex-1 break-words leading-relaxed text-muted-foreground"
        >
          {finding.detail}
        </div>
        <div
          data-testid="specs-finding-copy"
          className="ml-auto shrink-0 opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover:opacity-100"
        >
          <CopyIconButton text={findingLine(finding)} label="Copy finding line" />
        </div>
      </div>
    </div>
  )
}

/** The findings section: every parse-time finding, in parse order. */
export function SpecsFindings({
  findings,
  onOpen,
  onFixFinding,
}: {
  findings: SpecFinding[]
  onOpen: (relPath: string) => void
  onFixFinding: (finding: SpecFinding, newSession: boolean) => void
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
          onFixFinding={onFixFinding}
        />
      ))}
    </section>
  )
}
