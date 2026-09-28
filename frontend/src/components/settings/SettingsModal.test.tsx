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
  listVectorIndexGPUs: vi.fn(),
}))

vi.mock('@/api/config', () => ({
  MASKED_API_KEY: '***configured***',
  getConfig: mocks.getConfig,
  getLogLevel: mocks.getLogLevel,
  hasDefaultModel: mocks.hasDefaultModel,
}))

// VectorIndexSettings (General tab) probes the GPU list on mount.
vi.mock('@/api/vector', () => ({
  listVectorIndexGPUs: mocks.listVectorIndexGPUs,
}))

import { SettingsModal } from './SettingsModal'
import { useSettingsStore } from '@/stores/settingsStore'
import { useUIStore } from '@/stores/uiStore'
import { useExperimentalStore } from '@/stores/experimentalStore'

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
  // and getLogLevel (LogLevelSelector); a minimal-but-VALID llm section keeps
  // them quiet and leaves the default model unset (slow close path). The
  // auto-retry bound is REQUIRED (useLLMConfig fails the load without it).
  mocks.getConfig.mockResolvedValue({ loaded: true, llm: { auto_retry_max_seconds: 3600 } })
  mocks.getLogLevel.mockResolvedValue('info')
  mocks.hasDefaultModel.mockResolvedValue(true)
  mocks.listVectorIndexGPUs.mockResolvedValue([])
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
        auto_retry_max_seconds: 3600,
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

describe('SettingsModal Appearance tab', () => {
  /** Tab labels in the settings tab strip, in DOM order. */
  function tabLabels(): string[] {
    return Array.from(
      document.body.querySelectorAll<HTMLElement>('[data-slot="tabs-trigger"]'),
    ).map((t) => (t.textContent ?? '').trim())
  }

  it('sits directly after General in the tab strip', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const labels = tabLabels()
    expect(labels.indexOf('General')).toBe(0)
    expect(labels.indexOf('Appearance')).toBe(1)
  })

  it('renders Theme and UI Scale under Appearance, without Log Level', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'appearance' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    expect(bannerText()).toContain('Theme')
    expect(bannerText()).toContain('UI Scale')
    expect(bannerText()).not.toContain('Log Level')
  })

  it('stacks Theme and UI Scale as separate blocks, Theme first', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'appearance' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const spans = Array.from(document.body.querySelectorAll('span'))
    const themeHeader = spans.find((s) => s.textContent === 'Theme')
    const scaleHeader = spans.find((s) => s.textContent === 'UI Scale')
    expect(themeHeader).toBeDefined()
    expect(scaleHeader).toBeDefined()

    const themeBlock = themeHeader!.closest('div.flex.flex-col')
    const scaleBlock = scaleHeader!.closest('div.flex.flex-col')
    expect(themeBlock).not.toBeNull()
    expect(scaleBlock).not.toBeNull()

    // Theme sits at the top of the Appearance stack; UI Scale follows in its
    // own section below it. The Theme header must precede the UI Scale header
    // in DOM order, and the UI Scale section must live inside the same stack.
    const stack = themeBlock!.parentElement as HTMLElement
    expect(stack.className).toContain('space-y-6')
    expect(stack.contains(themeHeader!)).toBe(true)
    expect(stack.contains(scaleHeader!)).toBe(true)
    expect(
      themeHeader!.compareDocumentPosition(scaleHeader!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()

    // The UI Scale section is separated from Theme by a divider (mirroring
    // how General separates its sections), not laid out as a column.
    const scaleSection = scaleBlock!.parentElement as HTMLElement
    expect(scaleSection.className).toContain('border-t')
    expect(scaleSection.parentElement).toBe(stack)
  })

  it('keeps Log Level in General and no longer shows Theme or UI Scale there', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    expect(bannerText()).toContain('Log Level')
    expect(bannerText()).not.toContain('UI Scale')
    expect(bannerText()).not.toContain('Theme')
  })

  it('gives the active section a right-side gutter before the scrollbar', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const content = document.body.querySelector<HTMLElement>('[data-slot="tabs-content"]')
    expect(content).not.toBeNull()
    expect(content!.className).toContain('custom-scrollbar')
    expect(content!.className).toContain('pr-2')
  })
})

