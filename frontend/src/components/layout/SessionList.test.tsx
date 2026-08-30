// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'

// --- Mock the session API wrapper so tests never touch the Wails backend ---
const { apiMocks } = vi.hoisted(() => ({
  apiMocks: {
    createSession: vi.fn(),
    deleteSession: vi.fn(),
    archiveSession: vi.fn(),
    renameSession: vi.fn(),
    pinSession: vi.fn(),
    forkSession: vi.fn(),
  },
}))
vi.mock('@/api/sessions', () => apiMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

// --- Mock the session store with a controllable state handle ---
type SessionRow = {
  id: string
  project_id: string
  name: string
  archived: boolean
  pinned: boolean
  last_active_at: string
  has_unfinished_task: boolean
}
const { sessionMocks } = vi.hoisted(() => ({
  sessionMocks: {
    sessions: [] as SessionRow[],
    activeSessionId: null as string | null,
    setActiveSessionId: vi.fn(),
    selectSession: vi.fn(),
    addSession: vi.fn(),
    removeSession: vi.fn(),
    updateSession: vi.fn(),
  },
}))
vi.mock('@/stores/sessionStore', () => ({
  useSessionStore: (selector: (s: typeof sessionMocks) => unknown) => selector(sessionMocks),
}))

// The session item reads live task state via the chat store + status indicator.
// Provide a no-op chat store so the indicator resolves to 'idle'.
vi.mock('@/stores/chatStore', () => ({
  useChatStore: () => 'idle',
}))

import { SessionList } from './SessionList'

function makeSession(overrides: Partial<SessionRow> = {}): SessionRow {
  return {
    id: 's1',
    project_id: 'proj-1',
    name: 'Session One',
    archived: false,
    pinned: false,
    last_active_at: new Date(Date.now() - 60_000).toISOString(),
    has_unfinished_task: false,
    ...overrides,
  }
}

function render(ui: React.ReactNode): { root: Root; container: HTMLElement } {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => {
    root.render(<TooltipProvider>{ui}</TooltipProvider>)
  })
  return { root, container }
}

beforeEach(() => {
  vi.clearAllMocks()
  sessionMocks.sessions = []
  sessionMocks.activeSessionId = null
})

describe('SessionList', () => {
  it('renders sessions from the store as flat rows', () => {
    sessionMocks.sessions = [
      makeSession({ id: 's1', name: 'Alpha' }),
      makeSession({ id: 's2', name: 'Beta' }),
    ]
    const { container } = render(<SessionList />)

    const rows = container.querySelectorAll('button[aria-current], button:not([title])')
    const names = Array.from(container.querySelectorAll('span'))
      .map((s) => s.textContent ?? '')
      .filter((t) => t === 'Alpha' || t === 'Beta')
    expect(names).toContain('Alpha')
    expect(names).toContain('Beta')
    expect(rows.length).toBeGreaterThan(0)
  })

  it('the "New" header button calls createSession', async () => {
    apiMocks.createSession.mockResolvedValue(makeSession({ id: 'new', name: 'New' }))
    const { container } = render(<SessionList />)

    const newBtn = Array.from(container.querySelectorAll('button')).find(
      (b) => (b.textContent ?? '').toLowerCase().includes('new'),
    ) as HTMLButtonElement | undefined
    expect(newBtn).toBeTruthy()

    await act(async () => {
      newBtn!.click()
    })
    expect(apiMocks.createSession).toHaveBeenCalledTimes(1)
    expect(sessionMocks.addSession).toHaveBeenCalledTimes(1)
    // A created session becomes the visible active one — an explicit
    // activation persisted under its owning project (see selectSession).
    expect(sessionMocks.selectSession).toHaveBeenCalledWith('new', 'proj-1')
  })

  it('does not render the search box until there are ≥ 5 sessions', () => {
    sessionMocks.sessions = [makeSession({ id: 's1' })]
    const { container } = render(<SessionList />)
    const search = container.querySelector('input[placeholder*="Search"]')
    expect(search).toBeNull()
  })

  it('renders the search box once there are ≥ 5 sessions', () => {
    sessionMocks.sessions = Array.from({ length: 5 }, (_, i) =>
      makeSession({ id: `s${i}`, name: `Session ${i}` }),
    )
    const { container } = render(<SessionList />)
    const search = container.querySelector('input[placeholder*="Search"]')
    expect(search).not.toBeNull()
  })

  it('marks the active session with aria-current', () => {
    sessionMocks.sessions = [
      makeSession({ id: 's1', name: 'Active' }),
      makeSession({ id: 's2', name: 'Other' }),
    ]
    sessionMocks.activeSessionId = 's1'
    const { container } = render(<SessionList />)
    const active = container.querySelector('[aria-current="true"]')
    expect(active).toBeTruthy()
    expect(active?.textContent ?? '').toContain('Active')
  })
})
