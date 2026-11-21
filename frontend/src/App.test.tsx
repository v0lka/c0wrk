// @vitest-environment jsdom
//
// App root — audio-unlock wiring.
//
// The persistent gesture/visibility listeners that revive a suspended
// AudioContext must be installed once at app start, independent of whether a
// session is active (initSoundUnlock lives in an App mount effect, not in
// useSoundEvents). This test mounts the real App with every heavy dependency
// stubbed, inside StrictMode exactly as main.tsx does, and asserts the real
// initSoundUnlock attached exactly one listener per event type — i.e. it ran on
// startup with activeSessionId === null, and StrictMode's double mount did not
// duplicate the listeners.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { StrictMode, act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Mutable session id so a test can mount the App with no active session.
const control = vi.hoisted(() => ({ sessionId: null as string | null }))

// Captured subscribe handlers: the splash-recovery tests drive the app
// lifecycle events (backend:ready) through the same mock to run App's phase
// machine exactly as the backend would (the ScaleHud.test.tsx pattern).
const runtimeEvents = vi.hoisted(() => {
  const handlers = new Map<string, Set<(data: unknown) => void>>()
  return {
    subscribe(event: string, handler: (data: unknown) => void): () => void {
      let bucket = handlers.get(event)
      if (bucket === undefined) {
        bucket = new Set()
        handlers.set(event, bucket)
      }
      bucket.add(handler)
      return () => {
        bucket.delete(handler)
      }
    },
    emit(event: string, data?: unknown): void {
      handlers.get(event)?.forEach((handler) => handler(data))
    },
    reset(): void {
      handlers.clear()
    },
  }
})

// The mount-time GetToolInstallStatus snapshot (lost-start recovery): each
// test plants the DTO the RPC should return; null is the inactive steady
// state the wrapper resolves for every failure mode.
const toolInstallStatus = vi.hoisted(() => ({
  snapshot: null as null | {
    active: boolean
    pending: readonly { name: string; version: string }[]
    tools_total: number
  },
}))

// ToolInstallSplash probe: the splash phase renders this component with the
// recovered tool list, so the tests capture its props and leave a DOM marker
// (the real component's rendering is covered by ToolInstallSplash.test.tsx).
const splashProbe = vi.hoisted(() => ({
  renders: [] as Array<{ tools: readonly unknown[]; toolsTotal: number | undefined }>,
}))

// --- child components: stubbed (never rendered in the splash phase anyway) ---
vi.mock('@/components/layout/AppLayout', () => ({ AppLayout: () => null }))
vi.mock('@/components/ui/tooltip', () => ({ TooltipProvider: () => null }))
vi.mock('@/components/settings/SettingsModal', () => ({ SettingsModal: () => null }))
vi.mock('@/components/chat/WorkDirsModal', () => ({ WorkDirsModal: () => null }))
vi.mock('@/components/project/CreateProjectDialog', () => ({ CreateProjectDialog: () => null }))
vi.mock('@/components/ToolInstallSplash', async () => {
  const React = await import('react')
  return {
    ToolInstallSplash: (props: { tools: readonly unknown[]; toolsTotal?: number }) => {
      splashProbe.renders.push({ tools: props.tools, toolsTotal: props.toolsTotal })
      return React.createElement('div', { 'data-testid': 'tool-install-splash' })
    },
  }
})
vi.mock('@/components/UpdateToast', () => ({ UpdateToast: () => null }))
vi.mock('@/components/GitConfigRiskToast', () => ({ GitConfigRiskToast: () => null }))
vi.mock('@/components/ExitConfirmDialog', () => ({ ExitConfirmDialog: () => null }))
vi.mock('@/components/research/ResearchEventBridge', () => ({ ResearchEventBridge: () => null }))

