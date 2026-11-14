// Readiness gating for useVectorSearch: Enter in the inputs would bypass the
// (disabled) search button, so handleKeyDown must be a no-op while the vector
// index is not usable — no searchVectorStore call may block on index
// readiness. The usable predicate is ready PLUS the ADR-124 suspension (a
// live collection keeps serving queries), so Enter and the auto-search effect
// stay open while suspended and stay gated for every other non-ready state.
// Uses the real stores; only the RPC boundary (@/api/vector) is mocked.

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const searchSpy = vi.hoisted(() => vi.fn())

vi.mock('@/api/vector', () => ({
  searchVectorStore: searchSpy,
}))

import { useVectorSearch } from '@/hooks/useVectorSearch'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { useProjectStore } from '@/stores/projectStore'
import type { VectorIndexStatus } from '@/types/models'

const statusWith = (state: VectorIndexStatus['state']): VectorIndexStatus => ({
  state,
  progress: 0,
  files_indexed: 0,
  total_files: 0,
})

// Harness: capture the latest handleKeyDown so tests can invoke it directly.
let keyDown: ((e: ReactKeyboardEvent) => void) | null = null
function Harness() {
  const h = useVectorSearch()
  keyDown = h.handleKeyDown
  return null
}

/** Invoke the captured handleKeyDown, failing loudly if never rendered. */
function pressKey(key: string): void {
  if (!keyDown) throw new Error('harness did not capture handleKeyDown')
  // Minimal stand-in event — handleKeyDown only reads `key`.
  keyDown({ key } as unknown as ReactKeyboardEvent)
}

let container: HTMLElement
let root: Root

beforeEach(() => {
  searchSpy.mockReset()
  searchSpy.mockResolvedValue([])
  act(() => {
    useVectorIndexStore.getState().reset()
    useProjectStore.setState({ activeProjectId: null })
  })
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  document.body.replaceChildren()
})

function renderHarness() {
  act(() => {
    root.render(<Harness />)
  })
}

describe('useVectorSearch.handleKeyDown readiness gating', () => {
  it('Enter performs no search while the index is not ready', async () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('indexing'), query: 'handler' })
    })
    renderHarness()
    await act(async () => {
      pressKey('Enter')
    })
    expect(searchSpy).not.toHaveBeenCalled()
  })

  it('Enter performs no search in any non-ready state except the ADR-124 suspension', async () => {
    const nonReady = ['idle', 'reindexing', ['unavailable', 'fatal']] as const
    for (const entry of nonReady) {
      const [state, kind] = Array.isArray(entry) ? entry : [entry, undefined]
      searchSpy.mockClear()
      act(() => {
        useVectorIndexStore.setState({ status: { ...statusWith(state), error_kind: kind }, query: 'handler' })
      })
      renderHarness()
      await act(async () => {
        pressKey('Enter')
      })
      expect(searchSpy, kind ?? state).not.toHaveBeenCalled()
    }

    // The suspension keeps the search path open: the breaker stops the
    // UPDATE pipeline only and a live collection keeps serving queries, so
    // an enabled input whose Enter did nothing would be a silent dead end.
    searchSpy.mockClear()
    act(() => {
      useVectorIndexStore.setState({
        status: { ...statusWith('unavailable'), error_kind: 'suspended' },
        query: 'handler',
      })
    })
    renderHarness()
    await act(async () => {
      pressKey('Enter')
    })
    expect(searchSpy).toHaveBeenCalledTimes(1)
    expect(searchSpy).toHaveBeenCalledWith(
      expect.objectContaining({ query: 'handler', mode: 'hybrid' }),
    )
  })

  it('Enter triggers a search with the current filters when the index is ready', async () => {
    act(() => {
      useVectorIndexStore.setState({
        status: statusWith('ready'),
        query: 'handler',
        topK: 10,
        filePattern: '*.go',
      })
    })
    renderHarness()
    await act(async () => {
      pressKey('Enter')
    })
    expect(searchSpy).toHaveBeenCalledTimes(1)
    expect(searchSpy).toHaveBeenCalledWith({
      query: 'handler',
      top_k: 10,
      file_pattern: '*.go',
      must_match: [],
      mode: 'hybrid',
    })
  })

  it('non-Enter keys never trigger a search (unchanged)', async () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready'), query: 'handler' })
    })
    renderHarness()
    await act(async () => {
      pressKey('a')
    })
    expect(searchSpy).not.toHaveBeenCalled()
  })

  it('Enter strips +tokens into must_match when ready (unchanged handleSearch behavior)', async () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready'), query: 'handler +retry' })
    })
    renderHarness()
    await act(async () => {
      pressKey('Enter')
    })
    expect(searchSpy).toHaveBeenCalledWith({
      query: 'handler',
      top_k: 50,
      file_pattern: '',
      must_match: ['retry'],
      mode: 'hybrid',
    })
    expect(useVectorIndexStore.getState().query).toBe('handler')
    expect(useVectorIndexStore.getState().mustMatch).toEqual(['retry'])
  })
})

describe('useVectorSearch auto-search effect gating (unchanged)', () => {
  it('does not auto-search while the index is not ready, even with an active project and query', async () => {
    act(() => {
      useProjectStore.setState({ activeProjectId: 'proj-1' })
      useVectorIndexStore.setState({ status: statusWith('indexing'), query: 'handler' })
    })
    await act(async () => {
      root.render(<Harness />)
    })
    expect(searchSpy).not.toHaveBeenCalled()
  })

  it('auto-searches once on mount when ready with an active project and query', async () => {
    act(() => {
      useProjectStore.setState({ activeProjectId: 'proj-1' })
      useVectorIndexStore.setState({ status: statusWith('ready'), query: 'handler' })
    })
    await act(async () => {
      root.render(<Harness />)
    })
    expect(searchSpy).toHaveBeenCalledTimes(1)
    expect(searchSpy).toHaveBeenCalledWith({
      query: 'handler',
      top_k: 50,
      file_pattern: '',
      must_match: [],
      mode: 'hybrid',
    })
  })

  it('auto-searches while the ADR-124 suspension holds (a live collection keeps serving queries)', async () => {
    act(() => {
      useProjectStore.setState({ activeProjectId: 'proj-1' })
      useVectorIndexStore.setState({
        status: { ...statusWith('unavailable'), error_kind: 'suspended' },
        query: 'handler',
      })
    })
    await act(async () => {
      root.render(<Harness />)
    })
    expect(searchSpy).toHaveBeenCalledTimes(1)
  })

  it('does not auto-search unless the (unavailable, suspended) pair opens the gate', async () => {
    // The gate opens on the (unavailable, suspended) PAIR; a suspended kind
    // in another state, or an unavailable with another kind, stays gated.
    for (const st of [
      { ...statusWith('idle'), error_kind: 'suspended' as const },
      { ...statusWith('unavailable'), error_kind: 'repairable' as const },
    ]) {
      searchSpy.mockClear()
      act(() => {
        useProjectStore.setState({ activeProjectId: 'proj-1' })
        useVectorIndexStore.setState({ status: st, query: 'handler' })
      })
      await act(async () => {
        root.render(<Harness />)
      })
      expect(searchSpy).not.toHaveBeenCalled()
    }
  })
})
