/** Fixed (non-compatible) provider keys. */
export const FIXED_PROVIDERS = ['anthropic', 'chatgpt'] as const
export type FixedProviderKey = (typeof FIXED_PROVIDERS)[number]

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

/** Backwards-compatible alias: any compatible provider. */
export const isOpenAICompatibleProvider = isCompatibleProvider

/** Providers that require a base_url in their config form.
 *  Any compatible provider requires a base URL. */
export const PROVIDERS_WITH_BASE_URL = {
  has(name: string): boolean {
    return isCompatibleProvider(name)
  },
}
