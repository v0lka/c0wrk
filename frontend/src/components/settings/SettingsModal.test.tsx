// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run.
// Only the functions the mounted components actually call are mocked; the
// rest of the module surface stays absent per the project's partial-mock
// convention (see ModelConfigDialog.test).
const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
  getLogLevel: vi.fn(),
  hasDefaultModel: vi.fn(),
}))

vi.mock('@/api/config', () => ({
  MASKED_API_KEY: '***configured***',
  getConfig: mocks.getConfig,
  getLogLevel: mocks.getLogLevel,
  hasDefaultModel: mocks.hasDefaultModel,
}))

import { SettingsModal } from './SettingsModal'
import { useSettingsStore } from '@/stores/settingsStore'
import { useUIStore } from '@/stores/uiStore'

let container: HTMLDivElement
let root: Root

// The Dialog renders via a Radix Portal to document.body, so all queries
// target document.body rather than the local container.
function closeButton(): HTMLButtonElement {
  const btn = document.body.querySelector<HTMLButtonElement>('button[aria-label="Close"]')
  if (!btn) throw new Error('Close button not found')
  return btn
}

function bannerText(): string {
  return document.body.textContent ?? ''
}

function flush(): Promise<void> {
  // Flush pending microtasks (mocked promises) inside an act boundary.
  return act(async () => {})
}

beforeEach(() => {
  vi.clearAllMocks()
  // General-tab children call getConfig (ConfigWarningBanner, ProxySettings)
  // and getLogLevel (LogLevelSelector); an empty-but-valid llm section keeps
  // them quiet and leaves the default model unset (slow close path).
  mocks.getConfig.mockResolvedValue({ loaded: true, llm: {} })
  mocks.getLogLevel.mockResolvedValue('info')
  mocks.hasDefaultModel.mockResolvedValue(true)
  useSettingsStore.setState({ open: false, activeTab: 'general' })
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

describe('SettingsModal close flow', () => {
  it('closes immediately without probing the backend when a default model is already loaded', async () => {
    // LLM tab mounts LLMSettings → useLLMConfig.loadConfig reports the
    // effective default via onDefaultModelChange, priming the fast path.
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      llm: {
        default_model: 'claude-3-opus',
        anthropic: { api_key: '', models: ['claude-3-opus'] },
      },
    })
    useSettingsStore.setState({ open: true, activeTab: 'llm' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    await act(async () => {
      closeButton().click()
    })

    expect(mocks.hasDefaultModel).not.toHaveBeenCalled()
    expect(useSettingsStore.getState().open).toBe(false)
  })

  it('blocks close, shows the banner, and switches to the LLM tab when no default model is configured', async () => {
    mocks.hasDefaultModel.mockResolvedValue(false)
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    await act(async () => {
      closeButton().click()
    })
    await flush()

    expect(mocks.hasDefaultModel).toHaveBeenCalledTimes(1)
    expect(useSettingsStore.getState().open).toBe(true)
    expect(useSettingsStore.getState().activeTab).toBe('llm')
    expect(bannerText()).toContain('Default model is not configured.')
  })

  it('shows a spinner (disabled close button) while the check is in flight', async () => {
    let resolveCheck!: (v: boolean) => void
    mocks.hasDefaultModel.mockImplementation(
      () => new Promise<boolean>((res) => { resolveCheck = res }),
    )
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    await act(async () => {
      closeButton().click()
    })

    // While the probe is pending the close button is disabled and the modal
    // stays open.
    expect(closeButton().disabled).toBe(true)
    expect(useSettingsStore.getState().open).toBe(true)

    await act(async () => {
      resolveCheck(false)
    })
    await flush()

    // Resolving "no default" unblocks the button but keeps the modal open.
    expect(closeButton().disabled).toBe(false)
    expect(useSettingsStore.getState().open).toBe(true)
    expect(bannerText()).toContain('Default model is not configured.')
  })

  it('closes after the probe confirms a default model is configured', async () => {
    mocks.hasDefaultModel.mockResolvedValue(true)
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    await act(async () => {
      closeButton().click()
    })
    await flush()

    expect(mocks.hasDefaultModel).toHaveBeenCalledTimes(1)
    expect(useSettingsStore.getState().open).toBe(false)
    expect(bannerText()).not.toContain('Default model is not configured.')
  })

  it('fails open: closes when the probe errors', async () => {
    mocks.hasDefaultModel.mockRejectedValue(new Error('backend unavailable'))
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    await act(async () => {
      closeButton().click()
    })
    await flush()

    expect(useSettingsStore.getState().open).toBe(false)
    expect(bannerText()).not.toContain('Default model is not configured.')
  })
})

describe('SettingsModal General tab: session statistics display toggle', () => {
  it('shows the Session Statistics control, off by default, and toggling flips the uiStore flag', async () => {
    useUIStore.setState({ showSessionStats: false })
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    expect(bannerText()).toContain('Session Statistics')
    expect(bannerText()).toContain('Hidden')

    // The Toggle renders an sr-only checkbox; flipping it must update the
    // persisted uiStore flag that gates the ExecutionPanels stats row.
    const headers = Array.from(document.body.querySelectorAll('span'))
    const statsHeader = headers.find((s) => s.textContent === 'Session Statistics')
    expect(statsHeader).toBeDefined()
    const scope = statsHeader!.closest('div.flex.flex-col')
    const statsToggle = scope!.querySelector<HTMLInputElement>('input[type="checkbox"]')
    expect(statsToggle).toBeDefined()
    expect(statsToggle!.checked).toBe(false)

    await act(async () => {
      statsToggle!.click()
    })
    expect(useUIStore.getState().showSessionStats).toBe(true)
  })
})