// --- lifecycle hooks: pure side-effect hosts, stubbed to no-ops ---
vi.mock('@/hooks/useUpdateChecker', () => ({ useUpdateChecker: () => {} }))
vi.mock('@/hooks/useExitGuard', () => ({ useExitGuard: () => {} }))
vi.mock('@/hooks/useGitFocusRefresh', () => ({ useGitFocusRefresh: () => {} }))
// Mounted at the App root (beside useVectorIndexStatus); its own behavior is
// covered by hooks/useGitFocusSync.test.tsx. Stubbed here so the App mount
// does not pull @/lib/gitFocus (→ api/git + api/workspace → the wails
// bridge) into this audio-unlock-only suite.
vi.mock('@/hooks/useGitFocusSync', () => ({ useGitFocusSync: () => {} }))
vi.mock('@/hooks/useWindowTitle', () => ({ useWindowTitle: () => {} }))
vi.mock('@/hooks/useProjectLoader', () => ({ useProjectLoader: () => {} }))
vi.mock('@/hooks/useSessionLoader', () => ({ useSessionLoader: () => {} }))
vi.mock('@/hooks/useSessionEvents', () => ({ useSessionEvents: () => {} }))
vi.mock('@/hooks/useBackgroundSessionWatcher', () => ({ useBackgroundSessionWatcher: () => {} }))
// Mounted beside useBackgroundSessionWatcher at the App root; its own
// behavior is covered by hooks/useShallowResync.test.ts. Stubbed here so the
// App mount does not pull the resync loaders (lib/sessionHistoryReload →
// api/chat → the wails bridge) into this audio-unlock-only suite.
vi.mock('@/hooks/useShallowResync', () => ({ useShallowResync: () => {} }))
vi.mock('@/stores/activeSessionsStore', () => ({ useActiveSessionsRefresh: () => {} }))
vi.mock('@/hooks/useAutonomyLoader', () => ({ useAutonomyLoader: () => {} }))

