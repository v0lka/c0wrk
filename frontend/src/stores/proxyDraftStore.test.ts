import { describe, it, expect, beforeEach } from 'vitest'
import {
  useProxyDraftStore,
  selectProxyActive,
  isProxyEffective,
  isBypassed,
  pinGatedByProxy,
} from './proxyDraftStore'

beforeEach(() => {
  useProxyDraftStore.setState({ active: null, bypassList: [] })
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
    expect(selectProxyActive({ active: null, bypassList: [] })).toBe(false)
  })

  it('maps known states directly', () => {
    expect(selectProxyActive({ active: true, bypassList: [] })).toBe(true)
    expect(selectProxyActive({ active: false, bypassList: [] })).toBe(false)
  })

  // React 19 compares store snapshots by reference, so a selector must never
  // build a new object/array. A scalar return is stable by construction.
  it('returns a primitive', () => {
    expect(typeof selectProxyActive({ active: true, bypassList: [] })).toBe('boolean')
  })
})

describe('isBypassed', () => {
  const list = ['localhost', '127.0.0.1', '*.internal']

  it('matches exact hosts case-insensitively', () => {
    expect(isBypassed('localhost', list)).toBe(true)
    expect(isBypassed('LOCALHOST', list)).toBe(true)
    expect(isBypassed('127.0.0.1', list)).toBe(true)
  })

  it('honors wildcard suffixes like the Go proxy.BypassMatcher', () => {
    expect(isBypassed('llm.internal', list)).toBe(true)
    expect(isBypassed('deep.llm.internal', list)).toBe(true)
    expect(isBypassed('internal', list)).toBe(false)
    expect(isBypassed('notinternal', list)).toBe(false)
  })

  it('never matches on unrelated hosts or blank entries', () => {
    expect(isBypassed('google.com', list)).toBe(false)
    expect(isBypassed('localhost', ['', '   '])).toBe(false)
  })
})

describe('pinGatedByProxy', () => {
  const list = ['llm.lan', '*.internal']

  it('gates while the proxy dials for the host', () => {
    expect(pinGatedByProxy(true, [], 'https://llm.lan:8443/v1')).toBe(true)
  })

  it('re-arms the pin for a bypassed host (ADR-054)', () => {
    expect(pinGatedByProxy(true, list, 'https://llm.lan:8443/v1')).toBe(false)
    expect(pinGatedByProxy(true, list, 'https://a.internal/v1')).toBe(false)
  })

  it('is the port- and case-insensitive host comparison', () => {
    expect(pinGatedByProxy(true, list, 'https://LLM.LAN:1234/v1')).toBe(false)
  })

  it('does not gate without an effective proxy', () => {
    expect(pinGatedByProxy(false, list, 'https://vllm.example.com/v1')).toBe(false)
    expect(pinGatedByProxy(null, list, 'https://vllm.example.com/v1')).toBe(false)
  })

  it('gates an unparseable/absent host on the safe side', () => {
    expect(pinGatedByProxy(true, list, 'not-a-url')).toBe(true)
    expect(pinGatedByProxy(true, list, undefined)).toBe(true)
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
