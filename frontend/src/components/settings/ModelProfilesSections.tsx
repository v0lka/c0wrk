import type {
  ModelProfilesEssentialTools,
  ModelProfilesSystemPrompt,
  ModelProfilesSampling,
  ModelProfilesLoopHardening,
} from '@/types/models'
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from '@/components/ui/collapsible'
import { Combobox } from '@/components/ui/combobox'
import { ChevronDown } from 'lucide-react'
import { essentialToolPickerOptions } from '@/lib/modelProfilesTools'
import { Toggle, TagList } from './ModelProfilesControls'
import { NumberField } from './NumberField'
import { OptionalNumberField } from './ModelProfilesOptionalNumberField'

// `""` is a first-class stored value meaning "inherit the model default": the
// backend accepts it and the shipped gemma-* profiles leave it empty, so it is
// offered as an explicit option — otherwise those profiles would render a blank
// trigger with no way to read or re-select the stored value. "Medium" is the
// value the shipped "generic" profile pins.
const REASONING_EFFORTS: { value: string; label: string }[] = [
  { value: '', label: 'Inherit (model default)' },
  { value: 'off', label: 'Off' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
]

/** Collapsible wrapper for a single profile variant. */
export function VariantSection({ title, open, onOpenChange, children }: {
  title: string
  open: boolean
  onOpenChange: (open: boolean) => void
  children: React.ReactNode
}) {
  return (
    <Collapsible open={open} onOpenChange={onOpenChange} className="rounded-lg border border-border bg-card/50">
      <CollapsibleTrigger className="flex w-full items-center justify-between px-4 py-3">
        <span className="text-sm font-semibold">{title}</span>
        <ChevronDown className={`h-4 w-4 text-muted-foreground transition-transform ${open ? 'rotate-180' : ''}`} />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="flex flex-col gap-4 px-4 pb-4">{children}</div>
      </CollapsibleContent>
    </Collapsible>
  )
}

interface SectionCommon {
  open: boolean
  onOpenChange: (open: boolean) => void
  /**
   * Read-only render (predefined profile view): every control inside the
   * section is disabled while the values stay visible. The collapse itself
   * stays interactive — reading a collapsed variant is still useful.
   */
  disabled?: boolean
}

export function EssentialToolsSection({ slice, patch, open, onOpenChange, disabled }: SectionCommon & {
  slice: ModelProfilesEssentialTools
  patch: (p: Partial<ModelProfilesEssentialTools>) => void
}) {
  // Read-only protected list from the backend (core/model_profiles.ProtectedToolNames()).
  // SelectTools always keeps them regardless of always_present, so they render
  // as locked (non-removable) chips. They are UNIONED into the displayed list
  // (a custom profile's always_present may not pin them) but STRIPPED from the
  // value patched back, so the stored always_present stays exactly the
  // operator's own pins — the union is display-only.
  const locked = new Set(slice.protected_tools)
  const displayValues = [...slice.always_present]
  for (const t of slice.protected_tools) {
    if (!displayValues.includes(t)) displayValues.push(t)
  }
  // Picker entries: workflow clusters (offered atomically) plus ungrouped
  // built-ins, minus everything already allowed — the display list (pins ∪
  // protected) and the protected set are both subtracted, and MCP tools are
  // never in builtin_tools. Cluster entries add the whole cluster at once and
  // carry a hover tooltip describing the workflow plus its member list.
  const options = essentialToolPickerOptions(
    slice.builtin_tools ?? [],
    slice.tool_groups ?? [],
    displayValues,
    slice.protected_tools ?? [],
  )
  return (
    <VariantSection title="Essential Tools" open={open} onOpenChange={onOpenChange}>
      <Toggle
        checked={slice.enabled}
        onChange={(enabled) => patch({ enabled })}
        disabled={disabled}
        label="Curate the tool subset"
        description="Restrict the model to a small, high-signal tool set instead of the full registry."
      />
      {slice.enabled && (
        <>
          <TagList
            label="Always-present tools"
            values={displayValues}
            onChange={(v) =>
              // Recover the operator's own pins: drop the unioned protected
              // tools, but keep a protected tool that was already pinned (a
              // locked chip can be neither added nor removed, so its presence
              // reflects the stored list).
              patch({
                always_present: v.filter((n) => !locked.has(n) || slice.always_present.includes(n)),
              })
            }
            options={options}
            lockedValues={locked}
            disabled={disabled}
          />
          <p className="text-xs text-muted-foreground">
            Locked tools are protected and always included. Pick a built-in tool or a workflow
            group from the list to add it — a group entry adds the whole cluster at once — and the
            assigned set is exactly this selection plus every connected MCP server's tools.
          </p>
          <Toggle
            checked={slice.compact_descriptions}
            onChange={(compact_descriptions) => patch({ compact_descriptions })}
            disabled={disabled}
            label="Compact tool descriptions"
            description="Replace builtin tool descriptions with one-line variants (≤220 chars) to save context."
          />
        </>
      )}
    </VariantSection>
  )
}

export function SystemPromptSection({ slice, patch, open, onOpenChange, disabled }: SectionCommon & {
  slice: ModelProfilesSystemPrompt
  patch: (p: Partial<ModelProfilesSystemPrompt>) => void
}) {
  // Few-shot and reasoning-scaffold are tailored to the compact lite directive,
  // so they only take effect when Lite is on. Hide them when Lite is off so the
  // UI reflects what the backend actually honors.
  return (
    <VariantSection title="System Prompt" open={open} onOpenChange={onOpenChange}>
      <Toggle checked={slice.lite} onChange={(lite) => patch({ lite })} disabled={disabled} label="Lite prompt" description="Swap the verbose orchestrator directive for a compact one." />
      {slice.lite && (
        <>
          <Toggle checked={slice.few_shot} onChange={(few_shot) => patch({ few_shot })} disabled={disabled} label="Few-shot examples" description="Append worked ReAct examples (requires Lite)." />
          <Toggle checked={slice.reasoning_scaffold} onChange={(reasoning_scaffold) => patch({ reasoning_scaffold })} disabled={disabled} label="Reasoning scaffold" description="Append a structured thought template (requires Lite)." />
        </>
      )}
    </VariantSection>
  )
}

export function SamplingSection({ slice, patch, open, onOpenChange, disabled }: SectionCommon & {
  slice: ModelProfilesSampling
  patch: (p: Partial<ModelProfilesSampling>) => void
}) {
  return (
    <VariantSection title="Sampling" open={open} onOpenChange={onOpenChange}>
      <Toggle
        checked={slice.enabled}
        onChange={(enabled) => patch({ enabled })}
        disabled={disabled}
        label="Override sampling"
        description="Apply small-model-friendly sampling parameters."
      />
      {slice.enabled && (
        <>
          <p className="text-xs text-muted-foreground">
            Empty fields inherit the vendor preset for the selected model —
            including "Inherit (model default)" for reasoning effort, which
            keeps the vendor's own level (e.g. qwen xhigh).
          </p>
          <div className="grid grid-cols-2 gap-3">
            <OptionalNumberField
              label="Temperature"
              value={slice.temperature}
              onChange={(temperature) => patch({ temperature })}
              min={0.01}
              step={0.1}
              placeholder="vendor default"
              disabled={disabled}
            />
            <OptionalNumberField
              label="Top P"
              value={slice.top_p}
              onChange={(top_p) => patch({ top_p })}
              min={0.01}
              max={1}
              step={0.05}
              placeholder="vendor default"
              disabled={disabled}
            />
            <OptionalNumberField
              label="Top K"
              value={slice.top_k}
              onChange={(top_k) => patch({ top_k })}
              min={1}
              placeholder="vendor default"
              disabled={disabled}
            />
            <OptionalNumberField
              label="Repetition penalty"
              value={slice.repetition_penalty}
              onChange={(repetition_penalty) => patch({ repetition_penalty })}
              min={1}
              max={2}
              step={0.05}
              placeholder="vendor default"
              disabled={disabled}
            />
            <OptionalNumberField
              label="Presence penalty"
              value={slice.presence_penalty}
              onChange={(presence_penalty) => patch({ presence_penalty })}
              min={0.01}
              max={2}
              step={0.05}
              placeholder="vendor default"
              disabled={disabled}
            />
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-xs text-muted-foreground">Reasoning effort</label>
            <Combobox
              ariaLabel="Reasoning effort"
              value={slice.reasoning_effort}
              onChange={(reasoning_effort) => patch({ reasoning_effort })}
              className="min-w-[180px]"
              options={REASONING_EFFORTS}
              disabled={disabled}
            />
          </div>
        </>
      )}
    </VariantSection>
  )
}

export function LoopHardeningSection({ slice, patch, open, onOpenChange, disabled }: SectionCommon & {
  slice: ModelProfilesLoopHardening
  patch: (p: Partial<ModelProfilesLoopHardening>) => void
}) {
  return (
    <VariantSection title="Loop Hardening" open={open} onOpenChange={onOpenChange}>
      <Toggle
        checked={slice.enabled}
        onChange={(enabled) => patch({ enabled })}
        disabled={disabled}
        label="Tighten circuit breakers"
        description="Lower the nudge/abort thresholds so flaky loops fail fast."
      />
      {slice.enabled && (
        <div className="grid grid-cols-2 gap-3">
          {/* These fields render only while the variant is enabled, the exact
              state in which the backend requires every threshold >= 1. */}
          <NumberField label="Repeat nudge" value={slice.repeat_nudge_threshold} onChange={(v) => patch({ repeat_nudge_threshold: v })} min={1} integer disabled={disabled} />
          <NumberField label="Parse-error abort" value={slice.parse_error_abort_threshold} onChange={(v) => patch({ parse_error_abort_threshold: v })} min={1} integer disabled={disabled} />
          <NumberField label="Fruitless nudge" value={slice.fruitless_nudge_threshold} onChange={(v) => patch({ fruitless_nudge_threshold: v })} min={1} integer disabled={disabled} />
          <NumberField label="Fruitless abort" value={slice.fruitless_abort_threshold} onChange={(v) => patch({ fruitless_abort_threshold: v })} min={1} integer disabled={disabled} />
          <NumberField label="Same-tool repeat nudge" value={slice.same_tool_repeat_nudge_threshold} onChange={(v) => patch({ same_tool_repeat_nudge_threshold: v })} min={1} integer disabled={disabled} />
        </div>
      )}
    </VariantSection>
  )
}
