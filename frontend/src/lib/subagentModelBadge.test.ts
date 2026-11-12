import { describe, it, expect } from 'vitest'
import { subagentModelDiffers } from './subagentModelBadge'

describe('subagentModelDiffers', () => {
  it('treats a composite pin as the same model as a bare session model', () => {
    // The pin's provider prefix is an implementation detail of the delegate
    // launcher — bare-name equality means "same model", badge hidden.
    expect(subagentModelDiffers('openrouter/qwen3-27b', 'qwen3-27b')).toBe(false)
  })

  it('treats a bare pin as the same model as a composite session model', () => {
    expect(subagentModelDiffers('qwen3-27b', 'openrouter/qwen3-27b')).toBe(false)
  })

  it('splits the composite at the FIRST "/" (inner slashes survive normalization)', () => {
    // `ollama/org/path/model` is provider `ollama` + bare `org/path/model`;
    // comparing bare names keeps the model part's inner slashes intact.
    expect(subagentModelDiffers('ollama/org/path/model', 'llama.cpp/org/path/model')).toBe(false)
    // Different bare parts are still told apart despite matching prefixes.
    expect(subagentModelDiffers('ollama/org/path/model', 'ollama/org/other/model')).toBe(true)
  })

  it('reports a difference for identical bare names only when they are not equal', () => {
    expect(subagentModelDiffers('qwen3-27b', 'qwen3-27b')).toBe(false)
    expect(subagentModelDiffers('qwen3-27b', 'bonsai-2-27b')).toBe(true)
  })

  it('reports a difference when the bare names differ despite matching prefixes', () => {
    expect(subagentModelDiffers('openrouter/qwen3-27b', 'openrouter/bonsai-2-27b')).toBe(true)
  })

  it('treats an empty session model as different (badge visible)', () => {
    // Unknown conductor model — the badge stays visible until the session
    // model is known.
    expect(subagentModelDiffers('qwen3-27b', '')).toBe(true)
    expect(subagentModelDiffers('qwen3-27b', undefined)).toBe(true)
    expect(subagentModelDiffers('qwen3-27b', null)).toBe(true)
  })

  it('treats an empty pin as no badge (nothing to compare)', () => {
    expect(subagentModelDiffers('', 'qwen3-27b')).toBe(false)
    expect(subagentModelDiffers(undefined, undefined)).toBe(false)
    expect(subagentModelDiffers(null, 'qwen3-27b')).toBe(false)
  })
})