// --- api ---
// onGlobalEvent backs api/notifications' notification_clicked subscription
// (mounted by useNotificationClicks in App); isWailsReady gates the
// notification init in lib/systemNotifications. Both must no-op here —
// this suite tests the audio-unlock wiring only.
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn(runtimeEvents.subscribe),
  onGlobalEvent: vi.fn(() => () => {}),
  isWailsReady: vi.fn(() => false),
  // lib/systemNotifications' transport guard reads this; the real lib runs
  // here (only its init is fire-and-forget), so the export must exist.
  isWebTransport: vi.fn(() => false),
  // ScaleHud (mounted at the App root in every phase) runs useCtrlWheelScale,
  // whose effect gates on the transport snapshot — 'none' keeps its listeners
  // detached in this jsdom suite exactly as in a non-wails environment.
  transportSnapshot: vi.fn(() => 'none'),
}))
vi.mock('@/api/projects', () => ({ listProjects: vi.fn().mockResolvedValue([]) }))
// The App mount also fires the tool-install snapshot RPC (the lost-start
// recovery); each splash-recovery test plants the snapshot to return.
vi.mock('@/api/toolmanager', () => ({
  getToolInstallStatus: vi.fn(() => Promise.resolve(toolInstallStatus.snapshot)),
}))
// The App mount also fires two more RPC paths that must stay quiet here:
// the theme-catalog refresh (App → themeStore.loadThemes → listThemes) and
// the vector-index status seed (App → useVectorIndexStatus). Both go through
// getApp() from '@/api/runtime', which the mock above does not define — the
// resulting "[vitest] No 'getApp' export" errors are pure output noise for a
// test about audio-unlock wiring, so both API modules are stubbed instead.
vi.mock('@/api/themes', () => ({ listThemes: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/vector', () => ({ getVectorIndexStatus: vi.fn().mockResolvedValue(null) }))
// The system-font fetch (App → useSystemFonts → getSystemFonts) is the same
// kind of quiet RPC path: null families = "no system font detected", the
// normal not-an-error outcome that keeps the suite's output clean.
vi.mock('@/api/fonts', () => ({
  getSystemFonts: vi.fn().mockResolvedValue({ uiFamily: null, monoFamily: null }),
}))

// --- stores: direct-field selector mocks, no session active ---
vi.mock('@/stores/sessionStore', () => ({
  useSessionStore: (select: (s: { activeSessionId: string | null }) => unknown) =>
    select({ activeSessionId: control.sessionId }),
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

import App from './App'
import { listProjects } from '@/api/projects'
import { __resetSoundModule } from '@/lib/sound'

const WINDOW_TYPES = ['pointerdown', 'keydown', 'touchstart'] as const

function countFor(spy: { mock: { calls: unknown[][] } }, type: string): number {
  return spy.mock.calls.filter((call) => call[0] === type).length
}

async function mountApp(sessionId: string | null): Promise<() => Promise<void>> {
  control.sessionId = sessionId
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root: Root = createRoot(container)
  await act(async () => {
    root.render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
  })
  return async () => {
    await act(async () => {
      root.unmount()
    })
    container.remove()
  }
}

describe('App — audio unlock wiring', () => {
  beforeEach(() => {
    __resetSoundModule()
    control.sessionId = null
  })

  afterEach(() => {
    vi.restoreAllMocks()
    __resetSoundModule()
    document.body.innerHTML = ''
  })

  it('installs exactly one of each unlock listener at startup with no active session', async () => {
    const windowAdd = vi.spyOn(window, 'addEventListener')
    const documentAdd = vi.spyOn(document, 'addEventListener')

    const unmount = await mountApp(null)

    for (const type of WINDOW_TYPES) {
      expect(countFor(windowAdd, type)).toBe(1)
    }
    expect(countFor(documentAdd, 'visibilitychange')).toBe(1)

    await unmount()
  })
})

describe('App — tool-install splash recovery (GetToolInstallStatus snapshot)', () => {
  beforeEach(() => {
    __resetSoundModule()
    control.sessionId = null
    toolInstallStatus.snapshot = null
    splashProbe.renders = []
    runtimeEvents.reset()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    __resetSoundModule()
    document.body.innerHTML = ''
  })

  it('renders the full splash immediately on mount when the snapshot reports an active install (lost tool_manager:start)', async () => {
    toolInstallStatus.snapshot = {
      active: true,
      pending: [{ name: 'rg', version: '15.1.0' }, { name: 'uv', version: '0.9.8' }],
      tools_total: 2,
    }

    const unmount = await mountApp(null)

    // No tool_manager event was emitted at all — the mount-time snapshot
    // alone recovered the splash with the FULL pending list (real version
    // strings) and the splash denominator, without waiting for progress.
    expect(splashProbe.renders.length).toBeGreaterThan(0)
    const last = splashProbe.renders[splashProbe.renders.length - 1]
    expect(last?.tools).toEqual([{ name: 'rg', version: '15.1.0' }, { name: 'uv', version: '0.9.8' }])
    expect(last?.toolsTotal).toBe(2)
    // Still the splash-with-tools phase after every mount RPC settled: the
    // listProjects safety net must NOT bounce an active install to main,
    // because the snapshot populated splashTools (the !== null guard).
    expect(document.querySelector('[data-testid="tool-install-splash"]')).not.toBeNull()
    expect(document.querySelector('.animate-spin')).toBeNull()

    await unmount()
  })

  it('keeps the default flow unchanged when the snapshot is inactive (spinner → backend:ready → main)', async () => {
    // Defer the listProjects safety net so the mount flush cannot already
    // have exited the splash: with an inactive snapshot the minimal-spinner
    // phase must stay exactly as before the snapshot RPC existed, until a
    // real exit (backend:ready) arrives.
    let resolveProjects!: () => void
    const pendingProjects = new Promise<void>((resolve) => { resolveProjects = resolve })
    // ALL calls (not Once): StrictMode's double-mounted effect invokes
    // listProjects twice, and a once-implementation would leave the second
    // mount on the default resolved mock — a premature main transition.
    vi.mocked(listProjects).mockImplementation(
      () => pendingProjects as unknown as ReturnType<typeof listProjects>,
    )

    const unmount = await mountApp(null)

    // Inactive snapshot is a no-op: still the minimal spinner, no splash.
    expect(splashProbe.renders).toHaveLength(0)
    expect(document.querySelector('[data-testid="tool-install-splash"]')).toBeNull()
    expect(document.querySelector('.animate-spin')).not.toBeNull()

    await act(async () => {
      runtimeEvents.emit('backend:ready')
    })

    // Main phase via the unchanged backend:ready path: spinner gone, no splash.
    expect(document.querySelector('.animate-spin')).toBeNull()
    expect(document.querySelector('[data-testid="tool-install-splash"]')).toBeNull()
    expect(splashProbe.renders).toHaveLength(0)

    // Settle the deferred safety net inside act (its callback sets state):
    // by now phase is main, so the guard's transition is a no-op.
    await act(async () => {
      resolveProjects()
    })

    await unmount()
  })
})
