// Shared vision-capability helpers for the attachment entry points
// (useStageAttachments: picker/drop, usePasteHandler: clipboard paste).
//
// Centralizes two things that must be identical across every entry point:
//   1. The sentinel value the backend returns in PasteResult.rejected when a
//      raw clipboard image was declined because the active model lacks vision.
//   2. The rejection banner text — copy lives here so it has a single
//      has a single source of truth rather than being duplicated per hook.

import { bareModel, findModelInfo } from '@/lib/modelId'
import type { ModelInfo } from '@/types/models'

/** Sentinel value the backend returns in `PasteResult.rejected` when a raw
 *  clipboard image could not be staged because the active model lacks vision
 *  capability. It is NOT a human-readable message — the frontend maps it to a
 *  localized banner via {@link visionRejectionMessage}. Mirrors the backend
 *  constant `pasteImageVisionRejected`. */
export const VISION_UNSUPPORTED = 'vision_unsupported'

/** Build the vision-rejection banner: explains that the effective
 *  model can't consume images and prompts the user to pick a vision-capable
 *  model. Shared by useStageAttachments (drop/picker) and usePasteHandler
 *  (paste) so the wording is consistent and lives in exactly one place.
 *
 *  @param effectiveModel the composite or bare selector resolved for the
 *    active message (selectedModel ?? defaultModel).
 *  @param modelInfo the resolved ModelInfo (may be undefined when the model
 *    can't be found); its bare name is shown when available. */
export function visionRejectionMessage(
  effectiveModel: string,
  modelInfo: ModelInfo | undefined,
): string {
  const modelName = modelInfo ? bareModel(modelInfo.name) : effectiveModel || 'the current model'
  return `Model ${modelName} does not support images. Choose a multimodal (vision) model.`
}

/** Resolve the effective model selector (selectedModel override or the global
 *  default) — the shared pre-step used by both entry points. */
export function resolveEffectiveModel(
  selectedModel: string | null,
  defaultModel: string,
): string {
  return selectedModel ?? defaultModel
}

/** Convenience: resolve the effective model AND its ModelInfo in one call. */
export function resolveModelContext(
  models: ModelInfo[],
  selectedModel: string | null,
  defaultModel: string,
): { effectiveModel: string; modelInfo: ModelInfo | undefined } {
  const effectiveModel = resolveEffectiveModel(selectedModel, defaultModel)
  return { effectiveModel, modelInfo: findModelInfo(models, effectiveModel) }
}
