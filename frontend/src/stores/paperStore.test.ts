// @vitest-environment jsdom
// Unit tests for stores/paperStore.ts — incremental library updates,
// cross-project state reset, event-driven invalidation, and selector/hook
// reference stability (React #185).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/api/papers', () => ({
  getPapers: vi.fn(),
  getPaper: vi.fn(),
  setPaperPinned: vi.fn(),
  recordFlashcardReview: vi.fn(),
}))

import {
  getPaper,
  getPapers,
  recordFlashcardReview,
  setPaperPinned,
  type PaperLibrary,
  type PaperRecord,
} from '@/api/papers'
import {
  applyPapersChanged,
  commitFlashcardReview,
  fetchPaperLibrary,
  refreshPaper,
  selectPapers,
  selectPapersError,
  selectPapersLoading,
  selectPapersMutating,
  selectPapersProjectId,
  selectPaperViewMode,
  selectPinnedPaperPaths,
  selectSelectedPaperId,
  togglePaperPin,
  useFilteredPapers,
  usePaperStore,
  usePapers,
  usePaperViewMode,
  usePinnedPapers,
  useSelectedPaper,
  type PaperViewMode,
} from './paperStore'

const mockedGetPapers = vi.mocked(getPapers)
const mockedGetPaper = vi.mocked(getPaper)
const mockedSetPaperPinned = vi.mocked(setPaperPinned)
const mockedRecordFlashcardReview = vi.mocked(recordFlashcardReview)

// --- Fixtures ---

function recordOf(id: string, overrides: Partial<PaperRecord> = {}): PaperRecord {
  const slug = id.toLowerCase()
  return {
    id,
    slug,
    title: `Paper ${id}`,
    authors: ['A. Author'],
    year: 2024,
    venue: 'Journal',
    identifiers: [],
    mode: 'skim',
    reading: '',
    verdict: 'uncertain',
    confidence: 'medium',
    research_ids: [],
    anchors: [],
    claims: [],
    red_flags: [],
    uncertainties: [],
    dir: `/ws/.research/papers/${slug}`,
    card_path: `papers/${slug}/paper.md`,
    pinned: false,
    linked_research: [],
    ...overrides,
  }
}

function libraryOf(
  projectId: string,
  papers: PaperRecord[],
  pinned: string[] = [],
): PaperLibrary {
  return {
    project_id: projectId,
    research_root: '/ws/.research',
    root: '/ws/.research/papers',
    papers,
    pinned,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

// --- Selector-hook harness (see e2sStore.test.ts) ---

let container: HTMLDivElement | null = null
let root: Root | null = null
const seen: unknown[] = []
const last = (): unknown => seen[seen.length - 1]

function renderCapture(hook: () => unknown): void {
  if (root) {
    act(() => {
      root!.unmount()
    })
    root = null
  }
  container?.remove()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(
      createElement(function Harness(): null {
        seen.push(hook())
        return null
      }),
    )
  })
}

