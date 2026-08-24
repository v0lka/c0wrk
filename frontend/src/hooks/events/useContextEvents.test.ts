import { describe, it, expect } from 'vitest'
import { handleContextFill, type ContextFillStore } from '@/hooks/events/useContextEvents'
import type { ContextFillData } from '@/types/events'
import type { TokenInfo } from '@/types/models'

interface Recorded {
  stepFill: Array<{ sessionId: string; stepId: string; fill: number }>
  sessionTokens: Array<Partial<TokenInfo>>
}

function makeStore(): ContextFillStore & { recorded: Recorded } {
  const recorded: Recorded = { stepFill: [], sessionTokens: [] }
  return {
    recorded,
    setStepContextFill: (sessionId, stepId, fill) => { recorded.stepFill.push({ sessionId, stepId, fill }) },
    setSessionTokens: (_sessionId, tokens) => { recorded.sessionTokens.push(tokens) },
  }
}

function makeData(overrides: Partial<ContextFillData> & { plan_step_id?: string }): ContextFillData {
  return {
    fill_percent: 42.5,
    used_tokens: 8500,
    max_tokens: 20000,
    status: 'ok',
    session_input_tokens: 100,
    session_output_tokens: 50,
    model: 'qwen3.6',
    family: 'openai_compatible',
    ...overrides,
  }
}

describe('handleContextFill', () => {
  it('session-root event merges fill_percent/used_tokens/max_tokens into session tokens', () => {
    // The SetDisplayContextWindowForModel re-broadcast arrives with no
    // plan_step_id once a lazy local-model probe lands — the status bar must
    // correct immediately, not wait for the next LLM call.
    const store = makeStore()
    handleContextFill(store, 'sess-1', makeData({}))
    expect(store.recorded.sessionTokens).toHaveLength(1)
    expect(store.recorded.sessionTokens[0]).toMatchObject({
      total_input_tokens: 100,
      total_output_tokens: 50,
      model: 'qwen3.6',
      family: 'openai_compatible',
      fill_percent: 42.5,
      used_tokens: 8500,
      max_tokens: 20000,
    })
    expect(store.recorded.stepFill).toHaveLength(0)
  })

  it('step-scoped event updates only the step fill and token totals', () => {
    // A subagent step's own fill must NOT clobber the conductor's
    // session-level fill the status bar renders.
    const store = makeStore()
    handleContextFill(store, 'sess-1', makeData({ plan_step_id: 'step-9' }))
    expect(store.recorded.stepFill).toEqual([{ sessionId: 'sess-1', stepId: 'step-9', fill: 42.5 }])
    expect(store.recorded.sessionTokens).toHaveLength(1)
    expect(store.recorded.sessionTokens[0]).toEqual({
      total_input_tokens: 100,
      total_output_tokens: 50,
      model: 'qwen3.6',
      family: 'openai_compatible',
    })
  })

  it('session-root event preserves previously-known fill when fields are absent', () => {
    // The type guard (isContextFillData) requires only fill_percent+status;
    // a payload missing used_tokens/max_tokens must not overwrite the last
    // known values with 0 (store merge semantics keep them when omitted).
    const store = makeStore()
    handleContextFill(store, 'sess-1', makeData({ used_tokens: undefined, max_tokens: undefined }))
    expect(store.recorded.sessionTokens[0]).not.toHaveProperty('used_tokens')
    expect(store.recorded.sessionTokens[0]).not.toHaveProperty('max_tokens')
    expect(store.recorded.sessionTokens[0]).toMatchObject({ fill_percent: 42.5 })
  })
})
