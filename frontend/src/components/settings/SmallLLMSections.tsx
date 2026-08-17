import type {
  SmallLLMEssentialTools,
  SmallLLMSystemPrompt,
  SmallLLMSampling,
  SmallLLMLoopHardening,
} from '@/types/models'
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from '@/components/ui/collapsible'
import { ChevronDown } from 'lucide-react'
import { Toggle, NumberField, TagList } from './SmallLLMControls'
import { OptionalNumberField } from './SmallLLMOptionalNumberField'

const REASONING_EFFORTS: { value: string; label: string }[] = [
  { value: '', label: 'Inherit' },
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
}

export function EssentialToolsSection({ slice, patch, open, onOpenChange }: SectionCommon & {
  slice: SmallLLMEssentialTools
  patch: (p: Partial<SmallLLMEssentialTools>) => void
}) {
  // Read-only list from the backend (core/smallllm.ProtectedToolNames()).
  // The backend unions these into always_present and SelectTools always keeps
  // them, so they are rendered as locked (non-removable) chips.
  const locked = new Set(slice.protected_tools)
  return (
    <VariantSection title="Essential Tools" open={open} onOpenChange={onOpenChange}>
      <Toggle
        checked={slice.enabled}
        onChange={(enabled) => patch({ enabled })}
        label="Curate the tool subset"
        description="Restrict the model to a small, high-signal tool set instead of the full registry."
      />
      {slice.enabled && (
        <>
          <TagList
            label="Always-present tools"
            values={slice.always_present}
            onChange={(always_present) => patch({ always_present })}
            placeholder="tool name"
            lockedValues={locked}
          />
          <p className="text-xs text-muted-foreground">
            Locked tools are protected and always included. The budget below limits router-matched
            tools on top of the guaranteed set; guaranteed tools are never trimmed. Default is 16, 0
            disables the limit.
          </p>
          <NumberField
            label="Max tools"
            value={slice.max_tools}
            onChange={(max_tools) => patch({ max_tools })}
            min={0}
          />
          <Toggle
            checked={slice.compact_descriptions}
            onChange={(compact_descriptions) => patch({ compact_descriptions })}
            label="Compact tool descriptions"
            description="Replace builtin tool descriptions with one-line variants (≤220 chars) to save context."
          />
        </>
      )}
    </VariantSection>
  )
}

export function SystemPromptSection({ slice, patch, open, onOpenChange }: SectionCommon & {
  slice: SmallLLMSystemPrompt
  patch: (p: Partial<SmallLLMSystemPrompt>) => void
}) {
  // Few-shot and reasoning-scaffold are tailored to the compact lite directive,
  // so they only take effect when Lite is on. Hide them when Lite is off so the
  // UI reflects what the backend actually honors.
  return (
    <VariantSection title="System Prompt" open={open} onOpenChange={onOpenChange}>
      <Toggle checked={slice.lite} onChange={(lite) => patch({ lite })} label="Lite prompt" description="Swap the verbose orchestrator directive for a compact one." />
      {slice.lite && (
        <>
          <Toggle checked={slice.few_shot} onChange={(few_shot) => patch({ few_shot })} label="Few-shot examples" description="Append worked ReAct examples (requires Lite)." />
          <Toggle checked={slice.reasoning_scaffold} onChange={(reasoning_scaffold) => patch({ reasoning_scaffold })} label="Reasoning scaffold" description="Append a structured thought template (requires Lite)." />
        </>
      )}
    </VariantSection>
  )
}

export function SamplingSection({ slice, patch, open, onOpenChange }: SectionCommon & {
  slice: SmallLLMSampling
  patch: (p: Partial<SmallLLMSampling>) => void
}) {
  return (
    <VariantSection title="Sampling" open={open} onOpenChange={onOpenChange}>
      <Toggle
        checked={slice.enabled}
        onChange={(enabled) => patch({ enabled })}
        label="Override sampling"
        description="Apply small-model-friendly sampling parameters."
      />
      {slice.enabled && (
        <>
          <p className="text-xs text-muted-foreground">
            Empty fields inherit the vendor preset for the selected model.
          </p>
          <div className="grid grid-cols-2 gap-3">
            <OptionalNumberField
              label="Temperature"
              value={slice.temperature}
              onChange={(temperature) => patch({ temperature })}
              min={0.01}
              step={0.1}
              placeholder="vendor default"
            />
            <OptionalNumberField
              label="Top P"
              value={slice.top_p}
              onChange={(top_p) => patch({ top_p })}
              min={0.01}
              max={1}
              step={0.05}
              placeholder="vendor default"
            />
            <OptionalNumberField
              label="Top K"
              value={slice.top_k}
              onChange={(top_k) => patch({ top_k })}
              min={1}
              placeholder="vendor default"
            />
            <OptionalNumberField
              label="Repetition penalty"
              value={slice.repetition_penalty}
              onChange={(repetition_penalty) => patch({ repetition_penalty })}
              min={1}
              max={2}
              step={0.05}
              placeholder="vendor default"
            />
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-xs text-muted-foreground">Reasoning effort</label>
            <select
              value={slice.reasoning_effort}
              onChange={(e) => patch({ reasoning_effort: e.target.value })}
              className="c0-input h-9 px-3 rounded-md border border-input text-sm focus:outline-none min-w-[180px]"
            >
              {REASONING_EFFORTS.map((opt) => (
                <option key={opt.value} value={opt.value}>{opt.label}</option>
              ))}
            </select>
          </div>
        </>
      )}
    </VariantSection>
  )
}

export function LoopHardeningSection({ slice, patch, open, onOpenChange }: SectionCommon & {
  slice: SmallLLMLoopHardening
  patch: (p: Partial<SmallLLMLoopHardening>) => void
}) {
  return (
    <VariantSection title="Loop Hardening" open={open} onOpenChange={onOpenChange}>
      <Toggle
        checked={slice.enabled}
        onChange={(enabled) => patch({ enabled })}
        label="Tighten circuit breakers"
        description="Lower the nudge/abort thresholds so flaky loops fail fast."
      />
      {slice.enabled && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField label="Repeat nudge" value={slice.repeat_nudge_threshold} onChange={(v) => patch({ repeat_nudge_threshold: v })} min={0} />
          <NumberField label="Parse-error abort" value={slice.parse_error_abort_threshold} onChange={(v) => patch({ parse_error_abort_threshold: v })} min={0} />
          <NumberField label="Fruitless nudge" value={slice.fruitless_nudge_threshold} onChange={(v) => patch({ fruitless_nudge_threshold: v })} min={0} />
          <NumberField label="Fruitless abort" value={slice.fruitless_abort_threshold} onChange={(v) => patch({ fruitless_abort_threshold: v })} min={0} />
          <NumberField label="Same-tool repeat nudge" value={slice.same_tool_repeat_nudge_threshold} onChange={(v) => patch({ same_tool_repeat_nudge_threshold: v })} min={0} />
        </div>
      )}
    </VariantSection>
  )
}
