/** Fixed (non-OpenAI-compatible) provider keys. */
export const FIXED_PROVIDERS = ['anthropic', 'chatgpt'] as const
export type FixedProviderKey = (typeof FIXED_PROVIDERS)[number]

/** Canonical list of LLM provider keys shared across settings, comboboxes, and save logic. */
export const PROVIDERS = FIXED_PROVIDERS
export type ProviderKey = FixedProviderKey

export const PROVIDER_LABELS: Record<FixedProviderKey, string> = {
  anthropic: 'Anthropic',
  chatgpt: 'ChatGPT',
}

/** Returns true when the given provider name refers to an OpenAI-compatible provider,
 *  i.e. a provider whose name is not in {@link FIXED_PROVIDERS}. */
export function isOpenAICompatibleProvider(name: string): boolean {
  return !(FIXED_PROVIDERS as readonly string[]).includes(name)
}

/** Providers that require a base_url in their config form.
 *  Any OpenAI-compatible provider requires a base URL. */
export const PROVIDERS_WITH_BASE_URL = {
  has(name: string): boolean {
    return isOpenAICompatibleProvider(name)
  },
}
