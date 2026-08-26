import { Layers, Shrink, SquareStack, type LucideIcon } from 'lucide-react'

/**
 * Strategy catalog for manual context compaction. Descriptions mirror the
 * documented domain→strategy mapping (specs/domains/memory/compaction.md):
 * each entry's tooltip explains what the strategy does and when to use it.
 * Exported separately from the component for fast-refresh compliance and
 * unit-testability.
 */
export interface CompactionStrategyOption {
  id: string
  name: string
  hint: string
  tooltip: string
  icon: LucideIcon
}

export const COMPACTION_STRATEGIES: CompactionStrategyOption[] = [
  {
    id: 'sliding_window',
    name: 'Sliding window',
    hint: 'Fast · no LLM call',
    tooltip: 'Keeps the first few and the most recent messages verbatim and drops the middle with a marker note. Best for CODE tasks — recent edits and exchanges stay exact. Instant, no model call, but early context is lost entirely.',
    icon: SquareStack,
  },
  {
    id: 'summarization',
    name: 'Summarization',
    hint: 'LLM call · slower',
    tooltip: 'Condenses older exchanges into LLM-written summaries while keeping the most recent messages verbatim. Best for RESEARCH tasks — synthesized findings survive, though details may be lost. Requires model calls, so it takes longer.',
    icon: Shrink,
  },
  {
    id: 'hierarchical',
    name: 'Hierarchical',
    hint: 'LLM call · balanced',
    tooltip: 'Splits history into zones: an aggressive summary of the distant past, moderate per-block summaries mid-way, and the recent zone kept verbatim. Best for LONG-RUNNING COMPLEX tasks needing balanced retention.',
    icon: Layers,
  },
]
