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
// the component tests drive visibility directly and spy the card-grab
// controls the component wires onto its pointer handlers.
const control = vi.hoisted(() => ({ visible: false, hold: vi.fn(), release: vi.fn() }))
vi.mock('@/hooks/useCtrlWheelScale', () => ({
  useCtrlWheelScale: () => ({ visible: control.visible, hold: control.hold, release: control.release }),
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
  // jsdom also lacks ResizeObserver, which Radix slider's use-size hook
  // requires on mount (the HUD now renders the Radix-based slider
  // primitive). A record-only stub is enough: nothing resizes here.
  ;(win as Record<string, unknown>).ResizeObserver = class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
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
  control.hold.mockClear()
  control.release.mockClear()
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

/** The HUD slider is the self-styled Radix primitive: the focusable
 *  role=slider element is its thumb, the value lives in aria-valuenow and
 *  its accessible name + % unit in aria-label/aria-valuetext. */
function scaleSlider(): HTMLElement {
  const el = container.querySelector<HTMLElement>('[role="slider"]')
  expect(el).not.toBeNull()
  return el!
}

/** Step the slider one grid notch with the keyboard the way a browser
 *  delivers it: focus the thumb, dispatch a keydown on it. Radix handles
 *  the step arithmetic (SLIDER_STEP_PERCENT grid) internally. */
function stepSlider(key: string): void {
  const thumb = scaleSlider()
  act(() => {
    thumb.focus()
  })
  act(() => {
    thumb.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
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
    const slider = scaleSlider()
    expect(slider.getAttribute('aria-valuemin')).toBe('50')
    expect(slider.getAttribute('aria-valuemax')).toBe('200')
    expect(slider.getAttribute('aria-valuenow')).toBe('100')
    // The announced element is the thumb: name + % unit live on it (the
    // visible label span is aria-hidden, so the units exist only here).
    expect(slider.getAttribute('aria-label')).toBe('UI scale')
    expect(slider.getAttribute('aria-valuetext')).toBe('100%')
    // No native range input remains — the point of the change: the visual
    // is ours (design tokens), not the platform's.
    expect(container.querySelector('input[type="range"]')).toBeNull()
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
    expect(scaleSlider().getAttribute('aria-valuenow')).toBe('100')
    expect(container.textContent).toContain('100%')
  })

  it('the slider applies a stepped value live — store, zoom and label together', () => {
    act(() => {
      root.render(<ScaleHud />)
    })
    // One ArrowRight notch: 100 → 105 on the 5% grid; Radix calls
    // onValueChange → setScale, which applies the <html> zoom synchronously.
    stepSlider('ArrowRight')
    expect(useUiScaleStore.getState().scale).toBe(105)
    expect(document.documentElement.style.zoom).toBe('1.05')
    expect(scaleSlider().getAttribute('aria-valuenow')).toBe('105')
    expect(scaleSlider().getAttribute('aria-valuetext')).toBe('105%')
    expect(container.textContent).toContain('105%')
  })

  it('the value label mirrors the store scale', () => {
    useUiScaleStore.setState({ scale: 175 })
    act(() => {
      root.render(<ScaleHud />)
    })
    expect(scaleSlider().getAttribute('aria-valuenow')).toBe('175')
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

  it('grabbing the card holds the hide; pointerup/pointercancel release it', () => {
    act(() => {
      root.render(<ScaleHud />)
    })
    const card = container.querySelector<HTMLElement>('.pointer-events-auto')
    expect(card).not.toBeNull()

    act(() => {
      card!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    })
    expect(control.hold).toHaveBeenCalledTimes(1)
    expect(control.release).not.toHaveBeenCalled()

    act(() => {
      card!.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    })
    expect(control.release).toHaveBeenCalledTimes(1)

    act(() => {
      card!.dispatchEvent(new MouseEvent('pointercancel', { bubbles: true }))
    })
    expect(control.release).toHaveBeenCalledTimes(2)
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
    expect(document.querySelector('[role="slider"]')).not.toBeNull()
    await unmount()
  })

  it('leaves the HUD out of the DOM while hidden (steady state)', async () => {
    const unmount = await mountApp()
    expect(document.querySelector('[role="slider"]')).toBeNull()
    await unmount()
  })
})
