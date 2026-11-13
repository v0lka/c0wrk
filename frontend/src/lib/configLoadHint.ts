import type { ConfigLLMResponse } from '@/types/models'

/**
 * Parsed hint for the mandatory-composite `default_model` load error
 * (ADR-122): the config file was rejected because `default_model` carried a
 * bare model name, and the user must rewrite it as a full
 * "provider/model" selector by hand.
 */
export interface CompositeDefaultModelHint {
  /** Bare model name carried by the offending config value. */
  bareModel: string
  /**
   * Concrete "provider/bare" suggestions resolvable from the effective
   * config's provider model lists (the fail-soft fallback config still
   * carries the provider sections). Empty when no configured provider
   * serves the bare model — the UI then shows the "<provider>/bare"
   * pattern instead of a ready-to-paste line.
   */
  suggestions: string[]
}

/**
 * Matches the backend's mandatory-composite load errors for default_model
 * (backend/config ResolveDefaultModelProvider, wrapped by config.Load's
 * "config validation failed:" prefix). Two shapes reach the banner:
 * - bare-name rejection (ParseCompositeModelID finds no "/"):
 *   `default_model "claude-3-haiku" must be a full "provider/model" identifier — ...`
 * - empty-provider half-composite (the first-"/" split succeeds, so the id
 *   falls through to the provider scan): `default_model "/model" not found
 *   in provider "" enabled models`. A not-found error naming a REAL
 *   provider does not match — that id is already a full composite and the
 *   fix is the model name, not a provider prefix.
 */
const COMPOSITE_DEFAULT_MODEL_ERROR = /default_model\s+"([^"]+)"\s+must be a full\b/
const EMPTY_PROVIDER_DEFAULT_MODEL_ERROR = /default_model\s+"([^"]+)"\s+not found in provider ""/

/**
 * Detects the mandatory-composite default_model load error among the
 * config_errors strings and derives the corrected composite form for the
 * Settings banner. Pure — no RPC, no side effects. Returns null when no
 * error matches, so callers render the hint only for this specific
 * failure and leave every other load error untouched.
 */
export function compositeDefaultModelHint(
  errors: readonly string[],
  llm?: ConfigLLMResponse | null
): CompositeDefaultModelHint | null {
  if (!errors || errors.length === 0) {
    return null
  }
  for (const err of errors) {
    const match = COMPOSITE_DEFAULT_MODEL_ERROR.exec(err) ?? EMPTY_PROVIDER_DEFAULT_MODEL_ERROR.exec(err)
    if (!match) {
      continue
    }
    const raw = match[1]
    if (raw === undefined) {
      continue
    }
    // The bare-name rejection captures the bare id verbatim (it carries no
    // "/"); the empty-provider rejection captures the half-composite —
    // strip its provider segment with the backend's first-"/" split.
    const slash = raw.indexOf('/')
    const bareModel = slash === -1 ? raw : raw.slice(slash + 1)
    if (bareModel === '') {
      continue
    }
    const suggestions: string[] = []
    if (llm) {
      const candidates: Array<{ name: string; models?: string[] }> = [
        { name: 'anthropic', models: llm.anthropic?.models },
        { name: 'chatgpt', models: llm.chatgpt?.models },
        { name: 'kimi', models: llm.kimi?.models },
        ...Object.entries(llm.openai_compatible ?? {}).map(([name, p]) => ({
          name,
          models: p?.models,
        })),
        ...Object.entries(llm.anthropic_compatible ?? {}).map(([name, p]) => ({
          name,
          models: p?.models,
        })),
      ]
      for (const candidate of candidates) {
        if (candidate.models?.includes(bareModel)) {
          suggestions.push(`${candidate.name}/${bareModel}`)
        }
      }
    }
    return { bareModel, suggestions }
  }
  return null
}
