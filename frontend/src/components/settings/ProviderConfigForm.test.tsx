// @vitest-environment jsdom
//
// The per-provider TLS pin section (ADR-054). Two properties matter most:
//
//  - The pin IS the switch, so the field is always present for a compatible
//    provider and there is no separate toggle checkbox.
//  - The "Get" button is UNCONDITIONAL with respect to the configured pin:
//    it is enabled whether or not a pin exists, sends no pin, and overwrites
//    whatever the field holds.

// Radix popper positioning (autoUpdate) observes the trigger/content with
// ResizeObserver, which jsdom does not provide (needed by the preset
// dropdown of the auto-retry EditableCombobox).
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

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
import { useProxyDraftStore } from '@/stores/proxyDraftStore'

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
  useProxyDraftStore.setState({ active: null, bypassList: [] })
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
  bypassList?: string[]
  /** Draft auto-retry interval in seconds; undefined = not carried. */
  autoRetry?: number
  /** Server-published auto-retry upper bound; omit for the 3600 fallback. */
  autoRetryMax?: number
  /**
   * The pin section ships COLLAPSED by default; tests that touch the field
   * expand it first (the default), mirroring the user's click on the section
   * header. The collapse behavior itself is tested with expandPin: false.
   */
  expandPin?: boolean
}

