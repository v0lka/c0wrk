// The active-framework selector (the panel header).
//
// It exists ONLY while more than one SDD framework is detected for the parsed
// workspace — a single detected framework IS the answer, so offering a choice
// of one would be noise. The list, the labels and the ids all come from the
// detection payload (nothing is hardcoded), and a selection persists per
// project through SetActiveSDD, surviving an app restart.
//
// While the switch is in flight the control is disabled but already shows the
// CHOSEN framework: the panel keeps the PREVIOUS tree on screen until the
// re-parse lands (the store's last-write-wins ticket owns that window), so the
// selector is the one surface that must answer "what did I just pick" without
// waiting for the new parse.

import { ChevronDown } from 'lucide-react'
import type { SDDFramework } from '@/types/models'

export function SpecsFrameworkSelector({
  frameworks,
  activeId,
  onSelect,
  switching = false,
}: {
  /** Every detected framework, in detection default order. */
  frameworks: SDDFramework[]
  /** The selection to render (the operator's intent, including a pending
   *  switch); "" when the backend has not resolved one. */
  activeId: string
  onSelect: (id: string) => void
  /** A switch (persist + re-parse) is in flight. */
  switching?: boolean
}) {
  // A single detected framework is not a choice.
  if (frameworks.length <= 1) return null
  const value = frameworks.some((fw) => fw.id === activeId) ? activeId : frameworks[0]!.id
  const active = frameworks.find((fw) => fw.id === value)
  return (
    <label
      data-testid="specs-framework-selector"
      title={
        active?.description !== undefined && active.description !== ''
          ? `${active.label} — ${active.description}`
          : 'Active specification framework'
      }
      className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground"
    >
      <span className="sr-only">Specification framework</span>
      <span className="relative flex min-w-0 items-center">
        <select
          data-testid="specs-framework-select"
          value={value}
          disabled={switching}
          aria-label="Specification framework"
          onChange={(e) => onSelect(e.target.value)}
          className="min-w-0 max-w-[160px] appearance-none truncate rounded border border-border bg-background py-0.5 pl-1.5 pr-5 text-xs text-foreground transition-colors hover:bg-muted disabled:opacity-60"
        >
          {frameworks.map((fw) => (
            <option key={fw.id} value={fw.id}>
              {fw.label}
            </option>
          ))}
        </select>
        <ChevronDown className="pointer-events-none absolute right-1 size-3 text-muted-foreground" aria-hidden />
      </span>
    </label>
  )
}
