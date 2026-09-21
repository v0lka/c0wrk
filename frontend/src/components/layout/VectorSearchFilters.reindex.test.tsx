// @vitest-environment jsdom
//
// Tests for the force-full-reindex action hosted on the VectorSearchFilters
// mode-selector row (after the hybrid/vector/lexical buttons). The action was
// moved here from the FileTreePanel explorer header; the behavior is
// unchanged: fire-and-forget RPC, optimistic latch against duplicate clicks
// until the first vector_index:status event, spinning/disabled while busy, and
// hidden (not merely disabled) when no reindexable project is active.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The panel must never reach the Wails backend: mock the reindex action under
// test and the runtime event subscription (VectorSearchFilters subscribes to
// vector_index:status only while a reindex request is pending).
vi.mock('@/api/vector', () => ({
  reindexVectorIndex: vi.fn(async () => undefined),
}))
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn((event: string, cb: () => void) => {
    let list = runtimeSubs.get(event)
    if (!list) {
      list = []
      runtimeSubs.set(event, list)
    }
    list.push(cb)
    return () => {
      const current = runtimeSubs.get(event)
      if (current) current.splice(current.indexOf(cb), 1)
    }
  }),
}))
const runtimeSubs = vi.hoisted(() => new Map<string, Array<() => void>>())

/** Emit a `vector_index:status` event to every current subscriber. */
function emitVectorIndexStatus(): void {
  for (const cb of [...(runtimeSubs.get('vector_index:status') ?? [])]) cb()
}

import { VectorSearchFilters } from './VectorSearchFilters'
import { reindexVectorIndex } from '@/api/vector'
import { useProjectStore } from '@/stores/projectStore'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import type { ProjectInfo, VectorIndexStatus } from '@/types/models'

const reindexMock = vi.mocked(reindexVectorIndex)

function makeProject(overrides: Partial<ProjectInfo> = {}): ProjectInfo {
  return {
    id: 'p1',
    name: 'rootfs',
    workspace_path: '/ws',
    is_external: true,
    is_no_project: false,
    created_at: new Date(0).toISOString(),
    last_active_at: new Date(0).toISOString(),
    ...overrides,
  }
}

function setIndexState(state: VectorIndexStatus['state']): void {
  useVectorIndexStore.setState((s) => ({ status: { ...s.status, state } }))
}

function findButton(container: HTMLElement, title: string): HTMLButtonElement | null {
  return container.querySelector<HTMLButtonElement>(`button[title="${title}"]`)
}

