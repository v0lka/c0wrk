// The three PRIMARY memory-plan controls of the embedded local model: the
// context mode, the KV-cache precision and the layer-offload shape. A fully
// controlled leaf with ZERO local state — every value and the commit flow live
// in the parent (useEmbeddedLLMTuning); see that hook for the pattern.
//
// A count-bearing mode ("Exact" context, "N layers" offload) is a DRAFT the
// parent holds: picking it shows the mode and its count field but persists
// nothing, because the only count available at that instant is a fallback
// (`-ngl 0` is an all-CPU launch shape). The write happens when the user
// commits a count — the hint line below says so out loud.

import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { NumberField } from '@/components/settings/NumberField'
import {
  CONTEXT_MODE_EXACT,
  KV_CACHE_TYPES,
  OFFLOAD_MODE_ALL,
  OFFLOAD_MODE_CPU,
  OFFLOAD_MODE_LAYERS,
} from '@/api/embeddedTuning'
import { TUNING_AUTO, TUNING_RANGES } from '@/lib/embeddedTuningDisplay'
import type { EmbeddedLLMTuningPrimaryProps } from '@/hooks/useEmbeddedLLMTuning'

const CONTEXT_OPTIONS: readonly ComboboxOption[] = [
  { value: TUNING_AUTO, label: 'Auto (RAM-tiered)' },
  { value: CONTEXT_MODE_EXACT, label: 'Exact' },
]

const KV_OPTIONS: readonly ComboboxOption[] = [
  { value: TUNING_AUTO, label: 'Auto (adaptive)' },
  ...KV_CACHE_TYPES.map((t) => ({ value: t, label: t })),
]

const OFFLOAD_OPTIONS: readonly ComboboxOption[] = [
  { value: TUNING_AUTO, label: 'Auto (fit decides)' },
  { value: OFFLOAD_MODE_ALL, label: 'All' },
  { value: OFFLOAD_MODE_CPU, label: 'CPU' },
  { value: OFFLOAD_MODE_LAYERS, label: 'N layers' },
]

export function EmbeddedLLMTuning(props: EmbeddedLLMTuningPrimaryProps) {
  const { onSet, disabled } = props
  const draftPending = props.contextDraft || props.offloadDraft
  return (
    <div className="flex flex-col gap-2" data-testid="embedded-llm-tuning">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <span className="text-sm font-medium text-foreground">Tuning</span>
        {props.reloadRequired && (
          <span className="text-xs text-warning" data-testid="embedded-llm-tuning-reload">
            Pending changes — Unload + Load applies them
          </span>
        )}
      </div>
      <div className="flex flex-wrap items-start gap-4">
        <div className="flex flex-col gap-1" data-testid="embedded-llm-tuning-context">
          <label className="text-xs text-muted-foreground">Context</label>
          <div className="flex items-center gap-2">
            <Combobox
              ariaLabel="Context mode"
              value={props.contextMode}
              options={CONTEXT_OPTIONS}
              className="h-8 w-40 text-xs"
              disabled={disabled}
              onChange={props.onContextModeChange}
            />
            {props.contextMode === CONTEXT_MODE_EXACT && (
              <NumberField
                label="Tokens"
                value={props.contextTokens}
                min={TUNING_RANGES.context_tokens.min}
                max={TUNING_RANGES.context_tokens.max}
                integer
                disabled={disabled}
                onChange={(tokens) => onSet({ context: { mode: CONTEXT_MODE_EXACT, tokens } })}
              />
            )}
          </div>
        </div>
        <div className="flex flex-col gap-1" data-testid="embedded-llm-tuning-kv">
          <label className="text-xs text-muted-foreground">KV cache</label>
          <Combobox
            ariaLabel="KV cache precision"
            value={props.kvCacheType}
            options={KV_OPTIONS}
            className="h-8 w-36 text-xs"
            disabled={disabled}
            onChange={(type) => onSet(type === TUNING_AUTO ? { reset: ['kv_cache_type'] } : { kv_cache_type: type })}
          />
        </div>
        <div className="flex flex-col gap-1" data-testid="embedded-llm-tuning-offload">
          <label className="text-xs text-muted-foreground">Layer offload</label>
          <div className="flex items-center gap-2">
            <Combobox
              ariaLabel="Layer offload"
              value={props.offloadMode}
              options={OFFLOAD_OPTIONS}
              className="h-8 w-40 text-xs"
              disabled={disabled}
              onChange={props.onOffloadModeChange}
            />
            {props.offloadMode === OFFLOAD_MODE_LAYERS && (
              <NumberField
                label="Layers"
                value={props.offloadLayers}
                min={TUNING_RANGES.offload_layers.min}
                max={TUNING_RANGES.offload_layers.max}
                integer
                disabled={disabled}
                onChange={(layers) => onSet({ offload: { mode: OFFLOAD_MODE_LAYERS, layers } })}
              />
            )}
          </div>
        </div>
      </div>
      {draftPending && (
        <p className="text-xs text-warning" data-testid="embedded-llm-tuning-draft">
          Enter a count to apply it — picking the mode alone saves nothing.
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        Auto leaves every launch flag to the memory planner; overrides apply to the next Load and never
        restart a resident model.
      </p>
    </div>
  )
}