function render({
  provider = 'selfhosted',
  baseUrl = 'https://llm.lan:8443/v1',
  fingerprint = '',
  proxyActive = false,
  bypassList = [],
  autoRetry,
  autoRetryMax,
  expandPin = true,
}: RenderOpts = {}) {
  useProxyDraftStore.setState({ active: proxyActive, bypassList })
  act(() => {
    root.render(
      <ProviderConfigForm
        activeProvider={provider}
        config={{ api_key: 'key', base_url: baseUrl, tls_fingerprint: fingerprint, auto_retry_seconds: autoRetry }}
        apiKeyDirty={false}
        hasRequiredCredentials={true}
        modelsLoading={false}
        onConfigChange={(u) => changes.push(u as Record<string, unknown>)}
        onApply={() => {}}
        autoRetryMaxSeconds={autoRetryMax}
      />,
    )
  })
  if (expandPin) {
    // Expand while the field is not yet mounted: Radix renders the closed
    // content div but strips its children, so the input's presence is the
    // real "expanded" signal — repeated render() calls reuse the component
    // instance, and a blind click would TOGGLE an expanded block back shut.
    // Fixed providers have no trigger at all — nothing to expand.
    const inputMounted = container.querySelector('input[placeholder*="SPKI DER"]')
    const trigger = container.querySelector<HTMLButtonElement>('[data-slot="collapsible-trigger"]')
    if (trigger && !inputMounted) {
      act(() => {
        trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
    }
  }
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

describe('ProviderConfigForm collapse block', () => {
  function collapseTrigger(): HTMLButtonElement | null {
    return container.querySelector<HTMLButtonElement>('[data-slot="collapsible-trigger"]')
  }

  function collapseContent(): HTMLElement | null {
    return container.querySelector<HTMLElement>('[data-slot="collapsible-content"]')
  }

  // The old "Certificate fingerprint (SPKI, base64)" label is gone; the
  // section is introduced by the collapse trigger instead.
  it('titles the section "Pin TLS certificate (optional)" and drops the SPKI label', () => {
    render({ expandPin: false })
    expect(collapseTrigger()?.textContent).toBe('Pin TLS certificate (optional)')
    expect(container.textContent).not.toContain('Certificate fingerprint (SPKI, base64)')
  })

  // Radix renders the closed content div but strips its children, so
  // "collapsed by default" means the field is absent from the DOM until the
  // header is clicked — the visual collapse itself is the presentational
  // contract.
  it('is collapsed by default and mounts the field on expand', async () => {
    render({ fingerprint: pin, expandPin: false })
    expect(collapseContent()?.getAttribute('data-state')).toBe('closed')
    expect(fingerprintInput()).toBeNull()

    await act(async () => {
      collapseTrigger()!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(collapseContent()?.getAttribute('data-state')).toBe('open')
    expect(fingerprintInput()?.value).toBe(pin)
    expect(getButton()).not.toBeNull()
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

  // Bypass re-arms the pin (ADR-054): a host on the draft bypass list dials
  // directly, so its pin and its Get button stay usable even while the proxy
  // is on for everyone else.
  it('keeps the controls usable for a bypassed host', () => {
    render({ fingerprint: pin, proxyActive: true, bypassList: ['llm.lan'] })

    expect(fingerprintInput()?.disabled).toBe(false)
    expect(getButton()?.disabled).toBe(false)
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

  // The gate reads the live draft store: editing the bypass list in the
  // General tab re-enables the section in an already-mounted LLM tab with no
  // config re-read — the same propagation path the enabled/URL toggles use.
  it('reacts to a bypass-list edit published by the General tab', () => {
    render({ fingerprint: pin, proxyActive: true })
    expect(fingerprintInput()?.disabled).toBe(true)

    act(() => useProxyDraftStore.getState().setBypassList(['llm.lan']))
    expect(fingerprintInput()?.disabled).toBe(false)
    expect(getButton()?.disabled).toBe(false)
  })
})

// --- Auto-retry interval (compatible providers only) ---

describe('ProviderConfigForm auto-retry interval', () => {
  function retryInput(): HTMLInputElement | null {
    return container.querySelector<HTMLInputElement>('input[aria-label="Auto-retry interval"]')
  }

  function retryPresetsButton(): HTMLButtonElement | null {
    return container.querySelector<HTMLButtonElement>(
      'button[aria-label="Auto-retry interval presets"]',
    )
  }

  function retryLabel(): string | null {
    const labels = Array.from(container.querySelectorAll('label'))
    return labels.find((l) => l.textContent === 'Auto-retry interval')?.textContent ?? null
  }

  function typeRetry(value: string): void {
    const input = retryInput()
    if (!input) throw new Error('auto-retry input not found')
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

  function pressRetry(key: string): void {
    act(() => {
      retryInput()!.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
    })
  }

  async function openRetryPresets(): Promise<void> {
    const btn = retryPresetsButton()
    if (!btn) throw new Error('presets button not found')
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
  }

  it('is absent for fixed providers (anthropic, chatgpt)', () => {
    for (const provider of ['anthropic', 'chatgpt']) {
      render({ provider, baseUrl: '' })
      expect(retryInput()).toBeNull()
      expect(retryLabel()).toBeNull()
      // And neither the caption nor the presets button exist.
      expect(container.textContent).not.toContain('auto-resend off')
      expect(retryPresetsButton()).toBeNull()
    }
  })

  it('renders for a compatible provider with the persisted value from GetConfig', () => {
    render({ autoRetry: 45 })
    expect(retryInput()).not.toBeNull()
    expect(retryInput()?.value).toBe('45')
    expect(retryLabel()).toBe('Auto-retry interval')
  })

  // 0 is a VALID value: it disables the timer, and the field must display
  // it rather than blanking to a placeholder — saving it must emit an
  // EXPLICIT 0 (never undefined, which would keep the persisted interval).
  it('renders 0 as a valid value with the off caption', () => {
    render({ autoRetry: 0 })
    expect(retryInput()?.value).toBe('0')
    expect(container.textContent).toContain('0 = auto-resend off (retry only inside the engine)')
  })

  it('defaults the field to 0 when the config carries no interval', () => {
    render({ autoRetry: undefined })
    expect(retryInput()?.value).toBe('0')
    expect(container.textContent).toContain('0 = auto-resend off (retry only inside the engine)')
  })

  it('shows the unit suffix inside the combobox', () => {
    render({ autoRetry: 30 })
    const wrap = retryInput()?.closest('div')
    expect(wrap).not.toBeNull()
    const unit = Array.from(wrap!.querySelectorAll('span')).find((s) => s.textContent === 's')
    expect(unit).toBeDefined()
  })

  it('offers the presets [0, 5, 10, 30, 60, 120, 300] with the current one marked', async () => {
    render({ autoRetry: 30 })
    await openRetryPresets()

    const menu = document.body.querySelector('[role="menu"]')
    expect(menu).not.toBeNull()
    for (const p of [0, 5, 10, 30, 60, 120, 300]) {
      expect(menu!.textContent).toContain(String(p))
    }
    const selected = menu!.querySelector<HTMLElement>('[data-selected="true"]')
    expect(selected).not.toBeNull()
    expect(selected!.textContent).toContain('30')
  })

  it('commits a preset pick through onConfigChange', async () => {
    render({ autoRetry: 0 })
    await openRetryPresets()

    const menu = document.body.querySelector('[role="menu"]')!
    const item = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]')).find((o) =>
      o.textContent?.includes('60'),
    )
    expect(item).toBeDefined()
    act(() => {
      item!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(changes).toEqual([{ auto_retry_seconds: 60 }])
  })

  // Bounds: the EditableCombobox clamps manual input to [0, 3600].
  it('clamps above-max input to 3600 on Enter', () => {
    render({ autoRetry: 30 })
    typeRetry('9999')
    pressRetry('Enter')
    expect(changes).toEqual([{ auto_retry_seconds: 3600 }])
    expect(retryInput()?.value).toBe('3600')
  })

  // The upper bound is the SERVER-published limit (GetConfig →
  // llm.auto_retry_max_seconds, ADR-065), not the compiled-in fallback: a
  // backend with a tighter limit clamps here to that limit, so the form can
  // never propose a value the UpdateLLMConfig RPC would reject.
  it('clamps to the server-published max when it is tighter than the fallback', () => {
    render({ autoRetry: 30, autoRetryMax: 120 })
    typeRetry('9999')
    pressRetry('Enter')
    expect(changes).toEqual([{ auto_retry_seconds: 120 }])
    expect(retryInput()?.value).toBe('120')
  })

  it('commits an explicit 0 from manual input (disables the timer)', () => {
    render({ autoRetry: 120 })
    typeRetry('0')
    pressRetry('Enter')
    expect(changes).toEqual([{ auto_retry_seconds: 0 }])
    expect(retryInput()?.value).toBe('0')
  })

  it('commits a manual value within bounds', () => {
    render({ autoRetry: 0 })
    typeRetry('45')
    pressRetry('Enter')
    expect(changes).toEqual([{ auto_retry_seconds: 45 }])
  })
})
