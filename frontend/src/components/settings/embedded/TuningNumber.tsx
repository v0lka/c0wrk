// TuningNumber — ONE numeric knob of the embedded local model's Advanced tuning
// section: the shared NumberField plus that knob's Auto affordance.
//
// Lifted out of EmbeddedLLMAdvancedTuning so the section module holds only the
// layout, and so the Auto affordance has a single copy across the five knobs it
// serves. A knob the operator never overrode shows "Auto" beside the effective
// default; a set knob gets an Auto BUTTON that clears the override back to unset
// (`reset`), because "unset — the planner decides" is a different value from any
// explicit number (an explicit `cache_ram_mib: 0` DISABLES the prompt cache,
// while unset merely omits `--cache-ram`).

import { Button } from '@/components/ui/button'
import { NumberField } from '@/components/settings/NumberField'
import type { EmbeddedLLMTuningKnob, EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'

/** The numeric knobs this leaf can edit, spelled as the patch/YAML key. */
type NumericKnob = Extract<
  EmbeddedLLMTuningKnob,
  'fit_target_mib' | 'fit_min_context' | 'parallel' | 'cache_ram_mib' | 'host_reserve_gib'
>

export function TuningNumber({
  label,
  knob,
  value,
  auto,
  min,
  max,
  step,
  disabled,
  onSet,
}: {
  label: string
  knob: NumericKnob
  value: number
  /** True while the override is unset — the value on screen is a fallback. */
  auto: boolean
  min: number
  max?: number
  step?: number
  disabled: boolean
  onSet: (patch: EmbeddedLLMTuningPatch) => void
}) {
  return (
    <div className="flex items-end gap-1.5" data-testid={`embedded-llm-tuning-${knob}`}>
      <NumberField
        label={label}
        value={value}
        min={min}
        max={max}
        step={step}
        // Every knob but `host_reserve_gib` is an `int` on the wire
        // (backend/frontend_api_embedded_dto.go); a decimal entry would survive
        // the local range check and come back as a raw Go unmarshal error.
        integer={knob !== 'host_reserve_gib'}
        disabled={disabled}
        onChange={(v) => onSet({ [knob]: v } as EmbeddedLLMTuningPatch)}
      />
      {auto ? (
        <span className="pb-1.5 text-xs text-muted-foreground">Auto</span>
      ) : (
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-2 text-xs text-muted-foreground"
          disabled={disabled}
          aria-label={`${label} back to auto`}
          title={`${label} back to auto`}
          onClick={() => onSet({ reset: [knob] })}
        >
          Auto
        </Button>
      )}
    </div>
  )
}