describe('VectorSearchFilters — force full project reindex action', () => {
  let container: HTMLDivElement
  let root: Root | null = null
  let onSearch: ReturnType<typeof vi.fn>
  let onClear: ReturnType<typeof vi.fn>
  let onKeyDown: ReturnType<typeof vi.fn>

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    useProjectStore.setState({
      projects: [makeProject()],
      activeProjectId: 'p1',
    })
    act(() => {
      useVectorIndexStore.getState().reset()
    })
    setIndexState('ready')
    onSearch = vi.fn()
    onClear = vi.fn()
    onKeyDown = vi.fn()
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
    container.remove()
    runtimeSubs.clear()
    vi.clearAllMocks()
  })

  async function renderFilters(): Promise<void> {
    root = createRoot(container)
    await act(async () => {
      root!.render(
        <VectorSearchFilters
          isSearchMode={false}
          onSearch={onSearch as () => void}
          onClear={onClear as () => void}
          onKeyDown={onKeyDown as (e: React.KeyboardEvent) => void}
        />,
      )
    })
    await act(async () => {})
  }

  it('renders the action on the mode-selector row, after the mode buttons, and triggers a full reindex on click', async () => {
    await renderFilters()

    const button = findButton(container, 'Force full project reindex')
    expect(button).not.toBeNull()
    expect(button!.disabled).toBe(false)

    // Placement contract: the reindex button comes after the three mode
    // buttons (hybrid, vector, lexical) in DOM order inside the same row.
    const row = button!.closest('div')
    expect(row).not.toBeNull()
    const buttons = Array.from(row!.querySelectorAll('button'))
    expect(buttons.map((b) => b.textContent)).toEqual(['hybrid', 'vector', 'lexical', ''])
    expect(buttons[buttons.length - 1]).toBe(button)

    await act(async () => {
      button!.click()
    })

    expect(reindexMock).toHaveBeenCalledTimes(1)
  })

  it('is disabled and spinning while a reindex is already in progress', async () => {
    setIndexState('reindexing')
    await renderFilters()

    const button = findButton(container, 'Reindexing...')
    expect(button).not.toBeNull()
    expect(button!.disabled).toBe(true)
  })

  it('is hidden (not merely disabled) in No Project (CHAT) mode', async () => {
    useProjectStore.setState({
      projects: [makeProject({ id: 'np', is_no_project: true, workspace_path: '/np' })],
      activeProjectId: 'np',
    })
    await renderFilters()

    // The reindex affordance is not rendered at all in CHAT mode — the vector
    // index is disabled there, so a disabled button would be misleading.
    expect(findButton(container, 'Reindex unavailable')).toBeNull()
    expect(findButton(container, 'Force full project reindex')).toBeNull()
    expect(reindexMock).not.toHaveBeenCalled()
  })

  it('blocks duplicate clicks until the vector_index:status event arrives', async () => {
    await renderFilters()

    const button = findButton(container, 'Force full project reindex')
    expect(button).not.toBeNull()
    expect(button!.disabled).toBe(false)

    // First click: the optimistic latch engages immediately, before any status
    // event — the button flips to the busy state and is disabled.
    await act(async () => {
      button!.click()
    })
    expect(reindexMock).toHaveBeenCalledTimes(1)

    const busyButton = findButton(container, 'Reindexing...')
    expect(busyButton).not.toBeNull()
    expect(busyButton!.disabled).toBe(true)

    // A second click while the store still reports the stale (ready) state must
    // not fire another RPC.
    await act(async () => {
      busyButton!.click()
    })
    expect(reindexMock).toHaveBeenCalledTimes(1)

    // Backend emits the busy status → the store now owns the disabled state.
    await act(async () => {
      setIndexState('reindexing')
    })
    expect(findButton(container, 'Reindexing...')!.disabled).toBe(true)

    // Completion releases the latch and re-enables the action.
    await act(async () => {
      setIndexState('ready')
    })
    const readyButton = findButton(container, 'Force full project reindex')
    expect(readyButton).not.toBeNull()
    expect(readyButton!.disabled).toBe(false)
  })

  it('releases the optimistic latch when the reindex request is rejected', async () => {
    reindexMock.mockRejectedValueOnce(new Error('no indexer configured'))
    await renderFilters()

    const button = findButton(container, 'Force full project reindex')
    await act(async () => {
      button!.click()
    })
    expect(reindexMock).toHaveBeenCalledTimes(1)

    // The rejection releases the latch — the action is available again.
    const readyButton = findButton(container, 'Force full project reindex')
    expect(readyButton).not.toBeNull()
    expect(readyButton!.disabled).toBe(false)
  })

  it('releases the optimistic latch when a status event lands without a busy phase', async () => {
    // A backend pass that skips the documented busy status (or events
    // coalesced away) would leave the latch engaged forever with the store
    // still reporting the previous non-busy state — any vector_index:status
    // event observed after the request must release it.
    await renderFilters()

    const button = findButton(container, 'Force full project reindex')
    await act(async () => {
      button!.click()
    })
    expect(reindexMock).toHaveBeenCalledTimes(1)
    expect(findButton(container, 'Reindexing...')!.disabled).toBe(true)

    // The store never flips to a busy state; only a (terminal) status event
    // arrives — the latch releases and the action becomes available again.
    await act(async () => {
      emitVectorIndexStatus()
    })

    const readyButton = findButton(container, 'Force full project reindex')
    expect(readyButton).not.toBeNull()
    expect(readyButton!.disabled).toBe(false)
  })

  it('releases the optimistic latch and hides the action when the project becomes unavailable', async () => {
    await renderFilters()

    const button = findButton(container, 'Force full project reindex')
    await act(async () => {
      button!.click()
    })
    expect(reindexMock).toHaveBeenCalledTimes(1)
    expect(findButton(container, 'Reindexing...')!.disabled).toBe(true)

    // Switching to No Project invalidates the pending request and hides the
    // action entirely (CHAT mode has no vector index to reindex).
    await act(async () => {
      useProjectStore.setState({
        projects: [makeProject({ id: 'np', is_no_project: true, workspace_path: '/np' })],
        activeProjectId: 'np',
      })
    })
    expect(findButton(container, 'Reindex unavailable')).toBeNull()
    expect(findButton(container, 'Reindexing...')).toBeNull()
    expect(findButton(container, 'Force full project reindex')).toBeNull()
  })
})
