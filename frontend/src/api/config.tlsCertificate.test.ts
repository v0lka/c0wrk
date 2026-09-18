// @vitest-environment jsdom
//
// Tests for the GetProviderTLSCertificate RPC wrapper in api/config.ts: the
// request shape (the "Get" button is unconditional with respect to any
// configured pin, so no fingerprint is sent — ADR-052) and boundary
// validation of the backend response.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

import { getProviderTLSCertificate } from './config'

let consoleError: ReturnType<typeof vi.spyOn>

function installAppBinding(impl: (req: unknown) => unknown): void {
  ;(window as unknown as Record<string, unknown>).go = {
    desktop: { App: { GetProviderTLSCertificate: impl } },
  }
}

beforeEach(() => {
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  delete (window as unknown as Record<string, unknown>).go
})

afterEach(() => {
  consoleError.mockRestore()
})

describe('getProviderTLSCertificate', () => {
  const pin = 'k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g='

  it('returns the fetched fingerprint', async () => {
    installAppBinding(async () => ({ fingerprint: pin }))
    await expect(
      getProviderTLSCertificate({ provider: 'selfhosted', base_url: 'https://llm.lan:8443/v1' }),
    ).resolves.toEqual({ fingerprint: pin })
  })

  it('forwards provider and draft base_url, and sends no pin', async () => {
    const seen: unknown[] = []
    installAppBinding(async (req) => {
      seen.push(req)
      return { fingerprint: pin }
    })

    await getProviderTLSCertificate({ provider: 'selfhosted', base_url: 'https://draft.lan:8443/v1' })

    expect(seen).toHaveLength(1)
    expect(seen[0]).toEqual({ provider: 'selfhosted', base_url: 'https://draft.lan:8443/v1' })
    // The button always reports what the endpoint currently serves; sending a
    // pin would imply the answer could depend on it.
    expect(seen[0]).not.toHaveProperty('tls_fingerprint')
    expect(seen[0]).not.toHaveProperty('fingerprint')
  })

  it.each([
    { name: 'null', value: null },
    { name: 'undefined', value: undefined },
    { name: 'empty object', value: {} },
    { name: 'non-string fingerprint', value: { fingerprint: 42 } },
    { name: 'missing field', value: { other: 'x' } },
  ])('rejects a malformed response: $name', async ({ value }) => {
    installAppBinding(async () => value)
    await expect(getProviderTLSCertificate({ provider: 'p' })).rejects.toThrow(/invalid data/)
  })

  it('propagates a backend error (e.g. the proxy-active rejection)', async () => {
    installAppBinding(async () => {
      throw new Error('TLS fingerprint fetching is unavailable while an HTTP proxy is enabled')
    })
    await expect(getProviderTLSCertificate({ provider: 'p' })).rejects.toThrow(/HTTP proxy is enabled/)
  })

  it('propagates the runtime-unavailable error when the Wails binding is absent', async () => {
    await expect(getProviderTLSCertificate({ provider: 'p' })).rejects.toThrow()
  })
})
