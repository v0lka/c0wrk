// @vitest-environment jsdom
//
// The per-provider TLS pin section (ADR-054). Two properties matter most:
//
//  - The pin IS the switch, so the field is always present for a compatible
//    provider and there is no separate toggle checkbox.
//  - The "Get" button is UNCONDITIONAL with respect to the configured pin:
//    it is enabled whether or not a pin exists, sends no pin, and overwrites
//    whatever the field holds.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const spies = vi.hoisted(() => ({
  getProviderTLSCertificate: vi.fn<(req: Record<string, unknown>) => Promise<{ fingerprint: string }>>(),
}))

vi.mock('@/api/config', () => ({
  getProviderTLSCertificate: spies.getProviderTLSCertificate,
  MASKED_API_KEY: '***configured***',
}))

import { ProviderConfigForm } from './ProviderConfigForm'

const pin = 'k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g='
const serverPin = 'Zm9vYmFyYmF6cXV1eDEyMzQ1Njc4OWFiY2RlZmdoaT0='

let container: HTMLDivElement
let root: Root
let changes: Array<Record<string, unknown>>

beforeEach(() => {
  vi.clearAllMocks()
  spies.getProviderTLSCertificate.mockResolvedValue({ fingerprint: serverPin })
  changes = []
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
})

interface RenderOpts {
  provider?: string
  baseUrl?: string
  fingerprint?: string
  proxyActive?: boolean
}

function render({
  provider = 'selfhosted',
  baseUrl = 'https://llm.lan:8443/v1',
  fingerprint = '',
  proxyActive = false,
}: RenderOpts = {}) {
  act(() => {
    root.render(
      <ProviderConfigForm
        activeProvider={provider}
        config={{ api_key: 'key', base_url: baseUrl, tls_fingerprint: fingerprint }}
        apiKeyDirty={false}
        hasRequiredCredentials={true}
        modelsLoading={false}
        onConfigChange={(u) => changes.push(u as Record<string, unknown>)}
        onApply={() => {}}
        proxyActive={proxyActive}
      />,
    )
  })
}

function fingerprintInput(): HTMLInputElement | null {
  return container.querySelector('input[placeholder*="SPKI DER"]')
}

function getButton(): HTMLButtonElement | null {
  const buttons = Array.from(container.querySelectorAll('button'))
  return (buttons.find((b) => b.textContent?.trim() === 'Get') as HTMLButtonElement) ?? null
}

function typeInto(input: HTMLInputElement, value: string) {
  const nativeInputSetter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    'value',
  )?.set
  if (!nativeInputSetter) throw new Error('native input setter not found')
  act(() => {
    nativeInputSetter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

describe('ProviderConfigForm TLS section visibility', () => {
  it('is absent for fixed providers', () => {
    for (const provider of ['anthropic', 'chatgpt']) {
      render({ provider, baseUrl: '' })
      expect(fingerprintInput()).toBeNull()
      expect(getButton()).toBeNull()
    }
  })

  // The pin is the switch: an empty field already means "standard
  // verification", so nothing is gated behind a toggle.
  it('is always present for a compatible provider, pinned or not', () => {
    render({ fingerprint: '' })
    expect(fingerprintInput()).not.toBeNull()
    expect(getButton()).not.toBeNull()

    render({ fingerprint: pin })
    expect(fingerprintInput()?.value).toBe(pin)
    expect(getButton()).not.toBeNull()
  })

  it('renders no toggle checkbox in the form', () => {
    render({ fingerprint: pin })
    expect(container.querySelectorAll('input[type="checkbox"]')).toHaveLength(0)
  })
})

describe('ProviderConfigForm pin editing', () => {
  it('emits the typed value', () => {
    render({ fingerprint: '' })
    typeInto(fingerprintInput()!, pin)
    expect(changes).toEqual([{ tls_fingerprint: pin }])
  })

  // Clearing the field is how the override is switched off; nothing else
  // should be emitted alongside it.
  it('emits an explicit empty string when the field is cleared', () => {
    render({ fingerprint: pin })
    typeInto(fingerprintInput()!, '')
    expect(changes).toEqual([{ tls_fingerprint: '' }])
  })
})

describe('ProviderConfigForm Get button', () => {
  it('is enabled whether or not a pin is already set', () => {
    render({ fingerprint: '' })
    expect(getButton()?.disabled).toBe(false)

    render({ fingerprint: pin })
    expect(getButton()?.disabled).toBe(false)
  })

  it('requests the fingerprint with the draft base URL and sends no pin', async () => {
    render({ fingerprint: pin, baseUrl: 'https://draft.lan:8443/v1' })
    await act(async () => {
      getButton()!.click()
    })
    await flush()

    expect(spies.getProviderTLSCertificate).toHaveBeenCalledTimes(1)
    const req = spies.getProviderTLSCertificate.mock.calls[0]![0]
    expect(req).toEqual({ provider: 'selfhosted', base_url: 'https://draft.lan:8443/v1' })
    expect(req).not.toHaveProperty('tls_fingerprint')
  })

  // "It just takes the current one": an existing pin is replaced, not kept.
  it('overwrites an existing pin with the fetched value', async () => {
    render({ fingerprint: pin })
    await act(async () => {
      getButton()!.click()
    })
    await flush()
    expect(changes).toEqual([{ tls_fingerprint: serverPin }])
  })

  it('fills an empty field with the fetched value', async () => {
    render({ fingerprint: '' })
    await act(async () => {
      getButton()!.click()
    })
    await flush()
    expect(changes).toEqual([{ tls_fingerprint: serverPin }])
  })

  it('is disabled without a base URL', () => {
    render({ baseUrl: '' })
    expect(getButton()?.disabled).toBe(true)
  })

  it('shows the error and emits no change when the fetch fails', async () => {
    spies.getProviderTLSCertificate.mockRejectedValueOnce(new Error('TLS dial 10.0.0.1:8443: connection refused'))
    render({ fingerprint: pin })

    await act(async () => {
      getButton()!.click()
    })
    await flush()

    expect(changes).toEqual([])
    expect(container.textContent).toContain('connection refused')
    // The component survives and the button is usable again.
    expect(getButton()?.disabled).toBe(false)
  })

  it('recovers after a failed attempt', async () => {
    spies.getProviderTLSCertificate.mockRejectedValueOnce(new Error('connection refused'))
    render({ fingerprint: '' })
    await act(async () => { getButton()!.click() })
    await flush()
    expect(container.textContent).toContain('connection refused')

    await act(async () => { getButton()!.click() })
    await flush()
    expect(changes).toEqual([{ tls_fingerprint: serverPin }])
    expect(container.textContent).not.toContain('connection refused')
  })
})

describe('ProviderConfigForm proxy gate', () => {
  it('disables the field and the button, and explains why', () => {
    render({ fingerprint: pin, proxyActive: true })

    expect(fingerprintInput()?.disabled).toBe(true)
    expect(getButton()?.disabled).toBe(true)
    expect(container.textContent).toContain('HTTP Proxy')
    expect(container.textContent).toContain('bypass list')
  })

  // A configured pin stays visible while the proxy is on: it is preserved and
  // re-arms when the proxy is disabled.
  it('still shows the persisted pin', () => {
    render({ fingerprint: pin, proxyActive: true })
    expect(fingerprintInput()?.value).toBe(pin)
  })

  it('leaves the controls usable when no proxy is active', () => {
    render({ fingerprint: pin, proxyActive: false })
    expect(fingerprintInput()?.disabled).toBe(false)
    expect(getButton()?.disabled).toBe(false)
    expect(container.textContent).not.toContain('HTTP Proxy')
  })
})
