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

// --- child components: stubbed (never rendered in the splash phase anyway) ---
vi.mock('@/components/layout/AppLayout', () => ({ AppLayout: () => null }))
vi.mock('@/components/ui/tooltip', () => ({ TooltipProvider: () => null }))
vi.mock('@/components/settings/SettingsModal', () => ({ SettingsModal: () => null }))
vi.mock('@/components/chat/WorkDirsModal', () => ({ WorkDirsModal: () => null }))
vi.mock('@/components/project/CreateProjectDialog', () => ({ CreateProjectDialog: () => null }))
vi.mock('@/components/ToolInstallSplash', () => ({ ToolInstallSplash: () => null }))
vi.mock('@/components/UpdateToast', () => ({ UpdateToast: () => null }))
vi.mock('@/components/GitConfigRiskToast', () => ({ GitConfigRiskToast: () => null }))
vi.mock('@/components/ExitConfirmDialog', () => ({ ExitConfirmDialog: () => null }))
vi.mock('@/components/research/ResearchEventBridge', () => ({ ResearchEventBridge: () => null }))

// --- lifecycle hooks: pure side-effect hosts, stubbed to no-ops ---
vi.mock('@/hooks/useUpdateChecker', () => ({ useUpdateChecker: () => {} }))
vi.mock('@/hooks/useExitGuard', () => ({ useExitGuard: () => {} }))
vi.mock('@/hooks/useGitFocusRefresh', () => ({ useGitFocusRefresh: () => {} }))
vi.mock('@/hooks/useWindowTitle', () => ({ useWindowTitle: () => {} }))
vi.mock('@/hooks/useProjectLoader', () => ({ useProjectLoader: () => {} }))
vi.mock('@/hooks/useSessionLoader', () => ({ useSessionLoader: () => {} }))
vi.mock('@/hooks/useSessionEvents', () => ({ useSessionEvents: () => {} }))
vi.mock('@/hooks/useBackgroundSessionWatcher', () => ({ useBackgroundSessionWatcher: () => {} }))
vi.mock('@/stores/activeSessionsStore', () => ({ useActiveSessionsRefresh: () => {} }))

// --- api ---
// onGlobalEvent backs api/notifications' notification_clicked subscription
// (mounted by useNotificationClicks in App); isWailsReady gates the
// notification init in lib/systemNotifications. Both must no-op here —
// this suite tests the audio-unlock wiring only.
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn(() => () => {}),
  onGlobalEvent: vi.fn(() => () => {}),
  isWailsReady: vi.fn(() => false),
}))
vi.mock('@/api/projects', () => ({ listProjects: vi.fn().mockResolvedValue([]) }))

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
