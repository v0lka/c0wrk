import type { ModelInfo } from '@/types/models'

/**
 * Composite model identifier helpers.
 *
 * The backend (see `llm.CompositeModelID` / `config.CompositeModelID` and
 * `llm.BareModel`) uses a "provider/name" string as the canonical selector
 * when the same bare model name can be exposed by more than one provider.
 * The bare model name is what is displayed to the user; the composite value
 * is what is sent to the backend and persisted in `inputModeStore.selectedModel`.
 *
 * These helpers mirror the backend conventions so the frontend can build,
 * decompose, and resolve composite identifiers consistently. They are shared
 * by {@link ModelCombobox} and {@link ReasoningCombobox} to keep a single
 * source of truth.
 */

/** Build the composite selector value from a provider config key + bare model
 *  name. Mirrors the backend `llm.CompositeModelID` / `config.CompositeModelID`. */
export function compositeModelId(provider: string, model: string): string {
  return `${provider}/${model}`
}

/** Return the bare model name portion of a composite id (everything after the
 *  first "/"). A value without "/" is returned unchanged. Mirrors the backend
 *  `llm.BareModel`. */
export function bareModel(id: string): string {
  const idx = id.indexOf('/')
  return idx >= 0 ? id.slice(idx + 1) : id
}

/** Return true when `id` is a composite "provider/name" selector. */
export function isCompositeModelId(id: string): boolean {
  return id.indexOf('/') >= 0
}

/**
 * Find the {@link ModelInfo} for an effective model selector.
 *
 * The selector may be:
 *  - a composite id `"provider/name"` — resolved to the exact provider + bare
 *    name, so two providers exposing the same bare name are distinguished; or
 *  - a bare model name — resolved to the first matching model across providers
 *    (mirrors the backend's bare-name resolution, where the first match wins).
 *
 * Returns `undefined` when no enabled model matches (e.g. the selector is
 * empty, stale, or refers to a disabled model).
 */
export function findModelInfo(
  models: ModelInfo[],
  selector: string,
): ModelInfo | undefined {
  if (!selector) return undefined
  if (isCompositeModelId(selector)) {
    return models.find((m) => compositeModelId(m.provider, m.name) === selector)
  }
  return models.find((m) => m.name === selector)
}
