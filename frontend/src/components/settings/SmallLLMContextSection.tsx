import type { SmallLLMContext } from '@/types/models'
import { Toggle, NumberField } from './SmallLLMControls'
import { VariantSection } from './SmallLLMSections'

/**
 * Context-management variant: aggressive compaction, tool-output pruning and
 * output reserve tuning for small context windows. Validation (backend):
 * keep_last >= 2, block_size >= 2, 1 <= trigger_percent < 100,
 * tool_output_keep_last_n >= 1, output_token_reserve >= 1 when enabled.
 */
export function ContextSection({ slice, patch, open, onOpenChange }: {
  slice: SmallLLMContext
  patch: (p: Partial<SmallLLMContext>) => void
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const compaction = slice.compaction
  return (
    <VariantSection title="Context Management" open={open} onOpenChange={onOpenChange}>
      <Toggle
        checked={slice.enabled}
        onChange={(enabled) => patch({ enabled })}
        label="Aggressive context management"
        description="Tighten compaction, prune tool outputs and reserve headroom for the final answer."
      />
      {slice.enabled && (
        <>
          <p className="text-xs text-muted-foreground">Compaction</p>
          <div className="grid grid-cols-2 gap-3">
            <NumberField
              label="Keep last"
              value={compaction.keep_last}
              onChange={(keep_last) => patch({ compaction: { ...compaction, keep_last } })}
              min={2}
            />
            <NumberField
              label="Block size"
              value={compaction.block_size}
              onChange={(block_size) => patch({ compaction: { ...compaction, block_size } })}
              min={2}
            />
            <NumberField
              label="Trigger percent"
              value={compaction.trigger_percent}
              onChange={(trigger_percent) => patch({ compaction: { ...compaction, trigger_percent } })}
              min={1}
            />
            <NumberField
              label="Tool output keep N"
              value={slice.tool_output_keep_last_n}
              onChange={(tool_output_keep_last_n) => patch({ tool_output_keep_last_n })}
              min={1}
            />
            <NumberField
              label="Output token reserve"
              value={slice.output_token_reserve}
              onChange={(output_token_reserve) => patch({ output_token_reserve })}
              min={1}
            />
          </div>
          <p className="text-xs text-muted-foreground">
            Smaller keep/block values and a lower trigger percent compact sooner. Values apply as
            overrides on top of the executor defaults when the profile and this variant are enabled.
          </p>
        </>
      )}
    </VariantSection>
  )
}
