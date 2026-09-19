// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { gitMocks, subscribeMock } = vi.hoisted(() => ({
  gitMocks: {
    getIsGitRepo: vi.fn(),
  },
  subscribeMock: vi.fn((_name: string, _handler: () => void) => () => {}),
}))

vi.mock('@/api/git', () => ({ getIsGitRepo: gitMocks.getIsGitRepo }))
vi.mock('@/api/runtime', () => ({ subscribe: subscribeMock }))

import { useProjectGitRepo } from './useProjectGitRepo'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import type { ProjectInfo } from '@/types/models'

function makeProject(overrides: Partial<ProjectInfo> & { id: string }): ProjectInfo {
  return {
    name: overrides.id,
    workspace_path: `/ws/${overrides.id}`,
    is_external: false,
    is_no_project: false,
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

const P1 = makeProject({ id: 'p1' })
const P2 = makeProject({ id: 'p2' })
const NO_PROJECT = makeProject({ id: 'np', is_no_project: true })

let root: Root | null = null
let container: HTMLDivElement | null = null

/** Handlers captured from subscribe() calls, keyed by event name. */
const capturedHandlers = new Map<string, () => void>()
subscribeMock.mockImplementation((name: string, handler: () => void) => {
  capturedHandlers.set(name, handler)
  // A real unsubscribe removes the handler — mirror that so unmount
  // semantics are exercised faithfully.
  return () => {
    capturedHandlers.delete(name)
  }
})

function Harness(): null {
  useProjectGitRepo()
  return null
}

function renderHook(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(Harness))
  })
}

/** Let the fire-and-forget promise chain settle past the store writes. */
async function flushMicrotasks(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve()
  })
}

/** Switch the active project; the harness re-renders via the store. */
function setActiveProject(id: string): void {
  act(() => {
    useProjectStore.setState({ activeProjectId: id })
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  gitMocks.getIsGitRepo.mockReset()
  subscribeMock.mockClear()
  capturedHandlers.clear()
  useProjectStore.setState({
    projects: [P1, P2, NO_PROJECT],
    activeProjectId: null,
  })
  useGitPanelStore.getState().reset()
})

afterEach(() => {
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
  vi.useRealTimers()
})

describe('useProjectGitRepo', () => {
  it('checks the active project on mount and records the result with its id', async () => {
    gitMocks.getIsGitRepo.mockResolvedValue(true)
    setActiveProject('p1')

    renderHook()
    await flushMicrotasks()

    expect(gitMocks.getIsGitRepo).toHaveBeenCalledTimes(1)
    expect(useGitPanelStore.getState().isGitRepo).toBe(true)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBe('p1')
  })

  it('re-checks when the active project changes', async () => {
    gitMocks.getIsGitRepo.mockResolvedValue(false)
    setActiveProject('p1')

    renderHook()
    await flushMicrotasks()

    gitMocks.getIsGitRepo.mockResolvedValue(true)
    setActiveProject('p2')
    await flushMicrotasks()

    expect(gitMocks.getIsGitRepo).toHaveBeenCalledTimes(2)
    expect(useGitPanelStore.getState().isGitRepo).toBe(true)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBe('p2')
  })

  it('does not call the RPC for No Project and clears the pairing', async () => {
    setActiveProject('np')

    renderHook()
    await flushMicrotasks()

    expect(gitMocks.getIsGitRepo).not.toHaveBeenCalled()
    expect(useGitPanelStore.getState().isGitRepo).toBe(false)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBeNull()
  })

  it('does not call the RPC without an active project', async () => {
    renderHook()
    await flushMicrotasks()

    expect(gitMocks.getIsGitRepo).not.toHaveBeenCalled()
    expect(useGitPanelStore.getState().gitRepoProjectId).toBeNull()
  })

  it('fails closed on RPC rejection', async () => {
    gitMocks.getIsGitRepo.mockRejectedValue(new Error('no active project'))
    setActiveProject('p1')

    renderHook()
    await flushMicrotasks()

    expect(useGitPanelStore.getState().isGitRepo).toBe(false)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBe('p1')
  })

  it('drops a stale response when the project changed mid-flight', async () => {
    let resolve1!: (v: boolean) => void
    const deferred1 = new Promise<boolean>((resolve) => { resolve1 = resolve })
    gitMocks.getIsGitRepo.mockReturnValueOnce(deferred1).mockResolvedValue(false)
    setActiveProject('p1')

    renderHook()
    await flushMicrotasks()

    // Switch to p2 while p1's RPC is still pending; p2 resolves false.
    setActiveProject('p2')
    await flushMicrotasks()
    expect(useGitPanelStore.getState().gitRepoProjectId).toBe('p2')
    expect(useGitPanelStore.getState().isGitRepo).toBe(false)

    // The late p1 answer (true) must NOT overwrite p2's state.
    await act(async () => {
      resolve1(true)
    })
    await flushMicrotasks()

    expect(useGitPanelStore.getState().isGitRepo).toBe(false)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBe('p2')
  })

  it('re-checks (debounced) on workspace:tree_changed — a late git init flips the layout', async () => {
    gitMocks.getIsGitRepo.mockResolvedValue(false)
    setActiveProject('p1')

    renderHook()
    await flushMicrotasks()
    expect(useGitPanelStore.getState().isGitRepo).toBe(false)

    gitMocks.getIsGitRepo.mockResolvedValue(true)
    await act(async () => {
      capturedHandlers.get('workspace:tree_changed')?.()
      await vi.advanceTimersByTimeAsync(500)
    })
    await flushMicrotasks()

    expect(gitMocks.getIsGitRepo).toHaveBeenCalledTimes(2)
    expect(useGitPanelStore.getState().isGitRepo).toBe(true)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBe('p1')
  })

  it('unsubscribes from workspace:tree_changed on unmount', async () => {
    gitMocks.getIsGitRepo.mockResolvedValue(true)
    setActiveProject('p1')

    renderHook()
    await flushMicrotasks()

    const r = root
    root = null
    act(() => {
      r?.unmount()
    })

    gitMocks.getIsGitRepo.mockClear()
    await act(async () => {
      capturedHandlers.get('workspace:tree_changed')?.()
      await vi.advanceTimersByTimeAsync(500)
    })
    await flushMicrotasks()

    expect(gitMocks.getIsGitRepo).not.toHaveBeenCalled()
  })
})