describe('paperStore reducer', () => {
  beforeEach(() => {
    usePaperStore.getState().reset()
  })

  it('starts empty', () => {
    const s = usePaperStore.getState()
    expect(s.projectId).toBeNull()
    expect(s.papers).toEqual([])
    expect(s.records).toEqual({})
    expect(s.pinned).toEqual([])
    expect(s.selectedPaperId).toBeNull()
    expect(s.mode).toBe('library')
    expect(s.isLoading).toBe(false)
    expect(s.isMutating).toBe(false)
    expect(s.error).toBeNull()
  })

  it('loadLibrary stamps the project + library and clears loading/error', () => {
    usePaperStore.getState().setLoading(true)
    usePaperStore.getState().setError('boom')

    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')], ['papers/p-001/paper.md']))

    const s = usePaperStore.getState()
    expect(s.projectId).toBe('p1')
    expect(s.root).toBe('/ws/.research/papers')
    expect(s.papers.map((p) => p.id)).toEqual(['P-001'])
    expect(s.records['P-001']).toBe(s.papers[0])
    expect(s.pinned).toEqual(['papers/p-001/paper.md'])
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })

  it('loadLibrary merges incrementally: unchanged records keep their identity', () => {
    usePaperStore.getState().loadLibrary(
      libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]),
    )
    const before = usePaperStore.getState()
    const firstRecord = before.records['P-001']
    const secondRecord = before.records['P-002']

    // A refresh where only P-002 changed (title edited) + P-003 added.
    usePaperStore.getState().loadLibrary(
      libraryOf('p1', [
        recordOf('P-001'),
        recordOf('P-002', { title: 'Edited' }),
        recordOf('P-003'),
      ]),
    )

    const after = usePaperStore.getState()
    // Unchanged record reused; changed/appended records replaced.
    expect(after.records['P-001']).toBe(firstRecord)
    expect(after.records['P-002']).not.toBe(secondRecord)
    expect(after.records['P-002']!.title).toBe('Edited')
    expect(after.records['P-003']).toBeDefined()
    expect(after.papers).toHaveLength(3)
  })

  it('loadLibrary keeps selection + mode across a same-project refresh', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))
    usePaperStore.getState().selectPaper('P-001')
    usePaperStore.getState().setMode('reader')

    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))

    const s = usePaperStore.getState()
    expect(s.selectedPaperId).toBe('P-001')
    expect(s.mode).toBe('reader')
  })

  it('loadLibrary clears the selection when the selected paper vanished', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))
    usePaperStore.getState().selectPaper('P-002')

    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))

    expect(usePaperStore.getState().selectedPaperId).toBeNull()
  })

  it('loadLibrary resets the project-scoped state on a cross-project load', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    usePaperStore.getState().selectPaper('P-001')
    usePaperStore.getState().setMode('reader')
    const previousRecords = usePaperStore.getState().records

    // Same id in the other project: the selection must NOT bleed across.
    usePaperStore.getState().loadLibrary(
      libraryOf('p2', [recordOf('P-001', { title: 'Other project' })], ['papers/p-001/paper.md']),
    )

    const s = usePaperStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.selectedPaperId).toBeNull()
    expect(s.mode).toBe('library')
    expect(s.records).not.toBe(previousRecords)
    expect(s.records['P-001']!.title).toBe('Other project')
    expect(s.pinned).toEqual(['papers/p-001/paper.md'])
  })

  it('loadPaper upserts a single record without disturbing the rest', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))
    const other = usePaperStore.getState().records['P-001']

    usePaperStore.getState().loadPaper(recordOf('P-002', { pinned: true }))
    expect(usePaperStore.getState().records['P-001']).toBe(other)
    expect(usePaperStore.getState().records['P-002']!.pinned).toBe(true)
    // The pin is folded into the pinned-path list.
    expect(usePaperStore.getState().pinned).toEqual(['papers/p-002/paper.md'])

    usePaperStore.getState().loadPaper(recordOf('P-002'))
    expect(usePaperStore.getState().pinned).toEqual([])

    usePaperStore.getState().loadPaper(recordOf('P-009'))
    expect(usePaperStore.getState().papers.map((p) => p.id)).toEqual(['P-001', 'P-002', 'P-009'])
  })

  it('setMode / setLoading / setMutating / setError mutate only their slice', () => {
    const papers = usePaperStore.getState().papers
    usePaperStore.getState().setMode('reader')
    expect(usePaperStore.getState().mode).toBe('reader')
    usePaperStore.getState().setLoading(true)
    expect(usePaperStore.getState().isLoading).toBe(true)
    usePaperStore.getState().setMutating(true)
    expect(usePaperStore.getState().isMutating).toBe(true)
    usePaperStore.getState().setError('err')
    const s = usePaperStore.getState()
    expect(s.error).toBe('err')
    // setError settles both invoke flags.
    expect(s.isLoading).toBe(false)
    expect(s.isMutating).toBe(false)
    // Data references untouched by view/invoke-state mutations.
    expect(s.papers).toBe(papers)
  })

  it('reset returns every field to initial', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    usePaperStore.getState().selectPaper('P-001')
    usePaperStore.getState().setMode('reader')
    usePaperStore.getState().reset()

    const s = usePaperStore.getState()
    expect(s.projectId).toBeNull()
    expect(s.papers).toEqual([])
    expect(s.records).toEqual({})
    expect(s.pinned).toEqual([])
    expect(s.selectedPaperId).toBeNull()
    expect(s.mode).toBe('library')
  })
})

