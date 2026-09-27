// Embedded-LLM install vocabulary shared by every surface that reports an
// install's progress: the Settings block's per-component rows
// (`components/settings/embedded/EmbeddedLLMProgress.tsx`) and the status-bar
// indicator (`components/layout/EmbeddedModelStatus.tsx`).
//
// The words live here — not in either component — so the two surfaces cannot
// drift apart: they describe the SAME `embedded_llm:install_progress` payloads
// (see specs/contracts/event-catalog.md and specs/domains/embedded-llm.md).
// A component or stage the backend adds and this map does not know yet is
// rendered as its raw id by the callers, never as a blank or a guess.

import type { EmbeddedLLMComponent } from '@/types/events'

/** Render order = the install's own order: the runtime phase first (runtime,
 *  then cudart on Windows CUDA), then the weights (model, mmproj). */
export const EMBEDDED_COMPONENT_ORDER: readonly EmbeddedLLMComponent[] = [
  'runtime',
  'cudart',
  'model',
  'mmproj',
]

/** Human-readable name of one downloaded artifact. */
export const EMBEDDED_COMPONENT_LABELS: Record<EmbeddedLLMComponent, string> = {
  runtime: 'Inference runtime',
  cudart: 'CUDA runtime',
  model: 'Model weights',
  mmproj: 'Vision projector',
}

/** Human-readable name of one artifact's stage. */
export const EMBEDDED_STAGE_LABELS: Record<string, string> = {
  downloading: 'Downloading',
  verifying: 'Verifying',
  extracting: 'Extracting',
  signing: 'Signing',
  done: 'Done',
}

/** The label of a component, falling back to its raw id for an artifact this
 *  frontend does not know yet. */
export function embeddedComponentLabel(component: string): string {
  return EMBEDDED_COMPONENT_LABELS[component as EmbeddedLLMComponent] ?? component
}

/** The label of a stage, falling back to its raw id for a stage this frontend
 *  does not know yet. */
export function embeddedStageLabel(stage: string): string {
  return EMBEDDED_STAGE_LABELS[stage] ?? stage
}
