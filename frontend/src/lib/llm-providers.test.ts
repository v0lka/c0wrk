import { describe, expect, it } from 'vitest'

import { excludeEmbeddedModel, EMBEDDED_PROVIDER_NAME } from './llm-providers'

interface Entry {
  name: string
  provider: string
}

const remote: Entry = { name: 'claude-sonnet', provider: 'anthropic' }
const compatible: Entry = { name: 'glm-5.3', provider: 'lmstudio' }
const embedded: Entry = { name: 'Bonsai 2 27B', provider: EMBEDDED_PROVIDER_NAME }

describe('excludeEmbeddedModel (the embedded surfaces experimental gate)', () => {
  it('returns the input reference untouched while the gate is open', () => {
    const models = [embedded, remote]
    // Callers memoize the result; the open gate must not allocate.
    expect(excludeEmbeddedModel(models, true)).toBe(models)
  })

  it('keeps every non-embedded entry while the gate is open', () => {
    const models = [embedded, remote, compatible]
    expect(excludeEmbeddedModel(models, true)).toEqual([embedded, remote, compatible])
  })

  it('drops the backend-owned embedded entries while the gate is closed', () => {
    const models = [embedded, remote, compatible]
    expect(excludeEmbeddedModel(models, false)).toEqual([remote, compatible])
  })

  it('keeps a provider whose name merely contains the reserved key', () => {
    // The filter matches the provider key exactly — a user provider named
    // "embedded-proxy" (or any other superstring) stays visible.
    const models: Entry[] = [{ name: 'm', provider: `${EMBEDDED_PROVIDER_NAME}-proxy` }]
    expect(excludeEmbeddedModel(models, false)).toEqual(models)
  })

  it('drops every entry when only the embedded model is present', () => {
    const models = [embedded, { ...embedded, name: 'Bonsai 2 9B' }]
    expect(excludeEmbeddedModel(models, false)).toEqual([])
  })

  it('returns an empty list for empty input regardless of the gate', () => {
    expect(excludeEmbeddedModel<Entry>([], true)).toEqual([])
    expect(excludeEmbeddedModel<Entry>([], false)).toEqual([])
  })
})
