// @vitest-environment jsdom
//
// ScaleHud — the transient Ctrl+wheel zoom HUD.
//
// The component contract sits on top of useCtrlWheelScale (fully covered by
// hooks/useCtrlWheelScale.test.tsx): hidden is the steady state and must
// render nothing at all; while visible it must expose the reset action, a
// live slider bound to the store, and a label mirroring the current scale —
// every interaction landing in setScale, which applies the <html> zoom
// synchronously. The App-mount tests assert the root composition: the HUD
// is mounted beside ExitConfirmDialog in the app's render branches.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { StrictMode, act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ReactNode } from 'react'

// Controllable stand-in for the wheel hook: the real hook is gated on the
// wails transport (attached-listener behavior covered by its own suite), so
// the component tests drive visibility directly.
const control = vi.hoisted(() => ({ visible: false }))
vi.mock('@/hooks/useCtrlWheelScale', () => ({
  useCtrlWheelScale: () => ({ visible: control.visible }),
}))

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's persist middleware captures at store-creation time (see
// uiScaleStore.test.ts). Installed before any store module import.
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() { return map.size },
  }
})

// --- App-mount tests: the harness from App.test.tsx, heavy deps stubbed ---
vi.mock('@/components/layout/AppLayout', () => ({ AppLayout: () => null }))
// Pass-through provider: in the main phase every root child (ScaleHud
// included) sits inside TooltipProvider, so stubbing it to null would drop
// them all — the other mocked children stay null, the provider itself must
// render its subtree for this composition test to see anything.
vi.mock('@/components/ui/tooltip', () => ({
  TooltipProvider: ({ children }: { children?: ReactNode }) => children,
}))
vi.mock('@/components/settings/SettingsModal', () => ({ SettingsModal: () => null }))
vi.mock('@/components/chat/WorkDirsModal', () => ({ WorkDirsModal: () => null }))
vi.mock('@/components/project/CreateProjectDialog', () => ({ CreateProjectDialog: () => null }))
vi.mock('@/components/ToolInstallSplash', () => ({ ToolInstallSplash: () => null }))
vi.mock('@/components/UpdateToast', () => ({ UpdateToast: () => null }))
vi.mock('@/components/GitConfigRiskToast', () => ({ GitConfigRiskToast: () => null }))
vi.mock('@/components/ExitConfirmDialog', () => ({ ExitConfirmDialog: () => null }))
vi.mock('@/components/research/ResearchEventBridge', () => ({ ResearchEventBridge: () => null }))
vi.mock('@/hooks/useUpdateChecker', () => ({ useUpdateChecker: () => {} }))
vi.mock('@/hooks/useExitGuard', () => ({ useExitGuard: () => {} }))
vi.mock('@/hooks/useGitFocusRefresh', () => ({ useGitFocusRefresh: () => {} }))
vi.mock('@/hooks/useGitFocusSync', () => ({ useGitFocusSync: () => {} }))
vi.mock('@/hooks/useWindowTitle', () => ({ useWindowTitle: () => {} }))
vi.mock('@/hooks/useProjectLoader', () => ({ useProjectLoader: () => {} }))
vi.mock('@/hooks/useSessionLoader', () => ({ useSessionLoader: () => {} }))
vi.mock('@/hooks/useSessionEvents', () => ({ useSessionEvents: () => {} }))
vi.mock('@/hooks/useBackgroundSessionWatcher', () => ({ useBackgroundSessionWatcher: () => {} }))
vi.mock('@/hooks/useShallowResync', () => ({ useShallowResync: () => {} }))
vi.mock('@/hooks/useNotificationClicks', () => ({ useNotificationClicks: () => {} }))
vi.mock('@/hooks/useAutonomyLoader', () => ({ useAutonomyLoader: () => {} }))
vi.mock('@/stores/activeSessionsStore', () => ({ useActiveSessionsRefresh: () => {} }))
vi.mock('@/lib/sound', () => ({ initSoundUnlock: () => {} }))
vi.mock('@/lib/systemNotifications', () => ({ initSystemNotifications: () => {} }))
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn(() => () => {}),
  onGlobalEvent: vi.fn(() => () => {}),
  isWailsReady: vi.fn(() => false),
  isWebTransport: vi.fn(() => false),
}))
vi.mock('@/api/projects', () => ({ listProjects: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/themes', () => ({ listThemes: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/vector', () => ({ getVectorIndexStatus: vi.fn().mockResolvedValue(null) }))
vi.mock('@/api/fonts', () => ({
  getSystemFonts: vi.fn().mockResolvedValue({ uiFamily: null, monoFamily: null }),
}))
vi.mock('@/stores/sessionStore', () => ({
  useSessionStore: (select: (s: { activeSessionId: string | null }) => unknown) =>
    select({ activeSessionId: null }),
}))
vi.mock('@/stores/projectStore', () => ({
  useProjectStore: (select: (s: Record<string, unknown>) => unknown) =>
    select({
      activeProjectId: null,
      createDialogOpen: false,
      setCreateProjectDialogOpen: () => {},
    }),
}))
vi.mock('@/stores/settingsStore', () => ({
  useSettingsStore: Object.assign(
    (select: (s: { open: boolean }) => unknown) => select({ open: false }),
    { getState: () => ({ open: false, openSettings: () => {} }) },
  ),
}))
vi.mock('@/stores/workDirsStore', () => ({
  useWorkDirsStore: Object.assign(() => null, { getState: () => ({ clear: () => {} }) }),
}))
vi.mock('@/stores/vectorIndexStore', () => ({
  useVectorIndexStore: Object.assign(() => null, { getState: () => ({ setStatus: () => {} }) }),
}))

import { ScaleHud } from './ScaleHud'
import App from '@/App'
import { useUiScaleStore } from '@/stores/uiScaleStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  // Reset the store to defaults between tests; nothing is persisted yet.
  useUiScaleStore.setState({ scale: 100 })
  localStorage.clear()
  document.documentElement.style.zoom = ''
  control.visible = false
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

function resetButton(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>('button[aria-label="Reset UI scale to 100%"]')
  expect(el).not.toBeNull()
  return el!
}

function scaleInput(): HTMLInputElement {
  const el = container.querySelector<HTMLInputElement>('input[aria-label="UI scale"]')
  expect(el).not.toBeNull()
  return el!
}

/** Set a controlled input's value the way a real browser does: native
 *  prototype setter + `input` event, so React's synthetic onChange fires. */
function setRangeValue(el: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  if (!setter) throw new Error('native input setter not found')
  act(() => {
    setter.call(el, value)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('ScaleHud visibility', () => {
  it('renders nothing while the hook reports the HUD hidden', () => {
    control.visible = false
    act(() => {
      root.render(<ScaleHud />)
    })
    expect(container.childElementCount).toBe(0)
    expect(container.textContent).toBe('')
  })
})

describe('ScaleHud visible', () => {
  beforeEach(() => {
    control.visible = true
  })

  it('renders the reset button, the slider with the 5% step grid, and the value label', () => {
    act(() => {
      root.render(<ScaleHud />)
    })
    resetButton()
    const input = scaleInput()
    expect(input.type).toBe('range')
    expect(input.min).toBe('50')
    expect(input.max).toBe('200')
    expect(input.step).toBe('5')
    expect(input.value).toBe('100')
    expect(container.textContent).toContain('100%')
  })

  it('the reset button restores 100 and applies the zoom immediately', () => {
    useUiScaleStore.setState({ scale: 150 })
    act(() => {
      root.render(<ScaleHud />)
    })
    act(() => {
      resetButton().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useUiScaleStore.getState().scale).toBe(100)
    expect(document.documentElement.style.zoom).toBe('1')
    expect(scaleInput().value).toBe('100')
    expect(container.textContent).toContain('100%')
  })

  it('the slider applies a stepped value live — store, zoom and label together', () => {
    act(() => {
      root.render(<ScaleHud />)
    })
    setRangeValue(scaleInput(), '125')
    expect(useUiScaleStore.getState().scale).toBe(125)
    expect(document.documentElement.style.zoom).toBe('1.25')
    expect(scaleInput().value).toBe('125')
    expect(container.textContent).toContain('125%')
  })

  it('the value label mirrors the store scale', () => {
    useUiScaleStore.setState({ scale: 175 })
    act(() => {
      root.render(<ScaleHud />)
    })
    expect(scaleInput().value).toBe('175')
    expect(container.textContent).toContain('175%')
  })

  it('the wrapper never blocks input; only the card re-enables pointer events', () => {
    act(() => {
      root.render(<ScaleHud />)
    })
    const wrapper = container.firstElementChild as HTMLElement
    expect(wrapper.className).toContain('fixed')
    expect(wrapper.className).toContain('inset-0')
    expect(wrapper.className).toContain('z-[100]')
    expect(wrapper.className).toContain('items-center')
    expect(wrapper.className).toContain('justify-center')
    expect(wrapper.className).toContain('pointer-events-none')
    const card = wrapper.firstElementChild as HTMLElement
    expect(card.className).toContain('pointer-events-auto')
  })
})

describe('App mounts ScaleHud', () => {
  async function mountApp(): Promise<() => Promise<void>> {
    const appContainer = document.createElement('div')
    document.body.appendChild(appContainer)
    const appRoot: Root = createRoot(appContainer)
    await act(async () => {
      appRoot.render(
        <StrictMode>
          <App />
        </StrictMode>,
      )
    })
    return async () => {
      await act(async () => {
        appRoot.unmount()
      })
      appContainer.remove()
    }
  }

  it('renders the HUD while the wheel gesture is active', async () => {
    control.visible = true
    const unmount = await mountApp()
    // The splash-phase branch carries the HUD just like the main one —
    // presence of the slider proves the root composition mounts ScaleHud.
    expect(document.querySelector('input[aria-label="UI scale"]')).not.toBeNull()
    await unmount()
  })

  it('leaves the HUD out of the DOM while hidden (steady state)', async () => {
    const unmount = await mountApp()
    expect(document.querySelector('input[aria-label="UI scale"]')).toBeNull()
    await unmount()
  })
})