describe('paperStore selectors (reference stability)', () => {
  beforeEach(() => {
    usePaperStore.getState().reset()
  })

  it('every selector returns a referentially stable value (no allocation)', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))
    usePaperStore.getState().selectPaper('P-001')

    const state = usePaperStore.getState()
    const selectors = [
      selectPapers,
      selectPinnedPaperPaths,
      selectPapersProjectId,
      selectSelectedPaperId,
      selectPaperViewMode,
      selectPapersLoading,
      selectPapersMutating,
      selectPapersError,
    ]
    for (const selector of selectors) {
      expect(Object.is(selector(state), selector(state))).toBe(true)
    }
  })

  it('selectors keep their reference across unrelated state updates', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const before = usePaperStore.getState()
    const papersBefore = selectPapers(before)
    const pinnedBefore = selectPinnedPaperPaths(before)

    usePaperStore.getState().setMode('reader')
    usePaperStore.getState().setLoading(true)

    const after = usePaperStore.getState()
    expect(selectPapers(after)).toBe(papersBefore)
    expect(selectPinnedPaperPaths(after)).toBe(pinnedBefore)
  })
})

describe('paperStore hooks', () => {
  beforeEach(() => {
    usePaperStore.getState().reset()
    seen.length = 0
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
    container?.remove()
    container = null
  })

  it('usePapers returns the direct store array reference', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const stored = usePaperStore.getState().papers
    renderCapture(usePapers)
    expect(last()).toBe(stored)
  })

  it('useSelectedPaper returns the exact stored record (no copy)', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    usePaperStore.getState().selectPaper('P-001')
    renderCapture(useSelectedPaper)
    expect(last()).toBe(usePaperStore.getState().records['P-001'])

    act(() => {
      usePaperStore.getState().selectPaper(null)
    })
    expect(last()).toBeNull()
  })

  it('usePinnedPapers derives with useMemo (stable across unrelated re-renders)', () => {
    usePaperStore
      .getState()
      .loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')], ['papers/p-002/paper.md']))

    renderCapture(() => ({ pinned: usePinnedPapers(), mode: usePaperViewMode() }))
    const first = seen[0] as { pinned: PaperRecord[]; mode: PaperViewMode }
    expect(first.pinned.map((p) => p.id)).toEqual(['P-002'])

    // A mode change re-renders the harness; the derived array must keep its
    // identity (the selector allocates nothing).
    act(() => {
      usePaperStore.getState().setMode('reader')
    })
    const second = seen[seen.length - 1] as { pinned: PaperRecord[]; mode: PaperViewMode }
    expect(second.mode).toBe('reader')
    expect(second.pinned).toBe(first.pinned)
  })

  it('usePinnedPapers returns the library array itself when nothing is pinned', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))
    const papers = usePaperStore.getState().papers
    renderCapture(usePinnedPapers)
    expect(last()).toBe(papers)
  })

  it('useFilteredPapers memoizes and returns the library for an empty query', () => {
    usePaperStore.getState().loadLibrary(
      libraryOf('p1', [recordOf('P-001', { title: 'Attention' }), recordOf('P-002')]),
    )
    const papers = usePaperStore.getState().papers

    renderCapture(() => ({ filtered: useFilteredPapers('attention'), mode: usePaperViewMode() }))
    const first = seen[0] as { filtered: PaperRecord[]; mode: PaperViewMode }
    expect(first.filtered.map((p) => p.id)).toEqual(['P-001'])

    act(() => {
      usePaperStore.getState().setMode('reader')
    })
    const second = seen[seen.length - 1] as { filtered: PaperRecord[]; mode: PaperViewMode }
    expect(second.filtered).toBe(first.filtered)

    renderCapture(() => useFilteredPapers(''))
    expect(last()).toBe(papers)
  })
})

