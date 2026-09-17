// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run.
const spies = vi.hoisted(() => ({
  getProviderTLSCertificate: vi.fn<() => Promise<{ fingerprint: string }>>(),
}))

vi.mock('@/api/config', () => ({
  getProviderTLSCertificate: spies.getProviderTLSCertificate,
}))

import { ProviderConfigForm } from './ProviderConfigForm'

/**
 * TLS-pin form tests (ADR-052): the "Custom TLS fingerprint" checkbox +
 * fingerprint field + Get button render ONLY for compatible providers; the
 * checkbox is a visibility control derived from the persisted pin, and
 * unchecking it emits an explicit tls_fingerprint: '' (clears the pin on
 * save). The Get button fills the fingerprint input from the backend RPC.
 */

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.clearAllMocks()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

interface FormConfig {
  api_key: string
  base_url: string
  models: string[]
  tls_fingerprint: string
}

function renderForm(provider: string, cfg: Partial<FormConfig> = {}) {
  const onConfigChange = vi.fn()
  const config: FormConfig = {
    api_key: 'k',
    base_url: 'https://llm.lan:8443/v1',
    models: [],
    tls_fingerprint: cfg.tls_fingerprint ?? '',
  }
  act(() => {
    root.render(
      <ProviderConfigForm
        activeProvider={provider}
        config={config}
        apiKeyDirty={false}
        hasRequiredCredentials
        modelsLoading={false}
        onConfigChange={onConfigChange}
        onApply={vi.fn()}
      />,
    )
  })
  return { onConfigChange }
}

const tlsCheckbox = () => Array.from(document.querySelectorAll('input[type="checkbox"]'))[0] as HTMLInputElement
const getBtn = () => Array.from(document.querySelectorAll('button')).find((b) => b.textContent === 'Get') as HTMLButtonElement

describe('ProviderConfigForm TLS section', () => {
  it('renders the "Custom TLS fingerprint" checkbox for a compatible provider', () => {
    renderForm('lmstudio')
    expect(document.body.textContent).toContain('Custom TLS fingerprint')
  })

  it('hides the TLS section for fixed providers', () => {
    renderForm('anthropic')
    expect(document.body.textContent).not.toContain('Custom TLS fingerprint')
  })

  it('hides the fingerprint field when no pin is set', () => {
    renderForm('lmstudio', { tls_fingerprint: '' })
    expect(document.body.textContent).not.toContain('Certificate fingerprint')
    expect(tlsCheckbox().checked).toBe(false)
  })

  it('shows the fingerprint field with pin when a pin is set', () => {
    renderForm('lmstudio', { tls_fingerprint: 'PIN123' })
    expect(document.body.textContent).toContain('Certificate fingerprint')
    expect(tlsCheckbox().checked).toBe(true)
    const inputs = Array.from(document.querySelectorAll('input')) as HTMLInputElement[]
    expect(inputs.some((i) => i.value === 'PIN123')).toBe(true)
  })

  it('unchecking the checkbox emits an explicit empty pin (clears the override on save)', () => {
    const { onConfigChange } = renderForm('lmstudio', { tls_fingerprint: 'PIN123' })
    expect(tlsCheckbox().checked).toBe(true)
    act(() => {
      tlsCheckbox().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onConfigChange).toHaveBeenCalledWith(expect.objectContaining({ tls_fingerprint: '' }))
  })

  it('Get button fetches the fingerprint and fills the input', async () => {
    spies.getProviderTLSCertificate.mockResolvedValue({ fingerprint: 'FETCHED-PIN' })
    const { onConfigChange } = renderForm('lmstudio', { tls_fingerprint: 'PIN123' })

    await act(async () => {
      getBtn().click()
    })

    expect(spies.getProviderTLSCertificate).toHaveBeenCalledWith({
      provider: 'lmstudio',
      base_url: 'https://llm.lan:8443/v1',
    })
    expect(onConfigChange).toHaveBeenCalledWith({ tls_fingerprint: 'FETCHED-PIN' })
  })

  it('Get button surfaces an inline error on failure', async () => {
    spies.getProviderTLSCertificate.mockRejectedValue(new Error('dial failed'))
    renderForm('lmstudio', { tls_fingerprint: 'PIN123' })

    await act(async () => {
      getBtn().click()
    })

    expect(document.body.textContent).toContain('dial failed')
  })

  it('Get button is disabled without a base URL', () => {
    act(() => {
      root.render(
        <ProviderConfigForm
          activeProvider="lmstudio"
          config={{ api_key: 'k', base_url: '', models: [], tls_fingerprint: 'PIN123' }}
          apiKeyDirty={false}
          hasRequiredCredentials={false}
          modelsLoading={false}
          onConfigChange={vi.fn()}
          onApply={vi.fn()}
        />,
      )
    })
    expect(getBtn().disabled).toBe(true)
  })
})

// --- Proxy-wins rule (ADR-053) ---------------------------------------------

function renderFormWithProxy(provider: string, cfg: Partial<FormConfig>, proxyActive: boolean) {
  const onConfigChange = vi.fn()
  const config: FormConfig = {
    api_key: 'k',
    base_url: cfg.base_url ?? 'https://llm.lan:8443/v1',
    models: [],
    tls_fingerprint: cfg.tls_fingerprint ?? '',
  }
  act(() => {
    root.render(
      <ProviderConfigForm
        activeProvider={provider}
        config={config}
        apiKeyDirty={false}
        hasRequiredCredentials
        modelsLoading={false}
        onConfigChange={onConfigChange}
        onApply={vi.fn()}
        proxyActive={proxyActive}
      />,
    )
  })
  return { onConfigChange }
}

describe('ProviderConfigForm proxy-wins (ADR-053)', () => {
  it('disables the TLS checkbox and shows the proxy comment when a proxy is active', () => {
    renderFormWithProxy('lmstudio', { tls_fingerprint: 'PIN123' }, true)
    expect(tlsCheckbox().disabled).toBe(true)
    expect(document.body.textContent).toContain('Not available while an HTTP proxy is enabled')
  })

  it('does not show the proxy comment without a proxy', () => {
    renderFormWithProxy('lmstudio', { tls_fingerprint: 'PIN123' }, false)
    expect(tlsCheckbox().disabled).toBe(false)
    expect(document.body.textContent).not.toContain('Not available while an HTTP proxy is enabled')
  })

  it('disables the Get button while a proxy is active', () => {
    renderFormWithProxy('lmstudio', { tls_fingerprint: 'PIN123' }, true)
    expect(getBtn().disabled).toBe(true)
  })

  it('disables the fingerprint input while a proxy is active', () => {
    renderFormWithProxy('lmstudio', { tls_fingerprint: 'PIN123' }, true)
    const input = Array.from(document.querySelectorAll('input'))
      .find((i) => i.value === 'PIN123') as HTMLInputElement
    expect(input.disabled).toBe(true)
  })
})
