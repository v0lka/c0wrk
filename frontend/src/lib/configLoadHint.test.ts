import { describe, expect, it } from 'vitest'
import { compositeDefaultModelHint } from './configLoadHint'
import type { ConfigLLMResponse } from '@/types/models'

const COMPOSITE_ERROR =
  'config.Load: config validation failed: default_model "claude-3-haiku" must be a full "provider/model" identifier — bare model names are not accepted (the model name itself may contain "/": the id splits on the first "/" only)'

function makeLLM(partial: Partial<ConfigLLMResponse>): ConfigLLMResponse {
  return {
    default_model: '',
    anthropic: { api_key: '', models: [] },
    openai_compatible: {},
    anthropic_compatible: {},
    chatgpt: { api_key: '', models: [] },
    kimi: { api_key: '', models: [] },
    all_models: [],
    models_ready: false,
    auto_retry_max_seconds: 300,
    ...partial,
  } as ConfigLLMResponse
}

describe('compositeDefaultModelHint', () => {
  it('returns null for an empty error list', () => {
    expect(compositeDefaultModelHint([], null)).toBeNull()
  })

  it('returns null when no error matches the composite default_model shape', () => {
    expect(compositeDefaultModelHint(['some other section is broken'], null)).toBeNull()
  })

  it('extracts the bare model from the load error', () => {
    const hint = compositeDefaultModelHint([COMPOSITE_ERROR], null)
    expect(hint).not.toBeNull()
    expect(hint?.bareModel).toBe('claude-3-haiku')
  })

  it('offers no suggestions without an effective config', () => {
    expect(compositeDefaultModelHint([COMPOSITE_ERROR], null)?.suggestions).toEqual([])
  })

  it('resolves a concrete suggestion from the anthropic provider', () => {
    const hint = compositeDefaultModelHint(
      [COMPOSITE_ERROR],
      makeLLM({ anthropic: { api_key: '', models: ['claude-3-haiku'] } })
    )
    expect(hint?.suggestions).toEqual(['anthropic/claude-3-haiku'])
  })

  it('resolves suggestions from openai_compatible and anthropic_compatible entries', () => {
    const hint = compositeDefaultModelHint(
      [COMPOSITE_ERROR],
      makeLLM({
        openai_compatible: {
          local: { api_key: '', models: ['claude-3-haiku'] },
        },
        anthropic_compatible: {
          proxy: { api_key: '', models: ['claude-3-haiku'] },
        },
      })
    )
    expect(hint?.suggestions).toEqual(['local/claude-3-haiku', 'proxy/claude-3-haiku'])
  })

  it('lists every serving provider, fixed providers first', () => {
    const hint = compositeDefaultModelHint(
      [COMPOSITE_ERROR],
      makeLLM({
        anthropic: { api_key: '', models: ['claude-3-haiku'] },
        kimi: { api_key: '', models: ['claude-3-haiku'] },
        openai_compatible: {
          proxy: { api_key: '', models: ['claude-3-haiku'] },
        },
      })
    )
    expect(hint?.suggestions).toEqual([
      'anthropic/claude-3-haiku',
      'kimi/claude-3-haiku',
      'proxy/claude-3-haiku',
    ])
  })

  it('skips providers that do not serve the bare model', () => {
    const hint = compositeDefaultModelHint(
      [COMPOSITE_ERROR],
      makeLLM({
        anthropic: { api_key: '', models: ['claude-sonnet-4'] },
        chatgpt: { api_key: '', models: ['gpt-4o'] },
      })
    )
    expect(hint?.suggestions).toEqual([])
  })

  it('returns the first matching error when several errors are present', () => {
    const hint = compositeDefaultModelHint(
      ['web_access bind 0.0.0.0 is not loopback', COMPOSITE_ERROR],
      makeLLM({ anthropic: { api_key: '', models: ['claude-3-haiku'] } })
    )
    expect(hint?.bareModel).toBe('claude-3-haiku')
    expect(hint?.suggestions).toEqual(['anthropic/claude-3-haiku'])
  })
})
