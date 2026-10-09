// @vitest-environment jsdom
// SearchSettings — the web-search provider / API-key panel.
//
// The API-key field is an explicit-commit field: keystrokes stay in a local
// draft (the stored key is invisible while masked, so an autosaved fragment
// could overwrite the real key) and only blur/Enter move the draft into the
// save flow, which persists behind a 500 ms debounce. A pending COMMITTED
// edit must still be FLUSHED when the panel unmounts (closing the settings
// dialog / switching tabs) instead of being silently discarded — the same
// hardening the ProxySettings / VectorIndexSettings siblings carry — and an
// UNCOMMITTED draft must never persist, not even via the flush.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const spies = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateSearchSettings: vi.fn<(req: unknown) => Promise<void>>(),
}))

vi.mock('@/api/config', () => ({
  getConfig: spies.getConfig,
  updateSearchSettings: spies.updateSearchSettings,
  MASKED_API_KEY: '***configured***',
}))
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { SearchSettings } from './SearchSettings'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers()
  spies.updateSearchSettings.mockResolvedValue(undefined)
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

type SearchPayload = { provider: string; api_key: string }

async function renderSearchSettings(search: SearchPayload) {
  spies.getConfig.mockResolvedValue({ search })
  await act(async () => {
    root.render(<SearchSettings />)
  })
}

/** The API-key text input (rendered as a password field for plain keys). */
function apiKeyInput(): HTMLInputElement {
  const input = container.querySelector('input[type="password"]')
  if (!input) throw new Error('api key input not found')
  return input as HTMLInputElement
}

/**
 * Type into a controlled input. React tracks the previous value on the DOM
 * node, so the native setter has to be used or the synthetic change event is
 * suppressed as a no-op (the repo idiom, see ProxySettings.test).
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

describe('SearchSettings — explicit-commit debounced save', () => {
  /**
   * Move focus to the input and then away from it (React binds onBlur to the
   * delegated focusout; jsdom fires the native blur/focusout pair on .blur()).
   */
  function blurInput(input: HTMLInputElement) {
    act(() => {
      input.focus()
    })
    act(() => {
      input.blur()
    })
  }

  it('saves the API key only after the explicit commit (blur), through the 500 ms debounce', async () => {
    await renderSearchSettings({ provider: 'tavily', api_key: '' })

    typeInto(apiKeyInput(), 'sk-test')
    // Keystrokes NEVER schedule a save: the stored key is invisible while the
    // field is masked/empty, so autosaving per keystroke would persist a
    // half-typed fragment over the real key.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500)
    })
    expect(spies.updateSearchSettings).not.toHaveBeenCalled()

    // The blur commit is the single point where a key edit becomes saveable;
    // from there it flows through the normal 500 ms debounce.
    blurInput(apiKeyInput())
    expect(spies.updateSearchSettings).not.toHaveBeenCalled()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500)
    })
    expect(spies.updateSearchSettings).toHaveBeenCalledTimes(1)
    expect(spies.updateSearchSettings).toHaveBeenCalledWith({
      provider: 'tavily',
      api_key: 'sk-test',
    })
  })

  it('commits the API key draft on Enter without a blur', async () => {
    await renderSearchSettings({ provider: 'tavily', api_key: '' })

    const input = apiKeyInput()
    typeInto(input, 'sk-test')
    act(() => {
      input.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
      )
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500)
    })
    expect(spies.updateSearchSettings).toHaveBeenCalledTimes(1)
    expect(spies.updateSearchSettings).toHaveBeenCalledWith({
      provider: 'tavily',
      api_key: 'sk-test',
    })
  })

  it('never persists an uncommitted half-typed API key on unmount', async () => {
    await renderSearchSettings({ provider: 'tavily', api_key: '***configured***' })

    // The user focuses the (empty-rendering, masked) key field and types a
    // fragment, then closes the dialog without ever leaving the field — no
    // blur, no Enter. The stored key must survive untouched.
    const input = apiKeyInput()
    act(() => {
      input.focus()
    })
    typeInto(input, 'sk-tes')
    expect(spies.updateSearchSettings).not.toHaveBeenCalled()

    act(() => {
      root.unmount()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(spies.updateSearchSettings).not.toHaveBeenCalled()
  })

  it('blur without an edit commits nothing over a masked stored key', async () => {
    await renderSearchSettings({ provider: 'tavily', api_key: '***configured***' })

    // Focus clears the input for a fresh value; leaving without typing must
    // not schedule a save (an empty draft maps back to the masked sentinel,
    // which would only re-send the existing state anyway).
    blurInput(apiKeyInput())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(spies.updateSearchSettings).not.toHaveBeenCalled()
  })

  it('flushes a committed pending edit on unmount instead of discarding it', async () => {
    await renderSearchSettings({ provider: 'tavily', api_key: '' })

    typeInto(apiKeyInput(), 'sk-test')
    blurInput(apiKeyInput())
    expect(spies.updateSearchSettings).not.toHaveBeenCalled()

    // Close the settings dialog BEFORE the debounce window elapses — the
    // committed edit must be flushed, not cancelled (a cancelled edit is
    // lost with no error surfaced anywhere).
    act(() => {
      root.unmount()
    })

    expect(spies.updateSearchSettings).toHaveBeenCalledTimes(1)
    expect(spies.updateSearchSettings).toHaveBeenCalledWith({
      provider: 'tavily',
      api_key: 'sk-test',
    })

    // The abandoned timer must not produce a duplicate save afterwards.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500)
    })
    expect(spies.updateSearchSettings).toHaveBeenCalledTimes(1)
  })

  it('does not fire a save on unmount when nothing is pending', async () => {
    await renderSearchSettings({ provider: 'tavily', api_key: '' })

    act(() => {
      root.unmount()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500)
    })
    expect(spies.updateSearchSettings).not.toHaveBeenCalled()
  })
})
