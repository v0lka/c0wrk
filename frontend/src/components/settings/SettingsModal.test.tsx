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
  listFontFamilies: vi.fn(),
  getWebviewGpuPolicy: vi.fn(),
  setWebviewGpuPolicy: vi.fn(),
  getWebAccessInfo: vi.fn(),
  setWebAccessEnabled: vi.fn(),
}))

vi.mock('@/api/config', () => ({
  MASKED_API_KEY: '***configured***',
  getConfig: mocks.getConfig,
  getLogLevel: mocks.getLogLevel,
  hasDefaultModel: mocks.hasDefaultModel,
}))

// The Search tab's IndexingSettings embeds VectorIndexSettings, which
// probes the GPU list on mount (on Linux hosts). The General tab no longer
// mounts it — the Vector Index section moved to Search.
vi.mock('@/api/vector', () => ({
  listVectorIndexGPUs: mocks.listVectorIndexGPUs,
}))

// FontSettings (Appearance tab) enumerates the installed families on mount.
vi.mock('@/api/fonts', () => ({
  listFontFamilies: mocks.listFontFamilies,
}))

// WebviewGpuPolicySettings (General tab, Linux-only) probes the GPU policy on
// mount; on this machine's Linux test navigator it would otherwise hit the
// real Wails runtime boundary.
vi.mock('@/api/webview', () => ({
  getWebviewGpuPolicy: mocks.getWebviewGpuPolicy,
  setWebviewGpuPolicy: mocks.setWebviewGpuPolicy,
}))

// WebAccessSettings (General tab) reads the sidecar state on mount; without
// the mock it would hit the real Wails runtime boundary and log a transport
// error per render.
vi.mock('@/api/webAccess', () => ({
  getWebAccessInfo: mocks.getWebAccessInfo,
  setWebAccessEnabled: mocks.setWebAccessEnabled,
}))

// isWebTransport branches (the Web Access and Tabs blocks hide in the web
// transport) read this mutable flag; every other runtime export stays real.
const runtimeState = vi.hoisted(() => ({ web: false }))

vi.mock('@/api/runtime', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/runtime')>()
  return {
    ...actual,
    isWebTransport: () => runtimeState.web,
  }
})

// The Capabilities tab mounts CapabilitiesSettings (Subagents + Skills
// catalogs + capabilities modes) and, inside it, MCPSettings (status, tools,
// server configs). Only the functions those components actually call are
// mocked; the rest of each module stays absent per the partial-mock
// convention.
const capMocks = vi.hoisted(() => ({
  listAgents: vi.fn(),
  listSkills: vi.fn(),
  getCapabilityModes: vi.fn(),
  getMCPStatus: vi.fn(),
  getToolList: vi.fn(),
  getMCPServers: vi.fn(),
}))

