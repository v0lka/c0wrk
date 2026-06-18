/** Canonical list of LLM provider keys shared across settings, comboboxes, and save logic. */
export const PROVIDERS = ['anthropic', 'gemini', 'lmstudio', 'openai_compatible', 'chatgpt'] as const
export type ProviderKey = (typeof PROVIDERS)[number]

export const PROVIDER_LABELS: Record<ProviderKey, string> = {
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  lmstudio: 'LM Studio',
  openai_compatible: 'OpenAI Compatible',
  chatgpt: 'ChatGPT',
}

/** Providers that require a base_url in their config form. */
export const PROVIDERS_WITH_BASE_URL = new Set<ProviderKey>(['lmstudio', 'openai_compatible'])
