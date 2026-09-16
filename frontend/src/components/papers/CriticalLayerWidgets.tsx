// Critical-layer widgets (E2) — the evidence matrix, red-flag chips and
// uncertainty chips rendered as first-class UI instead of a Markdown table.
//
// Pure presentation: the layer is pre-parsed (see @/lib/paperWidgets). Evidence
// strength is colour-coded (present / weak / absent), red flags are capped at
// the skill's three (MAX_RED_FLAG_CHIPS), and every chip carries its detail as
// a tooltip.

import type { CriticalLayer, EvidenceStrength } from '@/lib/paperWidgets'
import { cn } from '@/lib/utils'

const BADGE_CLASS =
  'inline-flex items-center rounded px-1 py-0 text-[10px] font-medium uppercase tracking-wide'

/** Colour tokens per evidence strength (design tokens only). */
const STRENGTH_META: Record<EvidenceStrength, { label: string; tone: string }> = {
  present: { label: 'present', tone: 'bg-success/15 text-success' },
  weak: { label: 'weak', tone: 'bg-warning/15 text-warning' },
  absent: { label: 'absent', tone: 'bg-destructive/15 text-destructive' },
  unknown: { label: 'unknown', tone: 'bg-muted text-muted-foreground' },
}

/** Colour tokens per red-flag severity (free-form tag). */
function severityTone(severity: string): string {
  const s = severity.toLowerCase()
  if (/high|critical|severe/.test(s)) return 'bg-destructive/15 text-destructive'
  if (/med|moderate/.test(s)) return 'bg-warning/15 text-warning'
  if (/low|minor/.test(s)) return 'bg-info/15 text-info'
  return 'bg-muted text-muted-foreground'
}

function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <h4 className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
      {children}
    </h4>
  )
}

/** The evidence matrix: one row per load-bearing claim with its colour-coded
 *  strength. */
function EvidenceMatrix({ rows }: { rows: CriticalLayer['evidence'] }) {
  return (
    <section data-testid="paper-evidence-matrix">
      <SectionHeading>Claim → evidence</SectionHeading>
      <ul className="flex flex-col gap-1">
        {rows.map((row, i) => (
          <li
            key={i}
            data-testid="paper-evidence-row"
            className="flex items-start gap-1.5 rounded border border-border bg-background/40 px-1.5 py-1"
          >
            <span
              data-testid="paper-evidence-strength"
              data-strength={row.strength}
              className={cn(BADGE_CLASS, 'mt-0.5 shrink-0', STRENGTH_META[row.strength].tone)}
            >
              {STRENGTH_META[row.strength].label}
            </span>
            <div className="min-w-0 flex-1">
              <p className="text-[11px] text-foreground">{row.claim !== '' ? row.claim : '—'}</p>
              {row.evidence !== '' && (
                <p className="text-[10px] text-muted-foreground">{row.evidence}</p>
              )}
              {row.anchor !== '' && <p className="text-[10px] text-info">{row.anchor}</p>}
            </div>
          </li>
        ))}
      </ul>
    </section>
  )
}

/** Red-flag chips (≤3), colour-coded by severity. */
function RedFlagChips({ chips }: { chips: CriticalLayer['redFlags'] }) {
  return (
    <section data-testid="paper-red-flags">
      <SectionHeading>Red flags</SectionHeading>
      <div className="flex flex-wrap gap-1">
        {chips.map((chip, i) => (
          <span
            key={i}
            data-testid="paper-red-flag-chip"
            title={chip.detail !== '' ? chip.detail : undefined}
            className={cn(BADGE_CLASS, severityTone(chip.severity))}
          >
            {chip.flag}
            {chip.severity !== '' && <span className="ml-1 opacity-70">{chip.severity}</span>}
          </span>
        ))}
      </div>
    </section>
  )
}

/** Uncertainty chips. */
function UncertaintyChips({ chips }: { chips: CriticalLayer['uncertainties'] }) {
  return (
    <section data-testid="paper-uncertainties">
      <SectionHeading>Uncertainty</SectionHeading>
      <div className="flex flex-wrap gap-1">
        {chips.map((chip, i) => (
          <span
            key={i}
            data-testid="paper-uncertainty-chip"
            title={chip.detail !== '' ? chip.detail : undefined}
            className={cn(BADGE_CLASS, 'bg-info/15 text-info normal-case')}
          >
            {chip.item}
          </span>
        ))}
      </div>
    </section>
  )
}

/**
 * Render the critical layer. An empty layer (no matrix, flags or uncertainty)
 * shows an honest empty state instead of a blank region.
 */
export function CriticalLayerWidgets({ layer }: { layer: CriticalLayer }) {
  const { evidence, redFlags, uncertainties } = layer
  if (evidence.length === 0 && redFlags.length === 0 && uncertainties.length === 0) {
    return (
      <p data-testid="paper-critical-empty" className="text-[11px] text-muted-foreground">
        No critical layer recorded yet.
      </p>
    )
  }
  return (
    <div data-testid="paper-critical-layer" className="flex flex-col gap-2.5">
      {evidence.length > 0 && <EvidenceMatrix rows={evidence} />}
      {redFlags.length > 0 && <RedFlagChips chips={redFlags} />}
      {uncertainties.length > 0 && <UncertaintyChips chips={uncertainties} />}
    </div>
  )
}
