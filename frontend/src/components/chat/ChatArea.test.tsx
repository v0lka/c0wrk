// @vitest-environment jsdom
//
// Integration test for the archived-session input swap in ChatArea.
//
// ChatArea must render <ArchivedBanner/> instead of <ChatInput/> when the
// active session's `archived` flag is true, and flip back when it is cleared.
// This is the read-only invariant the ArchivedBanner feature exists to enforce
// at the UI layer (the backend guard in backend/session is the server-side
// backstop). We keep the real ArchivedBanner and stub the heavy siblings so the
// assertion exercises ChatArea's own conditional + the real banner wiring.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ReactElement } from 'react'

// Enable React's act() flushing in this jsdom environment.

import type { SessionInfo } from '@/types/models'

// --- sessionStore: a REAL zustand store so setState triggers re-renders ---
// (lets the live-toggle assertion flip the surface without a fresh mount).
vi.mock('@/stores/sessionStore', async () => {
  const { create } = await import('zustand')
  return {
    useSessionStore: create<{
      sessions: SessionInfo[] | null
      activeSessionId: string | null
      updateSession: (id: string, updates: Partial<SessionInfo>) => void
    }>((set) => ({
      sessions: null,
      activeSessionId: null,
      updateSession: (id, updates) =>
        set((s) => ({
          sessions: (s.sessions ?? []).map((x) => (x.id === id ? { ...x, ...updates } : x)),
        })),
    })),
  }
})

// --- chatStore: ChatArea only reads streamingText (undefined) + messages ([]) ---
vi.mock('@/stores/chatStore', () => ({
  useChatStore: Object.assign(
    () => undefined,
    { getState: () => ({ addMessage: vi.fn(), mergeHistoryMessages: vi.fn(), setTaskActive: vi.fn() }) },
  ),
  useSessionMessages: () => [],
}))

// --- planStore: ChatArea calls usePlanStore.getState().clearPlan() in an effect ---
vi.mock('@/stores/planStore', () => ({
  usePlanStore: Object.assign(() => null, { getState: () => ({ clearPlan: vi.fn() }) }),
}))

// --- api/chat: return empty/null so the history/reconcile effects are no-ops ---
vi.mock('@/api/chat', () => ({
  getSessionHistory: vi.fn().mockResolvedValue([]),
  getSessionRuntimeStatus: vi.fn().mockResolvedValue(null),
  getPendingActions: vi.fn().mockResolvedValue(null),
  resolveStalePrompt: vi.fn().mockResolvedValue(undefined),
}))

// --- ArchivedBanner deps (kept real, so its imports must resolve to stubs) ---
vi.mock('@/api/sessions', () => ({ archiveSession: vi.fn().mockResolvedValue(undefined) }))
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn(), debug: vi.fn() } }))

// --- Heavy siblings rendered alongside the input shell: stubbed to null ---
vi.mock('./ChatInput', () => ({ ChatInput: () => <div data-testid="chat-input-stub" /> }))
vi.mock('./ExecutionPanels', () => ({ ExecutionPanels: () => null }))
vi.mock('./BlackboardPanel', () => ({ BlackboardPanel: () => null }))

import { ChatArea } from './ChatArea'
import { useSessionStore } from '@/stores/sessionStore'

function session(over: Partial<SessionInfo>): SessionInfo {
  return {
    id: 's1',
    project_id: '',
    name: 'Session',
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: false,
    unfinished_task_status: '',
    ...over,
  }
}

let root: Root | null = null

function render(el: ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
  return container
}

async function flushEffects() {
  await act(async () => {
    // Let the history/reconcile effects (mocked async) settle.
    await Promise.resolve()
    await Promise.resolve()
  })
}

describe('ChatArea archived-session input swap', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    useSessionStore.setState({ sessions: null, activeSessionId: null })
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('renders ChatInput when the active session is not archived', async () => {
    useSessionStore.setState({ sessions: [session({ archived: false })], activeSessionId: 's1' })
    const container = render(<ChatArea />)
    await flushEffects()

    expect(container.querySelector('[data-testid="chat-input-stub"]')).not.toBeNull()
    // The archived banner text must NOT be present.
    expect(container.textContent).not.toContain('archived')
  })

  it('renders ArchivedBanner (and hides ChatInput) when the active session is archived', async () => {
    useSessionStore.setState({ sessions: [session({ archived: true })], activeSessionId: 's1' })
    const container = render(<ChatArea />)
    await flushEffects()

    // Chat input shell is replaced by the banner.
    expect(container.querySelector('[data-testid="chat-input-stub"]')).toBeNull()
    expect(container.textContent).toContain('archived')
    // The real ArchivedBanner offers a Restore affordance.
    expect(container.querySelector('button')?.textContent).toContain('Restore')
  })

  it('flips the surface when the archived flag toggles on the open session', async () => {
    // Start unarchived → ChatInput is shown.
    useSessionStore.setState({ sessions: [session({ archived: false })], activeSessionId: 's1' })
    const container = render(<ChatArea />)
    await flushEffects()
    expect(container.querySelector('[data-testid="chat-input-stub"]')).not.toBeNull()
    expect(container.textContent).not.toContain('archived')

    // Archive the currently-open session (e.g. via the sidebar) → banner appears.
    act(() => {
      useSessionStore.getState().updateSession('s1', { archived: true })
    })
    await flushEffects()
    expect(container.querySelector('[data-testid="chat-input-stub"]')).toBeNull()
    expect(container.textContent).toContain('archived')

    // Restore it → ChatInput returns.
    act(() => {
      useSessionStore.getState().updateSession('s1', { archived: false })
    })
    await flushEffects()
    expect(container.querySelector('[data-testid="chat-input-stub"]')).not.toBeNull()
    expect(container.textContent).not.toContain('archived')
  })
})
