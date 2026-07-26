// @vitest-environment node
import { describe, it, expect } from 'vitest'
import { defaultModelIsValid, type ProviderConfig } from './useLLMConfig'

/**
 * Unit tests for the frontend default-model reconciliation logic.
 *
 * The settings dialog must block close while no default model is set. After a
 * user deletes a provider or disables a model, the local default may point at
 * a model that no longer exists. `defaultModelIsValid` decides whether to keep
 * or clear that local default — it mirrors the backend
 * `config.LLMConfig.ResolveDefaultModelProvider` invariant.
 *
 * The authoritative close guard is the backend (UpdateLLMConfig self-clears a
 * dangling default, verified in backend/frontend_api_config_test.go); these
 * tests lock in the frontend's immediate local-state reconciliation.
 */
describe('defaultModelIsValid', () => {
    const configs = (providers: Record<string, string[]>): Record<string, ProviderConfig> => {
        const out: Record<string, ProviderConfig> = {}
        for (const [name, models] of Object.entries(providers)) {
            out[name] = { api_key: '', base_url: '', models }
        }
        return out
    }

    it('returns false for an empty default', () => {
        expect(defaultModelIsValid('', configs({ anthropic: ['claude-3-opus'] }))).toBe(false)
    })

    it('validates a composite default against its owning provider', () => {
        expect(
            defaultModelIsValid('anthropic/claude-3-opus', configs({ anthropic: ['claude-3-opus'] })),
        ).toBe(true)
    })

    it('rejects a composite default whose provider was deleted', () => {
        // The provider that owned the default is gone → dangling.
        expect(
            defaultModelIsValid('lmstudio/gpt-4', configs({ anthropic: ['claude-3-opus'] })),
        ).toBe(false)
    })

    it('rejects a composite default whose backing model was disabled', () => {
        // Provider still exists but the model is no longer enabled.
        expect(
            defaultModelIsValid('anthropic/claude-3-opus', configs({ anthropic: ['claude-3-haiku'] })),
        ).toBe(false)
    })

    it('does not match a composite default against a different provider with the same bare name', () => {
        // Same bare name under another provider must NOT rescue a composite
        // default pinned to a removed provider — avoids false positives.
        expect(
            defaultModelIsValid('lmstudio/gpt-4', configs({ openai: ['gpt-4'] })),
        ).toBe(false)
    })

    it('resolves a bare default to the first enabling provider', () => {
        expect(defaultModelIsValid('gpt-4', configs({ openai: ['gpt-4'] }))).toBe(true)
    })

    it('rejects a bare default not enabled in any provider', () => {
        expect(defaultModelIsValid('ghost-model', configs({ openai: ['gpt-4'] }))).toBe(false)
    })

    it('returns false when no providers are configured', () => {
        expect(defaultModelIsValid('anthropic/claude-3-opus', {})).toBe(false)
    })
})
