// TuningDevices — the `-dev` offload target checklist of the embedded local
// model's Advanced tuning section. One toggle row per MEASURED accelerator (the
// status snapshot's own inventory — the exact names `llama-server
// --list-devices` prints and `-dev` consumes), empty selection = Auto.
//
// The consequence a toggle carries is stated in the hint, because it is not a
// display detail: pinning devices turns `--fit` OFF (a chosen offload sizes
// nothing itself), the planner then prices the plan against ONLY the named
// devices' pool, and a name the machine no longer reports refuses the LOAD —
// fail-closed, never a silent launch on different devices.
//
// A fully controlled leaf with ZERO local state, like every tuning leaf: the
// selection lives in the persisted tuning snapshot, every toggle commits one
// patch through the shared `onSet` seam, and clearing the last checked device
// sends the `reset` spelling rather than an empty list.

import { Check } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { EmbeddedLLMDevice, EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'

/** MiB with a GiB promotion at the 1024 boundary; whole GiB without decimals
 *  (the install record's own vocabulary, kept leaf-local like its original). */
function formatMiB(mib: number): string {
  if (mib >= 1024) {
    const gib = mib / 1024
    return `${Number.isInteger(gib) ? gib : gib.toFixed(1)} GiB`
  }
  return `${mib} MiB`
}

export function TuningDevices({
  devices,
  selected,
  planned,
  probedAt,
  disabled,
  onSet,
}: {
  /** The measured inventory — the rows. Empty when no probe ever answered. */
  devices: readonly EmbeddedLLMDevice[]
  /** The persisted `-dev` override: null/empty = Auto (every device). */
  selected: readonly string[] | null
  /** The recorded plan's pinned devices, or null while nothing was recorded —
   *  quoted beside an unset selection so Auto is never a guess. */
  planned: readonly string[] | null
  /** RFC 3339 stamp of the last probe that answered; "" = none. */
  probedAt: string
  disabled: boolean
  onSet: (patch: EmbeddedLLMTuningPatch) => void
}) {
  const pinned = selected !== null && selected.length > 0
  const toggle = (name: string): void => {
    const current = pinned ? selected : []
    const next = current.includes(name) ? current.filter((n) => n !== name) : [...current, name]
    onSet(next.length > 0 ? { devices: next } : { reset: ['devices'] })
  }
  const plannedClause =
    planned !== null && planned.length > 0
      ? `the recorded plan pins ${planned.join(', ')}`
      : planned === null
        ? null
        : 'the recorded plan leaves the devices to the runtime'

  return (
    <div className="flex flex-col gap-1" data-testid="embedded-llm-tuning-devices">
      <label className="text-xs text-muted-foreground">Offload devices</label>
      {devices.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No device probe has answered yet{probedAt === '' ? '' : ` (last probe ${probedAt})`} —
          Auto stays in force until one does.
        </p>
      ) : (
        <div className="flex flex-col gap-1" role="group" aria-label="Offload devices">
          {devices.map((device) => {
            const checked = pinned && selected.includes(device.name)
            return (
              <Button
                key={device.name}
                variant="outline"
                size="sm"
                className={cn(
                  'h-8 justify-start gap-2 px-2 font-normal',
                  checked && 'border-primary/60 bg-primary/10',
                )}
                disabled={disabled}
                aria-pressed={checked}
                aria-label={`Offload to ${device.name}`}
                title={`Offload to ${device.name}`}
                onClick={() => toggle(device.name)}
              >
                <span
                  className={cn(
                    'flex size-4 items-center justify-center rounded-sm border',
                    checked ? 'border-primary bg-primary/20' : 'border-border/60',
                  )}
                >
                  {checked && <Check className="size-3" aria-hidden />}
                </span>
                <span className="truncate">
                  {device.name} — {device.description}
                </span>
                <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                  {formatMiB(device.free_mib)} free of {formatMiB(device.total_mib)}
                </span>
              </Button>
            )
          })}
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        Auto uses every device the runtime finds. Picking devices pins the offload (--dev), turns
        fit off, and prices the plan for the picked pool alone; a name the machine no longer
        reports refuses the load. Unchecking every device returns to Auto.
        {probedAt === '' ? '' : ` Probed ${probedAt}.`}
      </p>
      <p className="text-xs text-muted-foreground">
        {plannedClause === null ? null : `${plannedClause}.`}
      </p>
    </div>
  )
}
