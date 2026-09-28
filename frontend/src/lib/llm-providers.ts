/** Fixed (non-compatible) provider keys. */
export const FIXED_PROVIDERS = ['anthropic', 'chatgpt'] as const
export type FixedProviderKey = (typeof FIXED_PROVIDERS)[number]

/**
 * The provider key of the installed embedded local model
 * (`backend/config.EmbeddedLLMProviderName`).
 *
 * The record under `llm.openai_compatible.embedded` is BACKEND-OWNED: it is
 * generated from the authoritative `embedded_llm` state on every config load
 * and save (`SyncEmbeddedProvider`), so the settings dialog must display the
 * model but never offer it for editing — any edit is overwritten on the next
 * save. It is therefore excluded from the compatible-provider accordions and
 * reserved against user-created provider names (a custom provider named
 * "embedded" would be silently deleted by that sync whenever the local model
 * is not installed).
 *
 * See specs/domains/embedded-llm.md and ADR-066.
 */
export const EMBEDDED_PROVIDER_NAME = 'embedded'

/** Canonical list of fixed LLM provider keys. */
export const PROVIDERS = FIXED_PROVIDERS
export type ProviderKey = FixedProviderKey

export const PROVIDER_LABELS: Record<FixedProviderKey, string> = {
  anthropic: 'Anthropic',
  chatgpt: 'ChatGPT',
}

/**
 * Transport type for a compatible provider — determines which backend map
 * (openai_compatible vs anthropic_compatible) the provider is saved under.
 *
 * - `'openai'`    → OpenAI Chat Completions API transport
 * - `'anthropic'` → Anthropic Messages API transport
 *
 * Fixed providers (anthropic, chatgpt) do not carry a transport type; only
 * compatible (named, custom-endpoint) providers do.
 */
export type CompatibleType = 'openai' | 'anthropic'

/** Returns true when the given provider name refers to a compatible provider,
 *  i.e. a provider whose name is not in {@link FIXED_PROVIDERS}. */
export function isCompatibleProvider(name: string): boolean {
  return !(FIXED_PROVIDERS as readonly string[]).includes(name)
}

/**
 * Returns true when the provider record is owned by the backend rather than by
 * the settings dialog's draft: it is generated from authoritative app state and
 * re-injected on every save, so the draft must neither render an editor for it
 * nor send it back.
 */
export function isBackendOwnedProvider(name: string): boolean {
  return name === EMBEDDED_PROVIDER_NAME
}

/**
 * The frontend availability gate for the embedded local model's UI surfaces
 * (specs/domains/embedded-llm.md): while the experimental switch
 * (`experimental.enabled`, read reactively by the call sites from
 * `useExperimentalStore`) is off, the backend-owned `embedded` provider's
 * entries are dropped from the model lists BOTH pickers render — the
 * Settings → LLM default-model picker and the chat toolbar's ModelCombobox —
 * and the Settings block itself is not mounted (its own `experimentalEnabled`
 * guard). The backend RPCs stay ungated: this is deliberately a
 * frontend-only gate, so an already-generated provider record keeps working
 * underneath and the surfaces reappear the moment the switch is flipped on.
 *
 * Returns the input reference untouched while the gate is open (callers
 * memoize the result); a filtered copy otherwise.
 */
export function excludeEmbeddedModel<T extends { provider: string }>(
  models: readonly T[],
  experimentalEnabled: boolean,
): readonly T[] {
  if (experimentalEnabled) return models
  return models.filter((m) => m.provider !== EMBEDDED_PROVIDER_NAME)
}

/** Backwards-compatible alias: any compatible provider. */
export const isOpenAICompatibleProvider = isCompatibleProvider

/** Providers that require a base_url in their config form.
 *  Any compatible provider requires a base URL. */
export const PROVIDERS_WITH_BASE_URL = {
  has(name: string): boolean {
    return isCompatibleProvider(name)
  },
}
