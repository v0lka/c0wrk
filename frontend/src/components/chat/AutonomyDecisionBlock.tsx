import { memo } from 'react'
import { AlertTriangle, Check, X } from 'lucide-react'
import type { DisplayItem } from '@/types/messages'
import type { AutonomyDecisionData } from '@/types/events'
import { isAutonomyDecisionData } from '@/types/events'
import { cn } from '@/lib/utils'
import {
  autonomyDecisionTitle,
  autonomyDecisionVerdictLine,
  isAutonomyAllowVerdict,
} from '@/lib/autonomyDecision'

type AutonomyItem = Extract<DisplayItem, { kind: 'autonomy_decision' }>

interface AutonomyDecisionBlockProps {
  item: AutonomyItem
}

/**
 * Card chrome per verdict tone. These are complete literal class strings —
 * never assembled by interpolation — so Tailwind's scanner emits them. Mirrors
 * the resolved branches of ToolConfirmation / PlanApprovalPanel, which reuse
 * the exact same `rounded-md border …/30 bg-…/5 px-3 py-2` shell.
 */
const toneClasses = {
  success: { card: 'border-success/30 bg-success/5', icon: 'text-success' },
  destructive: { card: 'border-destructive/30 bg-destructive/5', icon: 'text-destructive' },
  muted: { card: 'border-border bg-background/50', icon: 'text-muted-foreground' },
} as const

type Tone = keyof typeof toneClasses

/**
 * The verdict decides the glyph and the tone: an ALLOW-family verdict is a
 * green Check, a DENY a red X, and anything else (a malformed/unknown verdict
 * on an otherwise valid payload) a muted AlertTriangle — fail-safe, never
 * mis-painted as an allow or a deny.
 */
function verdictTone(data: AutonomyDecisionData): Tone {
  if (isAutonomyAllowVerdict(data.verdict)) return 'success'
  if (data.verdict === 'deny') return 'destructive'
  return 'muted'
}

function VerdictIcon({ tone }: { tone: Tone }) {
  const cls = cn('h-3.5 w-3.5 shrink-0', toneClasses[tone].icon)
  if (tone === 'success') return <Check className={cls} />
  if (tone === 'destructive') return <X className={cls} />
  return <AlertTriangle className={cls} />
}

/** One labeled audit row; rendered only when its field carries a value. */
function DecisionRow({ label, value }: { label: string; value: string }) {
  return (
    <p className="text-xs text-muted-foreground">
      <span className="text-muted-foreground/60">{label}:</span>{' '}
      <span className="text-foreground/80">{value}</span>
    </p>
  )
}

/**
 * Standard-format audit card for a `autonomy_decision` row — a tool-call gate
 * (a confirmation-gated call resolved without a human, or a strict-judge
 * auto-deny) or a step-limit boundary decided by the backend. The header names
 * the gate; the icon/tone encode the verdict; the body is the verdict line plus
 * the Mode/Policy/Reason/Justification audit rows. Non-blocking — it records a
 * decision a human would otherwise have made (OWASP ASI10).
 */
export const AutonomyDecisionBlock = memo(function AutonomyDecisionBlock({ item }: AutonomyDecisionBlockProps) {
  const metadata = item.message.metadata

  // Malformed / foreign payload: render a neutral muted card instead of
  // reaching for `kind`/`verdict` string operations that could throw mid-render.
  if (!isAutonomyDecisionData(metadata)) {
    return (
      <div className="rounded-md border border-border bg-background/50 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span>Autonomy Decision</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">
          {item.message.content || 'Automatic security decision'}
        </p>
      </div>
    )
  }

  const tone = verdictTone(metadata)

  const rows: Array<{ label: string; value: string }> = []
  if (metadata.mode) rows.push({ label: 'Mode', value: metadata.mode })
  if (metadata.policy) rows.push({ label: 'Policy', value: metadata.policy })
  if (metadata.reason) rows.push({ label: 'Reason', value: metadata.reason })
  if (metadata.justification) rows.push({ label: 'Justification', value: metadata.justification })

  return (
    <div className={cn('rounded-md border px-3 py-2', toneClasses[tone].card)}>
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <VerdictIcon tone={tone} />
        <span>{autonomyDecisionTitle(metadata)}</span>
      </div>
      <div className="mt-1.5 space-y-1">
        <p className="text-xs text-foreground">{autonomyDecisionVerdictLine(metadata)}</p>
        {rows.map((row) => (
          <DecisionRow key={row.label} label={row.label} value={row.value} />
        ))}
      </div>
    </div>
  )
})