describe('paperStore backend sync', () => {
  beforeEach(() => {
    usePaperStore.getState().reset()
    mockedGetPapers.mockReset()
    mockedGetPaper.mockReset()
    mockedSetPaperPinned.mockReset()
  })

  it('fetchPaperLibrary applies the fetched library and settles the spinner', async () => {
    const pending = deferred<PaperLibrary>()
    mockedGetPapers.mockReturnValueOnce(pending.promise)

    const promise = fetchPaperLibrary('p1')
    expect(usePaperStore.getState().isLoading).toBe(true)

    pending.resolve(libraryOf('p1', [recordOf('P-001')]))
    await promise

    const s = usePaperStore.getState()
    expect(mockedGetPapers).toHaveBeenCalledWith('p1')
    expect(s.projectId).toBe('p1')
    expect(s.papers.map((p) => p.id)).toEqual(['P-001'])
    expect(s.isLoading).toBe(false)
  })

  it('fetchPaperLibrary records an error and keeps the previous library', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedGetPapers.mockRejectedValueOnce(new Error('nope'))

    await fetchPaperLibrary('p1')

    const s = usePaperStore.getState()
    expect(s.error).toBe('nope')
    expect(s.isLoading).toBe(false)
    expect(s.papers.map((p) => p.id)).toEqual(['P-001'])
  })

  it('fetchPaperLibrary drops a stale (older-started) payload: last-write-wins', async () => {
    const first = deferred<PaperLibrary>()
    const second = deferred<PaperLibrary>()
    mockedGetPapers
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)

    const firstFetch = fetchPaperLibrary('p1')
    const secondFetch = fetchPaperLibrary('p2')

    // The NEWER fetch resolves first and wins.
    second.resolve(libraryOf('p2', [recordOf('P-002')]))
    await secondFetch
    expect(usePaperStore.getState().projectId).toBe('p2')

    // The older fetch resolves later and must be dropped wholesale.
    first.resolve(libraryOf('p1', [recordOf('P-001')]))
    await firstFetch

    const s = usePaperStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.papers.map((p) => p.id)).toEqual(['P-002'])
    // The superseded fetch must not clear the spinner the newest fetch owns.
    expect(s.isLoading).toBe(false)
  })

  it('applyPapersChanged refetches (invalidates) for the loaded project', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const unchanged = usePaperStore.getState().records['P-001']
    mockedGetPapers.mockResolvedValueOnce(
      libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]),
    )

    const invalidated = await applyPapersChanged({
      project_id: 'p1',
      paths: '/ws/.research/papers/p-002/paper.md',
    })

    expect(invalidated).toBe(true)
    expect(mockedGetPapers).toHaveBeenCalledWith('p1')
    const s = usePaperStore.getState()
    expect(s.papers.map((p) => p.id)).toEqual(['P-001', 'P-002'])
    // The refresh merges incrementally: the untouched card keeps its identity.
    expect(s.records['P-001']).toBe(unchanged)
    expect(s.lastSyncAt).toBeGreaterThan(0)
  })

  it('applyPapersChanged ignores an event for another project', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))

    const invalidated = await applyPapersChanged({
      project_id: 'p2',
      paths: '/ws/.research/papers/p-001/paper.md',
    })

    expect(invalidated).toBe(false)
    expect(mockedGetPapers).not.toHaveBeenCalled()
    expect(usePaperStore.getState().projectId).toBe('p1')
  })

  it('applyPapersChanged ignores an event when no library is loaded', async () => {
    const invalidated = await applyPapersChanged({
      project_id: 'p1',
      paths: '/ws/.research/papers/p-001/paper.md',
    })

    expect(invalidated).toBe(false)
    expect(mockedGetPapers).not.toHaveBeenCalled()
  })

  it('refreshPaper upserts a single record incrementally', async () => {
    usePaperStore
      .getState()
      .loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))
    const untouched = usePaperStore.getState().records['P-001']
    mockedGetPaper.mockResolvedValueOnce(recordOf('P-002', { title: 'Re-read' }))

    await refreshPaper('P-002')

    expect(mockedGetPaper).toHaveBeenCalledWith('p1', 'P-002')
    const s = usePaperStore.getState()
    expect(s.records['P-002']!.title).toBe('Re-read')
    // The rest of the library is untouched (same reference).
    expect(s.records['P-001']).toBe(untouched)
  })

  it('refreshPaper is a no-op without a loaded library or for an unknown paper', async () => {
    await refreshPaper('P-001')
    expect(mockedGetPaper).not.toHaveBeenCalled()

    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    await refreshPaper('P-404')
    expect(mockedGetPaper).not.toHaveBeenCalled()
  })

  it('refreshPaper records an error and keeps the previous record', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedGetPaper.mockRejectedValueOnce(new Error('gone'))

    await refreshPaper('P-001')

    const s = usePaperStore.getState()
    expect(s.error).toBe('gone')
    expect(s.records['P-001']!.title).toBe('Paper P-001')
  })

  it('togglePaperPin persists and reflects the pin locally', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedSetPaperPinned.mockResolvedValueOnce(undefined)

    await togglePaperPin('P-001', true)

    expect(mockedSetPaperPinned).toHaveBeenCalledWith('p1', 'P-001', true)
    const s = usePaperStore.getState()
    expect(s.records['P-001']!.pinned).toBe(true)
    expect(s.pinned).toEqual(['papers/p-001/paper.md'])
    expect(s.isMutating).toBe(false)
  })

  it('togglePaperPin is a no-op without a loaded library or for an unknown paper', async () => {
    await togglePaperPin('P-001', true)
    expect(mockedSetPaperPinned).not.toHaveBeenCalled()

    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    await togglePaperPin('P-404', true)
    expect(mockedSetPaperPinned).not.toHaveBeenCalled()
  })

  it('togglePaperPin records an error without touching the record', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedSetPaperPinned.mockRejectedValueOnce(new Error('denied'))

    await togglePaperPin('P-001', true)

    const s = usePaperStore.getState()
    expect(s.error).toBe('denied')
    expect(s.records['P-001']!.pinned).toBe(false)
    expect(s.pinned).toEqual([])
    expect(s.isMutating).toBe(false)
  })
})

