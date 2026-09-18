import { describe, it, expect, beforeEach } from 'vitest'
import { useProxyDraftStore, selectProxyActive, isProxyEffective } from './proxyDraftStore'

beforeEach(() => {
  useProxyDraftStore.setState({ active: null })
})

describe('proxyDraftStore', () => {
  it('starts in the unknown state', () => {
    expect(useProxyDraftStore.getState().active).toBeNull()
  })

  it('seedActive establishes a value when none is known', () => {
    useProxyDraftStore.getState().seedActive(true)
    expect(useProxyDraftStore.getState().active).toBe(true)
  })

  // The seed comes from the LLM tab's own getConfig, which may be staler than
  // a draft the General tab already wrote. Seeding over it would re-enable
  // pin controls the user just disabled by turning the proxy on.
  it('seedActive is a no-op once a value is known', () => {
    useProxyDraftStore.getState().seedActive(true)
    useProxyDraftStore.getState().seedActive(false)
    expect(useProxyDraftStore.getState().active).toBe(true)

    useProxyDraftStore.setState({ active: false })
    useProxyDraftStore.getState().seedActive(true)
    expect(useProxyDraftStore.getState().active).toBe(false)
  })

  it('setActive always applies, including after a seed', () => {
    useProxyDraftStore.getState().seedActive(false)
    useProxyDraftStore.getState().setActive(true)
    expect(useProxyDraftStore.getState().active).toBe(true)
    useProxyDraftStore.getState().setActive(false)
    expect(useProxyDraftStore.getState().active).toBe(false)
  })
})

describe('selectProxyActive', () => {
  it('treats the unknown state as no proxy', () => {
    expect(selectProxyActive({ active: null })).toBe(false)
  })

  it('maps known states directly', () => {
    expect(selectProxyActive({ active: true })).toBe(true)
    expect(selectProxyActive({ active: false })).toBe(false)
  })

  // React 19 compares store snapshots by reference, so a selector must never
  // build a new object/array. A scalar return is stable by construction.
  it('returns a primitive', () => {
    expect(typeof selectProxyActive({ active: true })).toBe('boolean')
  })
})

describe('isProxyEffective', () => {
  // Mirrors proxy.BuildTransport on the Go side: enabled AND a URL.
  it.each([
    { proxy: { enabled: true, url: 'http://proxy.lan:3128' }, want: true },
    { proxy: { enabled: true, url: '' }, want: false },
    { proxy: { enabled: false, url: 'http://proxy.lan:3128' }, want: false },
    { proxy: { enabled: false, url: '' }, want: false },
    { proxy: {}, want: false },
    { proxy: null, want: false },
    { proxy: undefined, want: false },
  ])('$proxy -> $want', ({ proxy, want }) => {
    expect(isProxyEffective(proxy)).toBe(want)
  })
})
