// TuningTriState — the Auto/On/Off control every TRI-STATE boolean knob of the
// embedded local model's Advanced tuning section renders (Fit, the KV cache, the
// vision projector).
//
// A boolean override has THREE values, not two: `null` means "the planner
// decides", and the planner can decide OFF — the offload exclusivity rule forces
// `-fit off`, and the memory gate spills the KV cache and the projector reserve
// to host RAM (`-nkvo`, `--no-mmproj-offload`) when the accelerator cannot hold
// them. A two-position switch has no spelling for that state, so it would render
// a fallback-derived ON which "flipping the switch to what it already shows"
// then silently contradicts — and it would leave no path back to unset, since the
// backend's `reset` vocabulary is the only way to clear an override.
//
// So all three booleans share this leaf: Auto sends `{reset:[knob]}`, On/Off send
// the explicit boolean, and the recorded plan's own outcome is quoted beside an
// unset knob (the caller passes the clause) so Auto is never a guess.

import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { TRI_ON, TUNING_AUTO, type TriStateKnob } from '@/lib/embeddedTuningDisplay'
import type { EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'

export function TuningTriState({
  knob,
  label,
  value,
  options,
  hint,
  planned,
  disabled,
  onSet,
}: {
  knob: TriStateKnob
  /** The visible label AND the combobox's accessible name. */
  label: string
  /** The tri-state spelling: 'auto' | 'on' | 'off'. */
  value: string
  options: readonly ComboboxOption[]
  /** The knob's description sentence, WITHOUT its terminating period. */
  hint: string
  /** The clause quoting the recorded plan's own outcome, or null while no plan
   *  was recorded. */
  planned: string | null
  disabled: boolean
  onSet: (patch: EmbeddedLLMTuningPatch) => void
}) {
  return (
    <div className="flex flex-col gap-1" data-testid={`embedded-llm-tuning-${knob}`}>
      <label className="text-xs text-muted-foreground">{label}</label>
      <Combobox
        ariaLabel={label}
        value={value}
        options={options}
        className="h-8 w-44 text-xs"
        disabled={disabled}
        onChange={(mode) =>
          onSet(
            mode === TUNING_AUTO
              ? { reset: [knob] }
              : ({ [knob]: mode === TRI_ON } as EmbeddedLLMTuningPatch),
          )
        }
      />
      <p className="text-xs text-muted-foreground">
        {hint}
        {planned === null ? '.' : ` — ${planned}.`}
      </p>
    </div>
  )
}
