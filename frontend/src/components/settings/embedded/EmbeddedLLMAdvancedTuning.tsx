// The collapsed ADVANCED memory-plan controls of the embedded local model,
// wrapped in the Model Profiles `VariantSection` (the shared collapsible
// shape). A fully controlled leaf with ZERO local state — the open/closed flag
// and every value live in the parent (useEmbeddedLLMTuning). Each numeric knob
// carries a per-knob Auto affordance: a knob the operator never overrode shows
// "Auto" beside the effective default, and a set knob gets an Auto button that
// clears the override back to unset (`reset`), because "unset — the planner
// decides" is a different value from any explicit number.

import { Button } from '@/components/ui/button'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { VariantSection } from '@/components/settings/ModelProfilesSections'
import { NumberField, Toggle } from '@/components/settings/ModelProfilesControls'
import { TUNING_PACKINGS, type EmbeddedLLMTuningKnob, type EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'
import { TUNING_AUTO, type EmbeddedLLMAdvancedTuningProps } from '@/hooks/useEmbeddedLLMTuning'

const PACKING_OPTIONS: readonly ComboboxOption[] = [
  { value: TUNING_AUTO, label: 'Auto (resolver)' },
  ...TUNING_PACKINGS.map((p) => ({ value: p, label: p })),
]

/** The numeric knobs this section edits, spelled as the patch/YAML key. */
type NumericKnob = Extract<
  EmbeddedLLMTuningKnob,
  'fit_target_mib' | 'fit_min_context' | 'parallel' | 'cache_ram_mib' | 'host_reserve_gib'
>

function TuningNumber({
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
          onClick={() => onSet({ reset: [knob] })}
        >
          Auto
        </Button>
      )}
    </div>
  )
}

export function EmbeddedLLMAdvancedTuning(props: EmbeddedLLMAdvancedTuningProps) {
  const { onSet, disabled } = props
  return (
    <VariantSection title="Advanced tuning" open={props.open} onOpenChange={props.onOpenChange}>
      <p className="text-xs text-muted-foreground">
        Every knob left at Auto follows the memory planner; explicit values replace its choice verbatim
        on the next Load. Host-side knobs trade RAM for speed — see the plan notes in the install
        record.
      </p>
      <Toggle
        checked={props.fit}
        onChange={(fit) => onSet({ fit })}
        disabled={disabled}
        label="Fit to device memory"
        description="Let the runtime size the layer count and the context (--fit). Off computes them from the profile instead."
      />
      <div className="grid grid-cols-2 gap-3">
        <TuningNumber
          label="Fit target (MiB)"
          knob="fit_target_mib"
          value={props.fitTargetMiB}
          auto={props.auto.fitTargetMiB}
          min={0}
          disabled={disabled}
          onSet={onSet}
        />
        <TuningNumber
          label="Fit floor (tokens)"
          knob="fit_min_context"
          value={props.fitMinContext}
          auto={props.auto.fitMinContext}
          min={1}
          disabled={disabled}
          onSet={onSet}
        />
      </div>
      <Toggle
        checked={props.kvOffload}
        onChange={(kv_offload) => onSet({ kv_offload })}
        disabled={disabled}
        label="KV cache on device"
        description="Off keeps the KV cache in system RAM (-nkvo), trading device memory for host memory."
      />
      <Toggle
        checked={props.mmprojOffload}
        onChange={(mmproj_offload) => onSet({ mmproj_offload })}
        disabled={disabled}
        label="Vision projector on device"
        description="Off moves the projector's worst-case reserve to system RAM."
      />
      <div className="flex flex-col gap-1" data-testid="embedded-llm-tuning-packing">
        <label className="text-xs text-muted-foreground">Packing</label>
        <Combobox
          ariaLabel="Packing"
          value={props.packing}
          options={PACKING_OPTIONS}
          className="h-8 w-40 text-xs"
          disabled={disabled}
          onChange={(packing) => onSet(packing === TUNING_AUTO ? { reset: ['packing'] } : { packing })}
        />
      </div>
      <div className="grid grid-cols-2 gap-3">
        <TuningNumber
          label="Parallel slots"
          knob="parallel"
          value={props.parallel}
          auto={props.auto.parallel}
          min={1}
          disabled={disabled}
          onSet={onSet}
        />
        <TuningNumber
          label="Prompt cache (MiB)"
          knob="cache_ram_mib"
          value={props.cacheRamMiB}
          auto={props.auto.cacheRamMiB}
          min={0}
          disabled={disabled}
          onSet={onSet}
        />
      </div>
      <TuningNumber
        label="Host reserve (GiB)"
        knob="host_reserve_gib"
        value={props.hostReserveGiB}
        auto={props.auto.hostReserveGiB}
        min={0}
        step={0.5}
        disabled={disabled}
        onSet={onSet}
      />
    </VariantSection>
  )
}
