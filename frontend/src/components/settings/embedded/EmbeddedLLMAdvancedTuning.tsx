// The collapsed ADVANCED memory-plan controls of the embedded local model,
// wrapped in the Model Profiles `VariantSection` (the shared collapsible
// shape). A fully controlled leaf with ZERO local state — the open/closed flag
// and every value live in the parent (useEmbeddedLLMTuning). Each numeric knob
// carries a per-knob Auto affordance (./TuningNumber) and each boolean knob is a
// TRI-STATE Auto/On/Off control (./TuningTriState), because "unset — the planner
// decides" is a different value from any explicit one and can resolve to OFF.
// The recorded plan's own outcome is quoted beside every unset knob, so Auto is
// never a guess.

import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { VariantSection } from '@/components/settings/ModelProfilesSections'
import { TUNING_PACKINGS } from '@/api/embeddedTuning'
import { TRI_OFF, TRI_ON, TUNING_AUTO, TUNING_RANGES } from '@/lib/embeddedTuningDisplay'
import { TuningNumber } from './TuningNumber'
import { TuningTriState } from './TuningTriState'
import type { EmbeddedLLMAdvancedTuningProps } from '@/hooks/useEmbeddedLLMAdvancedTuning'

const PACKING_OPTIONS: readonly ComboboxOption[] = [
  { value: TUNING_AUTO, label: 'Auto (resolver)' },
  ...TUNING_PACKINGS.map((p) => ({ value: p, label: p })),
]

const FIT_OPTIONS: readonly ComboboxOption[] = [
  { value: TUNING_AUTO, label: 'Auto (offload rule)' },
  { value: TRI_ON, label: 'On' },
  { value: TRI_OFF, label: 'Off' },
]

const DEVICE_OFFLOAD_OPTIONS: readonly ComboboxOption[] = [
  { value: TUNING_AUTO, label: 'Auto (memory gate)' },
  { value: TRI_ON, label: 'On' },
  { value: TRI_OFF, label: 'Off' },
]

/** The clause quoting the recorded plan beside an unset knob, or null while no
 *  plan was recorded. */
function planNote(prefix: string, quoted: string | null): string | null {
  return quoted === null ? null : `${prefix} ${quoted}`
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
      <TuningTriState
        knob="fit"
        label="Fit to device memory"
        value={props.fitMode}
        options={FIT_OPTIONS}
        hint="Let the runtime size the layer count and the context (--fit); Off computes them from the profile instead. Auto leaves the choice to the offload exclusivity rule"
        planned={planNote('the recorded plan runs -fit', props.fitPlanned)}
        disabled={disabled}
        onSet={onSet}
      />
      <div className="grid grid-cols-2 gap-3">
        <TuningNumber
          label="Fit target (MiB)"
          knob="fit_target_mib"
          value={props.fitTargetMiB}
          auto={props.auto.fitTargetMiB}
          min={TUNING_RANGES.fit_target_mib.min}
          max={TUNING_RANGES.fit_target_mib.max}
          disabled={disabled}
          onSet={onSet}
        />
        <TuningNumber
          label="Fit floor (tokens)"
          knob="fit_min_context"
          value={props.fitMinContext}
          auto={props.auto.fitMinContext}
          min={TUNING_RANGES.fit_min_context.min}
          max={TUNING_RANGES.fit_min_context.max}
          disabled={disabled}
          onSet={onSet}
        />
      </div>
      <TuningTriState
        knob="kv_offload"
        label="KV cache on device"
        value={props.kvMode}
        options={DEVICE_OFFLOAD_OPTIONS}
        hint="Off keeps the KV cache in system RAM (-nkvo), trading device memory for host memory. Auto leaves the choice to the memory gate"
        planned={planNote('the recorded plan keeps it', props.kvPlanned)}
        disabled={disabled}
        onSet={onSet}
      />
      <TuningTriState
        knob="mmproj_offload"
        label="Vision projector on device"
        value={props.mmprojMode}
        options={DEVICE_OFFLOAD_OPTIONS}
        hint="Off moves the projector's worst-case reserve to system RAM (--no-mmproj-offload). Auto leaves the choice to the memory gate"
        planned={planNote('the recorded plan keeps it', props.mmprojPlanned)}
        disabled={disabled}
        onSet={onSet}
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
          min={TUNING_RANGES.parallel.min}
          max={TUNING_RANGES.parallel.max}
          disabled={disabled}
          onSet={onSet}
        />
        <TuningNumber
          label="Prompt cache (MiB)"
          knob="cache_ram_mib"
          value={props.cacheRamMiB}
          auto={props.auto.cacheRamMiB}
          min={TUNING_RANGES.cache_ram_mib.min}
          max={TUNING_RANGES.cache_ram_mib.max}
          disabled={disabled}
          onSet={onSet}
        />
      </div>
      <TuningNumber
        label="Host reserve (GiB)"
        knob="host_reserve_gib"
        value={props.hostReserveGiB}
        auto={props.auto.hostReserveGiB}
        min={TUNING_RANGES.host_reserve_gib.min}
        max={TUNING_RANGES.host_reserve_gib.max}
        step={0.5}
        disabled={disabled}
        onSet={onSet}
      />
    </VariantSection>
  )
}