vi.mock('@/api/agents', () => ({
  listAgents: capMocks.listAgents,
}))
vi.mock('@/api/skills', () => ({
  listSkills: capMocks.listSkills,
}))
vi.mock('@/api/capabilities', () => ({
  getCapabilityModes: capMocks.getCapabilityModes,
  updateCapabilityModes: vi.fn(async () => undefined),
}))
vi.mock('@/api/mcp', () => ({
  getMCPStatus: capMocks.getMCPStatus,
  getToolList: capMocks.getToolList,
  getMCPServers: capMocks.getMCPServers,
  updateMCPServers: vi.fn(async () => undefined),
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
  mocks.listFontFamilies.mockResolvedValue([])
  mocks.getWebviewGpuPolicy.mockResolvedValue({
    setting: 'auto',
    running: 'on-demand',
    running_source: 'auto',
    fallback: null,
    restart_required: false,
    linux_only: true,
  })
  // WebAccessSettings reads the sidecar state on mount; null = sidecar
  // unavailable, which the section renders as its quiet off state.
  mocks.getWebAccessInfo.mockResolvedValue(null)
  mocks.setWebAccessEnabled.mockResolvedValue(undefined)
  runtimeState.web = false
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
  it('hides the Session Statistics block while the experimental switch is off', async () => {
    // The block is an experimental surface: with the master switch off (the
    // store default) it must not render anywhere on the General tab — not
    // even its border wrapper.
    useExperimentalStore.setState({ enabled: false, loaded: true })
    useUIStore.setState({ showSessionStats: false })
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    expect(bannerText()).not.toContain('Show the per-run summary row')
    // The block's own header (a span whose exact text is "Session
    // Statistics") must be gone; matching the exact span rather than the
    // whole text body, because the always-visible Experimental Features
    // toggle description names the block too.
    const headers = Array.from(document.body.querySelectorAll('span'))
    expect(headers.find((s) => s.textContent === 'Session Statistics')).toBeUndefined()
  })

  it('shows the Session Statistics control, off by default, and toggling flips the uiStore flag', async () => {
    // Gate on: the block renders and the display toggle still drives only
    // the persisted uiStore flag that gates the ExecutionPanels stats row.
    useExperimentalStore.setState({ enabled: true, loaded: true })
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

describe('SettingsModal General tab: web access experimental gate', () => {
  it('hides the Web Access block, border wrapper included, while the experimental switch is off', async () => {
    // The block is an experimental surface (as well as a desktop-only one):
    // with the master switch off (the store default) it must not render
    // anywhere on the General tab — and its border-t wrapper must vanish with
    // it, leaving no orphaned divider between its neighbors.
    useExperimentalStore.setState({ enabled: false, loaded: true })
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    // Match the exact header span rather than the whole text body, because
    // the always-visible Experimental Features toggle description names the
    // block too.
    const spans = Array.from(document.body.querySelectorAll('span'))
    expect(spans.some((s) => s.textContent === 'Web Access')).toBe(false)
    expect(document.body.querySelector('[data-testid="web-access-settings"]')).toBeNull()

    // The sections the block sits between must now be adjacent: no empty
    // border wrapper left behind where the section used to be. With Web
    // Access hidden, HTTP Proxy is the FINAL General section — and the
    // Vector Index section no longer lives here at all (it moved to the
    // Search tab with the rest of the indexing controls).
    const h3s = Array.from(document.body.querySelectorAll('h3'))
    const proxy = h3s.find((h) => h.textContent === 'HTTP Proxy')
    expect(proxy).toBeDefined()
    expect(h3s.find((h) => h.textContent === 'Vector Index')).toBeUndefined()
    const stack = proxy!.closest('div.space-y-6')
    expect(stack).not.toBeNull()
    expect(stack!.lastElementChild).toBe(proxy!.closest('div.border-t'))
  })

  it('shows the Web Access block when the gate is on, and hides/shows it reactively without a remount', async () => {
    // Gate on: the block renders as before, inside the conventional border-t
    // section wrapper.
    useExperimentalStore.setState({ enabled: true, loaded: true })
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const spans = Array.from(document.body.querySelectorAll('span'))
    const webAccessHeader = spans.find((s) => s.textContent === 'Web Access')
    expect(webAccessHeader).toBeDefined()
    expect(document.body.querySelector('[data-testid="web-access-settings"]')).not.toBeNull()
    const section = webAccessHeader!.closest('div.border-t')
    expect(section).not.toBeNull()
    expect(section!.className).toContain('pt-4')

    // Flipping the master switch reactively hides the block: the gate is read
    // from the store inside SettingsModal, so no remount or reload is
    // involved.
    act(() => {
      useExperimentalStore.setState({ enabled: false })
    })
    expect(document.body.querySelector('[data-testid="web-access-settings"]')).toBeNull()
    expect(
      Array.from(document.body.querySelectorAll('span')).some(
        (s) => s.textContent === 'Web Access',
      ),
    ).toBe(false)

    // And back on again, still without any reload in between.
    act(() => {
      useExperimentalStore.setState({ enabled: true })
    })
    expect(document.body.querySelector('[data-testid="web-access-settings"]')).not.toBeNull()
  })
})

describe('SettingsModal General tab: prompt suggestions gate', () => {
  it('places the Prompt Suggestions section directly after System Notifications', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const headers = Array.from(document.body.querySelectorAll('span'))
    const notificationsHeader = headers.find((s) => s.textContent === 'System Notifications')
    const suggestionsHeader = headers.find((s) => s.textContent === 'Prompt Suggestions')
    expect(notificationsHeader).toBeDefined()
    expect(suggestionsHeader).toBeDefined()
    // DOM order pins the placement contract: the gate reads immediately after
    // the system-notifications block, before Session Statistics.
    expect(
      notificationsHeader!.compareDocumentPosition(suggestionsHeader!) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    // General-tab sections are divided by a border-t wrapper — the section
    // must follow that convention, not float undivided between neighbors.
    const section = suggestionsHeader!.closest('div.border-t')
    expect(section).not.toBeNull()
    expect(section!.className).toContain('pt-4')
  })

  it('places the Sidebar Research & Specs section directly after Prompt Suggestions', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const headers = Array.from(document.body.querySelectorAll('span'))
    const suggestionsHeader = headers.find((s) => s.textContent === 'Prompt Suggestions')
    const sidebarHeader = headers.find((s) => s.textContent === 'Sidebar Research & Specs')
    expect(suggestionsHeader).toBeDefined()
    expect(sidebarHeader).toBeDefined()
    // DOM order pins the placement contract: the sidebar-gate section reads
    // immediately after the prompt-suggestions gate, before Session
    // Statistics.
    expect(
      suggestionsHeader!.compareDocumentPosition(sidebarHeader!) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    // General-tab sections are divided by a border-t wrapper — the section
    // must follow that convention, not float undivided between neighbors.
    const section = sidebarHeader!.closest('div.border-t')
    expect(section).not.toBeNull()
    expect(section!.className).toContain('pt-4')
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
    // w-44: sized for the caps labels at text-sm (14px) so the longest
    // entry ("Model Profiles") fits without wrapping or clipping.
    expect(list!.className).toContain('w-44')
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

describe('SettingsModal appearance — Tabs block', () => {
  it('renders the Tabs switch at the bottom of the tab and toggles uiStore.tabsEnabled', async () => {
    useUIStore.setState({ tabsEnabled: false })
    useSettingsStore.setState({ open: true, activeTab: 'appearance' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    expect(bannerText()).toContain('Tabs')
    // Order lock-in: the Tabs section is the LAST block on the Appearance
    // tab — after the Theme section and the UI Scale + Fonts section — in
    // the shared space-y-6 stack.
    const spans = Array.from(document.body.querySelectorAll('span'))
    const tabsHeader = spans.find((s) => s.textContent === 'Tabs')
    expect(tabsHeader).toBeDefined()
    const tabsBlock = tabsHeader!.closest('div.flex.flex-col')
    expect(tabsBlock).not.toBeNull()
    const tabsSection = tabsBlock!.parentElement as HTMLElement
    expect(tabsSection.className).toContain('border-t')
    const appearanceStack = tabsSection.parentElement as HTMLElement
    expect(appearanceStack.className).toContain('space-y-6')
    expect(appearanceStack.lastElementChild).toBe(tabsSection)
    const prevSection = tabsSection.previousElementSibling
    expect(prevSection?.textContent).toContain('UI Scale')
    expect(prevSection?.textContent).toContain('Fonts')

    // The Toggle's peer-switch checkbox is the appearance tab's only checkbox.
    const toggle = document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')
    expect(toggle).not.toBeNull()
    expect(toggle!.checked).toBe(false)
    expect(useUIStore.getState().tabsEnabled).toBe(false)

    await act(async () => {
      toggle!.click()
    })
    expect(useUIStore.getState().tabsEnabled).toBe(true)

    await act(async () => {
      toggle!.click()
    })
    expect(useUIStore.getState().tabsEnabled).toBe(false)
  })
})

describe('SettingsModal Capabilities tab', () => {
  function primeCapabilitiesMocks() {
    capMocks.listAgents.mockResolvedValue([{ name: 'code-reviewer', description: 'Reviews code changes' }])
    capMocks.listSkills.mockResolvedValue([{ name: 'commit', description: 'Commit discipline skill' }])
    capMocks.getCapabilityModes.mockResolvedValue({ skills: {}, agents: {}, mcp: {} })
    capMocks.getMCPStatus.mockResolvedValue([])
    capMocks.getToolList.mockResolvedValue([])
    capMocks.getMCPServers.mockResolvedValue({})
  }

  it('renders the renamed Capabilities entry with the Component icon', async () => {
    primeCapabilitiesMocks()
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const trigger = Array.from(document.body.querySelectorAll<HTMLButtonElement>('[role="tab"]'))
      .find((el) => el.textContent?.includes('Capabilities'))
    expect(trigger).toBeDefined()
    // The lucide Component mark (svg.lucide-component), not the old Server.
    expect(trigger!.querySelector('svg.lucide-component')).not.toBeNull()
    expect(trigger!.textContent).not.toContain('MCP')
    // No stale 'mcp' tab remains in the nav.
    expect(Array.from(document.body.querySelectorAll('[role="tab"]')).some((el) => el.textContent?.trim() === 'MCP')).toBe(false)
  })

  it('mounts the three subsections in order: Subagents, MCP Servers, Skills', async () => {
    primeCapabilitiesMocks()
    useSettingsStore.setState({ open: true, activeTab: 'capabilities' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const titles = Array.from(document.body.querySelectorAll<HTMLElement>('span.text-sm.font-medium'))
      .map((el) => el.textContent)
      .filter((t): t is string => t === 'Subagents' || t === 'MCP Servers' || t === 'Skills')
    expect(titles).toEqual(['Subagents', 'MCP Servers', 'Skills'])
  })

  it('renders subagents and skills read-only: identity + two context selectors, no mutation buttons', async () => {
    primeCapabilitiesMocks()
    useSettingsStore.setState({ open: true, activeTab: 'capabilities' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    // Catalog entries render with both per-context selectors each.
    for (const name of ['code-reviewer', 'commit']) {
      expect(document.body.querySelector(`[aria-label="${name} chat mode"]`)).not.toBeNull()
      expect(document.body.querySelector(`[aria-label="${name} code mode"]`)).not.toBeNull()
    }
    expect(document.body.textContent).toContain('Reviews code changes')
    expect(document.body.textContent).toContain('Commit discipline skill')

    // Read-only: the only Edit/Delete buttons on this tab live inside MCP
    // server cards — with no servers configured there are none at all.
    const buttonLabels = Array.from(document.body.querySelectorAll('button')).map((b) => b.textContent?.trim())
    expect(buttonLabels).not.toContain('Edit')
    expect(buttonLabels).not.toContain('Delete')
    // MCP management stays available in its own subsection.
    expect(buttonLabels).toContain('Add Server')
  })
})

describe('SettingsModal web transport', () => {
  beforeEach(() => {
    runtimeState.web = true
  })

  afterEach(() => {
    runtimeState.web = false
  })

  it('hides the Web Access block on the General tab (control RPCs are unreachable from a tab)', async () => {
    // Gate on: the block is an experimental surface too, so with the master
    // switch off it would be hidden anyway — setting it on isolates the
    // transport branch this test exists to pin.
    useExperimentalStore.setState({ enabled: true, loaded: true })
    useSettingsStore.setState({ open: true, activeTab: 'general' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const spans = Array.from(document.body.querySelectorAll('span'))
    expect(spans.some((s) => s.textContent === 'Web Access')).toBe(false)
    expect(document.body.querySelector('[data-testid="web-access-settings"]')).toBeNull()
  })

  it('hides the Tabs block on the Appearance tab (the browser owns the tabs)', async () => {
    useSettingsStore.setState({ open: true, activeTab: 'appearance' })
    act(() => {
      root.render(<SettingsModal />)
    })
    await flush()

    const spans = Array.from(document.body.querySelectorAll('span'))
    expect(spans.some((s) => s.textContent === 'Tabs')).toBe(false)
  })
})
