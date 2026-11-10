// @vitest-environment jsdom
//
// Tests for the force-full-reindex action hosted on the VectorSearchFilters
// mode-selector row (after the hybrid/vector/lexical buttons). The action was
// moved here from the FileTreePanel explorer header; the behavior is
// unchanged: fire-and-forget RPC, optimistic latch against duplicate clicks
// until the RPC resolves or the first vector_index:status event lands,
// spinning/disabled while busy — a pass in flight OR the branch-scoped DB
// still opening (`loading`, ADR-064) — and hidden (not merely disabled) when
// no reindexable project is active.

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
  // The hook surfaces a rejected reindex RPC through the app-wide
  // runtime_error toast; spied here so tests can assert it stays silent on
  // the happy path.
  emit: emitMock,
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
const emitMock = vi.hoisted(() => vi.fn())

/** A controllable pending reindex RPC: the optimistic latch now releases
 *  when the RPC RESOLVES (the pass is scheduled), so the tests that pin the
 *  latched window keep the promise pending until they choose to settle it. */
function deferredRpc(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void
  const promise = new Promise<void>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

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

  // ADR-064: the branch-scoped chromem DB open reports the distinct
  // `loading` state. Before ADR-064 the open reported `indexing`, which kept
  // the action disabled; `loading` must keep it disabled too — a reindex
  // fired during the open would race it and, until the manager publishes the
  // indexer, could only fail.
  it('is disabled and spinning while the branch-scoped index is still opening (loading)', async () => {
    setIndexState('loading')
    await renderFilters()

    // The open is not a reindex pass, so the tooltip must not claim one.
    const button = findButton(container, 'Opening the index…')
    expect(button).not.toBeNull()
    expect(button!.disabled).toBe(true)
    expect(findButton(container, 'Reindexing...')).toBeNull()
    expect(findButton(container, 'Force full project reindex')).toBeNull()

    await act(async () => {
      button!.click()
    })
    expect(reindexMock).not.toHaveBeenCalled()

    // The open settling re-enables the action.
    await act(async () => {
      setIndexState('ready')
    })
    expect(findButton(container, 'Force full project reindex')!.disabled).toBe(false)
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

  it('blocks duplicate clicks until the RPC resolves or a status event arrives', async () => {
    const pending = deferredRpc()
    reindexMock.mockReturnValueOnce(pending.promise)
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

    // A second click while the request is still pending and the store still
    // reports the stale (ready) state must not fire another RPC.
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
    // Settle the parked promise so no state update lands outside act.
    await act(async () => {
      pending.resolve()
    })
  })

  it('releases the optimistic latch when the reindex request is rejected', async () => {
    reindexMock.mockRejectedValueOnce(new Error('no indexer configured'))
    await renderFilters()

    const button = findButton(container, 'Force full project reindex')
    await act(async () => {
      button!.click()
    })
    expect(reindexMock).toHaveBeenCalledTimes(1)

    // The rejection releases the latch — the action is available again — and
    // surfaces through the app-wide runtime_error toast, never a silent
    // console-only no-op.
    const readyButton = findButton(container, 'Force full project reindex')
    expect(readyButton).not.toBeNull()
    expect(readyButton!.disabled).toBe(false)
    expect(emitMock).toHaveBeenCalledTimes(1)
    expect(emitMock).toHaveBeenCalledWith(
      'runtime_error',
      expect.objectContaining({
        id: expect.any(String),
        message: 'Rebuild index failed: no indexer configured',
      }),
    )
  })

  it('releases the optimistic latch when a status event lands without a busy phase', async () => {
    // A backend pass that skips the documented busy status (or events
    // coalesced away) would leave the latch engaged forever with the store
    // still reporting the previous non-busy state — any vector_index:status
    // event observed after the request must release it.
    const pending = deferredRpc()
    reindexMock.mockReturnValueOnce(pending.promise)
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
    await act(async () => {
      pending.resolve()
    })
  })

  it('releases the optimistic latch and hides the action when the project becomes unavailable', async () => {
    const pending = deferredRpc()
    reindexMock.mockReturnValueOnce(pending.promise)
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
    await act(async () => {
      pending.resolve()
    })
  })

  // The unavailable state is both the state that most needs explaining (why is
  // search gone?) and the user's repair path. Both halves are pinned here: the
  // backend's own cause is legible inline next to the filters (never a modal, a
  // toast or a log dive), and the action stays clickable, labelled a rebuild,
  // with an honest busy title while the repair runs.
  describe('unavailable — the cause is legible and the repair stays reachable', () => {
    function setUnavailable(lastError: string | undefined, errorKind?: VectorIndexStatus['error_kind']): void {
      useVectorIndexStore.getState().setStatus({
        state: 'unavailable',
        progress: 0,
        files_indexed: 0,
        total_files: 0,
        last_error: lastError,
        error_kind: errorKind,
      })
    }

    it('renders the cause inline and keeps the repair action enabled', async () => {
      setUnavailable('corpus store is corrupt (bad header)', 'repairable')
      await renderFilters()

      // Inline text, in the filters column: the panel-local copy of the pill
      // tooltip, so the reason is readable without hovering anything.
      expect(container.textContent).toContain('corpus store is corrupt (bad header)')

      const button = findButton(container, 'Rebuild index — recreate the broken index from source')
      expect(button).not.toBeNull()
      expect(button!.disabled).toBe(false)

      const pending = deferredRpc()
      reindexMock.mockReturnValueOnce(pending.promise)
      await act(async () => {
        button!.click()
      })
      // Nothing is running yet, but the request is: the latch owns the busy
      // state and names the repair, not an incremental pass.
      expect(reindexMock).toHaveBeenCalledTimes(1)
      expect(findButton(container, 'Repairing index…')!.disabled).toBe(true)
      await act(async () => {
        pending.resolve()
      })
    })

    it('releases the latch when a status event settles back into unavailable', async () => {
      setUnavailable('corpus store is corrupt', 'repairable')
      await renderFilters()

      const pending = deferredRpc()
      reindexMock.mockReturnValueOnce(pending.promise)
      const button = findButton(container, 'Rebuild index — recreate the broken index from source')
      await act(async () => {
        button!.click()
      })
      expect(reindexMock).toHaveBeenCalledTimes(1)
      expect(findButton(container, 'Repairing index…')!.disabled).toBe(true)

      // The backend answers with a (still) unavailable status: the latch must
      // release so a second repair attempt stays possible.
      await act(async () => {
        emitVectorIndexStatus()
      })
      const repair = findButton(container, 'Rebuild index — recreate the broken index from source')
      expect(repair).not.toBeNull()
      expect(repair!.disabled).toBe(false)
      await act(async () => {
        pending.resolve()
      })
    })

    it('releases the optimistic latch when the state flips to unavailable', async () => {
      // Started healthy: the action is a plain force-reindex until the backend
      // reports the failure the request surfaced.
      const pending = deferredRpc()
      reindexMock.mockReturnValueOnce(pending.promise)
      await renderFilters()
      const button = findButton(container, 'Force full project reindex')
      await act(async () => {
        button!.click()
      })
      expect(findButton(container, 'Reindexing...')!.disabled).toBe(true)

      await act(async () => {
        setUnavailable('failed to open the corpus', 'fatal')
      })
      // A `fatal` unavailable renders the action disabled with its own
      // reason title — the rebuild cannot fix it (see the gate tests in
      // useVectorReindex.test.tsx); the latch is released either way.
      const gated = findButton(container, 'A rebuild cannot fix this failure — see the status cause below')
      expect(gated).not.toBeNull()
      expect(gated!.disabled).toBe(true)
      await act(async () => {
        pending.resolve()
      })
    })

    it('drops the cause line once a healthy status replaces it (the reason is transient)', async () => {
      setUnavailable('corpus store is corrupt', 'repairable')
      await renderFilters()
      expect(container.textContent).toContain('corpus store is corrupt')

      // A healthy snapshot carries no cause — the whole status object is
      // replaced, so no stale reason may linger in the panel.
      await act(async () => {
        useVectorIndexStore.getState().setStatus({
          state: 'ready',
          progress: 1,
          files_indexed: 2,
          total_files: 2,
        })
      })
      expect(container.textContent).not.toContain('corpus store is corrupt')
      expect(findButton(container, 'Force full project reindex')).not.toBeNull()
    })

    it('renders no cause line for an unavailable status without one (never a placeholder)', async () => {
      setUnavailable(undefined, 'no_target')
      await renderFilters()

      // No invented text: the pill tooltip carries the classification, but the
      // inline line only ever shows a real cause. The no_target kind also
      // renders the repair action disabled — there is no focus root to
      // reindex, so the RPC would only be rejected.
      expect(container.textContent).not.toContain('Kind:')
      const gated = findButton(container, 'No focused workspace to reindex')
      expect(gated).not.toBeNull()
      expect(gated!.disabled).toBe(true)
    })
  })
})
