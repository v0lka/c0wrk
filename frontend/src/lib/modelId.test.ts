import { describe, it, expect } from 'vitest'
import { compositeModelId, bareModel, isCompositeModelId, decomposeCompositeModelId, findModelInfo } from './modelId'
import type { ModelInfo } from '@/types/models'

const reasoning = { options: ['low', 'medium', 'high'], default: 'high' }

const models: ModelInfo[] = [
  { name: 'claude-sonnet-4', provider: 'anthropic', family: 'anthropic', vision: true, reasoning },
  { name: 'gpt-4o', provider: 'chatgpt', family: 'openai_flagship', vision: true, reasoning: null },
  // Same bare name exposed by two providers — composite id disambiguates.
  { name: 'gpt-4o', provider: 'lmstudio', family: 'openai_flagship', vision: false, reasoning },
  { name: 'qwen-max', provider: 'openrouter', family: 'qwen', vision: false },
]

describe('compositeModelId', () => {
  it('joins provider and bare model with "/"', () => {
    expect(compositeModelId('anthropic', 'claude-sonnet-4')).toBe('anthropic/claude-sonnet-4')
  })
})

describe('bareModel', () => {
  it('returns the part after the first "/" for a composite id', () => {
    expect(bareModel('anthropic/claude-sonnet-4')).toBe('claude-sonnet-4')
  })

  it('returns the value unchanged when it has no "/"', () => {
    expect(bareModel('claude-sonnet-4')).toBe('claude-sonnet-4')
  })

  it('returns everything after the first "/" when the model name contains "/"', () => {
    // Defensive: a bare name is not expected to contain "/", but bareModel
    // mirrors the backend which slices after the first occurrence.
    expect(bareModel('org/path/model')).toBe('path/model')
  })
})

describe('isCompositeModelId', () => {
  it('is true for a "provider/name" string', () => {
    expect(isCompositeModelId('anthropic/claude-sonnet-4')).toBe(true)
  })

  it('is false for a bare model name', () => {
    expect(isCompositeModelId('claude-sonnet-4')).toBe(false)
  })

  it('is false for an empty string', () => {
    expect(isCompositeModelId('')).toBe(false)
  })
})

describe('decomposeCompositeModelId', () => {
  it('splits a "provider/name" id at the first "/"', () => {
    expect(decomposeCompositeModelId('anthropic/claude-sonnet-4')).toEqual({
      provider: 'anthropic',
      model: 'claude-sonnet-4',
    })
  })

  it('treats everything after the first "/" as the model name', () => {
    // A bare name may itself contain "/" (e.g. local/Ollama-style ids like
    // "ollama/org/path/model"); the first "/" is always the separator.
    expect(decomposeCompositeModelId('ollama/org/path/model')).toEqual({
      provider: 'ollama',
      model: 'org/path/model',
    })
  })

  it('returns null for a bare model name without "/"', () => {
    expect(decomposeCompositeModelId('claude-sonnet-4')).toBeNull()
  })

  it('returns null for an empty string', () => {
    expect(decomposeCompositeModelId('')).toBeNull()
  })

  it('is the inverse of compositeModelId for single-segment names', () => {
    const id = compositeModelId('chatgpt', 'gpt-4o')
    const parts = decomposeCompositeModelId(id)
    expect(parts?.provider).toBe('chatgpt')
    expect(parts?.model).toBe('gpt-4o')
  })
})

describe('findModelInfo', () => {
  it('returns undefined for an empty selector', () => {
    expect(findModelInfo(models, '')).toBeUndefined()
  })

  it('resolves a composite id to the exact provider + bare name', () => {
    expect(findModelInfo(models, 'anthropic/claude-sonnet-4')?.name).toBe('claude-sonnet-4')
  })

  it('distinguishes two providers exposing the same bare name', () => {
    // chatgpt provider has reasoning: null
    expect(findModelInfo(models, 'chatgpt/gpt-4o')?.reasoning).toBeNull()
    // lmstudio provider has reasoning metadata
    expect(findModelInfo(models, 'lmstudio/gpt-4o')?.reasoning).toEqual(reasoning)
  })

  it('resolves a bare name to the first matching provider', () => {
    // "gpt-4o" matches chatgpt first (deterministic provider order in `models`)
    expect(findModelInfo(models, 'gpt-4o')?.provider).toBe('chatgpt')
    expect(findModelInfo(models, 'claude-sonnet-4')?.provider).toBe('anthropic')
  })

  it('returns undefined when no enabled model matches', () => {
    expect(findModelInfo(models, 'anthropic/unknown-model')).toBeUndefined()
    expect(findModelInfo(models, 'unknown-model')).toBeUndefined()
  })

  it('returns undefined for a composite id whose provider is unknown', () => {
    expect(findModelInfo(models, 'nope/claude-sonnet-4')).toBeUndefined()
  })
})
