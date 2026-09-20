// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const spies = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateProxySettings: vi.fn<(req: unknown) => Promise<void>>(),
}))

vi.mock('@/api/config', () => ({
  getConfig: spies.getConfig,
  updateProxySettings: spies.updateProxySettings,
}))

import { ProxySettings } from './ProxySettings'
import { useProxyDraftStore } from '@/stores/proxyDraftStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers()
  spies.updateProxySettings.mockResolvedValue(undefined)
  useProxyDraftStore.setState({ active: null })
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
  vi.useRealTimers()
})

type ProxyPayload = { enabled: boolean; url: string; bypass_list: string[]; tls_cert_dir: string }

async function renderProxySettings(proxy: ProxyPayload) {
  spies.getConfig.mockResolvedValue({ proxy })
  await act(async () => {
    root.render(<ProxySettings />)
  })
}

/** The "Use proxy" master toggle. */
function enabledToggle(): HTMLInputElement {
  const input = container.querySelector('input[type="checkbox"]')
  if (!input) throw new Error('proxy enabled toggle not found')
  return input as HTMLInputElement
}

/** The proxy URL text input (only rendered while the proxy is enabled). */
function urlInput(): HTMLInputElement {
  const input = container.querySelector('input[placeholder^="http://user"]')
  if (!input) throw new Error('proxy URL input not found')
  return input as HTMLInputElement
}

/** Click a checkbox the way React sees it (native click → synthetic change). */
function toggle(input: HTMLInputElement) {
  act(() => {
    input.click()
  })
}

/**
 * Type into a controlled input. React tracks the previous value on the DOM
 * node, so the native setter has to be used or the synthetic change event is
 * suppressed as a no-op (the repo idiom, see ModelConfigDialog.test).
 */
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

describe('ProxySettings → proxyDraftStore', () => {
  it('publishes the effective proxy state on load', async () => {
    await renderProxySettings({ enabled: true, url: 'http://proxy.lan:3128', bypass_list: [], tls_cert_dir: '' })
    expect(useProxyDraftStore.getState().active).toBe(true)
  })

  it('publishes false for an enabled-but-empty proxy URL on load', async () => {
    await renderProxySettings({ enabled: true, url: '', bypass_list: [], tls_cert_dir: '' })
    expect(useProxyDraftStore.getState().active).toBe(false)
  })

  // The LLM tab must see the change even if the user switches tabs inside
  // the 800 ms debounce window, so publishing cannot wait for the save.
  it('publishes synchronously, without waiting for the debounced save', async () => {
    await renderProxySettings({ enabled: false, url: 'http://proxy.lan:3128', bypass_list: [], tls_cert_dir: '' })
    expect(useProxyDraftStore.getState().active).toBe(false)

    toggle(enabledToggle())

    // No timer has fired yet: the backend has NOT been told.
    expect(spies.updateProxySettings).not.toHaveBeenCalled()
    // ...but the store already reflects the user's action.
    expect(useProxyDraftStore.getState().active).toBe(true)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(800)
    })
    expect(spies.updateProxySettings).toHaveBeenCalledTimes(1)
    expect(useProxyDraftStore.getState().active).toBe(true)
  })

  it('publishes false when the proxy is switched off', async () => {
    await renderProxySettings({ enabled: true, url: 'http://proxy.lan:3128', bypass_list: [], tls_cert_dir: '' })
    toggle(enabledToggle())
    expect(useProxyDraftStore.getState().active).toBe(false)
  })

  // Clearing the URL makes an enabled proxy dial directly, which re-arms the
  // pin — the store has to follow.
  it('publishes false when the URL is cleared on an enabled proxy', async () => {
    await renderProxySettings({ enabled: true, url: 'http://proxy.lan:3128', bypass_list: [], tls_cert_dir: '' })
    expect(useProxyDraftStore.getState().active).toBe(true)

    typeInto(urlInput(), '')
    expect(useProxyDraftStore.getState().active).toBe(false)

    typeInto(urlInput(), 'http://proxy.lan:3128')
    expect(useProxyDraftStore.getState().active).toBe(true)
  })
})