describe('paperStore flashcard write-back', () => {
  beforeEach(() => {
    usePaperStore.getState().reset()
    mockedRecordFlashcardReview.mockReset()
  })

  it('records a review for the loaded project', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedRecordFlashcardReview.mockResolvedValueOnce(undefined)

    await commitFlashcardReview('P-001', 'P1-01', 'good')

    expect(mockedRecordFlashcardReview).toHaveBeenCalledWith('p1', 'P-001', 'P1-01', 'good')
  })

  it('is a no-op without a loaded library, for an unknown paper, or a card-less id', async () => {
    await commitFlashcardReview('P-001', 'P1-01', 'good')
    expect(mockedRecordFlashcardReview).not.toHaveBeenCalled()

    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    await commitFlashcardReview('P-404', 'P1-01', 'good')
    await commitFlashcardReview('P-001', '', 'good')
    expect(mockedRecordFlashcardReview).not.toHaveBeenCalled()
  })

  it('fails soft: a rejected RPC does not throw and leaves the library intact', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedRecordFlashcardReview.mockRejectedValueOnce(new Error('boom'))

    await expect(commitFlashcardReview('P-001', 'P1-01', 'good')).resolves.toBeUndefined()
    expect(usePaperStore.getState().projectId).toBe('p1')
    expect(usePaperStore.getState().records['P-001']).toBeDefined()
  })
})