describe('SettingsModal Model Profiles tab (independent of the experimental switch)', () => {
  /** Tab labels in the settings tab strip, in DOM order. */
  function tabLabels(): string[] {
    return Array.from(
      document.body.querySelectorAll<HTMLElement>('[data-slot="tabs-trigger"]'),
    ).map((t) => (t.textContent ?? '').trim())
  }

  // ADR-044: the tab graduated out of the experimental gate. It must be
  // present whether the switch is off (the state that used to hide it) or on.
  it('always renders the Model Profiles tab, with the switch off and on', async () => {
    await act(async () => {
      useExperimentalStore.setState({ enabled: false, loaded: true })
      useSettingsStore.setState({ open: true, activeTab: 'general' })
      root.render(<SettingsModal />)
    })
    await flush()

    expect(tabLabels()).toContain('Model Profiles')

    await act(async () => {
      useExperimentalStore.setState({ enabled: true, loaded: true })
    })
    await flush()

    expect(tabLabels()).toContain('Model Profiles')
  })
})

describe('SettingsModal layout: vertical left nav and fixed height', () => {
  it('lays the section nav out vertically on the left', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const tabs = document.body.querySelector<HTMLElement>('[data-slot="tabs"]')
    expect(tabs).not.toBeNull()
    expect(tabs!.getAttribute('data-orientation')).toBe('vertical')

    const list = document.body.querySelector<HTMLElement>('[data-slot="tabs-list"]')
    expect(list).not.toBeNull()
    // A column, not the previous edge-to-edge horizontal strip.
    expect(list!.className).toContain('flex-col')
    expect(list!.className).not.toContain('justify-between')
    // Fixed-width column pinned to the left of the scrolling content.
    expect(list!.className).toContain('w-40')
    expect(list!.className).toContain('shrink-0')

    // Section labels stay visible in the sidebar (no sm-gated hiding).
    const labels = Array.from(
      document.body.querySelectorAll<HTMLElement>('[data-slot="tabs-trigger"] span'),
    ).map((s) => s.textContent)
    expect(labels).toContain('General')
    expect(labels).toContain('Model Profiles')
    labels.forEach((l) => expect(l).not.toBe(''))
  })

  it('styles the nav as a transparent caps index with a bold, color-swapped active entry', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const list = document.body.querySelector<HTMLElement>('[data-slot="tabs-list"]')
    expect(list).not.toBeNull()
    const listClasses = list!.className.split(/\s+/)
    // Transparent track (the filled `bg-muted` pill is gone) with a wider
    // vertical gap between the entries (was gap-1).
    expect(listClasses).toContain('bg-transparent')
    expect(listClasses).not.toContain('bg-muted')
    expect(listClasses).toContain('gap-3')
    expect(listClasses).not.toContain('gap-1')

    const triggers = Array.from(
      document.body.querySelectorAll<HTMLElement>('[data-slot="tabs-trigger"]'),
    )
    expect(triggers.length).toBeGreaterThan(0)
    for (const trigger of triggers) {
      const classes = trigger.className.split(/\s+/)
      // Caps labels.
      expect(classes).toContain('uppercase')
      // Active entry: bold, with no state-dependent background left.
      expect(classes).toContain('data-[state=active]:font-bold')
      expect(classes).toContain('data-[state=active]:bg-transparent')
      expect(classes).not.toContain('data-[state=active]:bg-background')
      // No frame around the entries. The shared trigger's 1px border is
      // removed by zeroing its WIDTH (`border-0`): the unlayered
      // `* { border-color }` rule in index.css outranks the layered
      // `border-transparent` utility, so a color-only override would leave
      // the frame painted in the theme border color.
      expect(classes).toContain('border-0')
      expect(classes).not.toContain('border')
      // ...while the dark hover highlight is deliberately retained.
      expect(classes).toContain('hover:bg-muted/50')
      // Colors swapped: inactive keeps the full-contrast foreground (the
      // former active color); active recedes to a muted tone (the former
      // inactive color).
      expect(classes).toContain('text-foreground')
      expect(classes).toContain('data-[state=active]:text-foreground/60')
      expect(classes).not.toContain('text-foreground/60')
      expect(classes).not.toContain('data-[state=active]:text-foreground')
    }
  })

  it('pins the dialog to a fixed height equal to its former maximum height', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const content = document.body.querySelector<HTMLElement>('[data-slot="dialog-content"]')
    expect(content).not.toBeNull()
    // Fixed height == the height the dialog could previously grow to (0.8 of
    // the zoom-corrected viewport), instead of a content-driven max-height.
    expect(content!.className).toContain('h-[calc(var(--ui-vh)*0.8)]')
    expect(content!.className).not.toContain('max-h-[calc(var(--ui-vh)*0.8)]')
  })
})
