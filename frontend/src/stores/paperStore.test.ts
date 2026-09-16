// @vitest-environment jsdom
// Unit tests for stores/paperStore.ts — incremental library updates,
// cross-project state reset, event-driven invalidation, the in-flight pin
// guards, and selector/hook reference stability (React #185).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/api/papers', () => ({
  getPapers: vi.fn(),
  setPaperPinned: vi.fn(),
  recordFlashcardReview: vi.fn(),
}))

import {
  getPapers,
  recordFlashcardReview,
  setPaperPinned,
  type PaperLibrary,
  type PaperRecord,
} from '@/api/papers'
import {
  commitFlashcardReview,
  ensurePaperPinned,
  fetchPaperLibrary,
  selectPapers,
  selectPapersError,
  selectPapersLoading,
  selectPapersMutating,
  selectPapersProjectId,
  selectPapersSyncAt,
  togglePaperPin,
  usePaperBySlug,
  usePapers,
  usePaperStore,
} from './paperStore'

const mockedGetPapers = vi.mocked(getPapers)
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
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
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
    expect(s.isLoading).toBe(false)
    expect(s.isMutating).toBe(false)
    expect(s.error).toBeNull()
    expect(s.lastSyncAt).toBe(0)
  })

  it('loadLibrary stamps the project + library and clears loading/mutating/error', () => {
    usePaperStore.getState().setLoading(true)
    usePaperStore.getState().setMutating(true)
    usePaperStore.getState().setError('boom')

    usePaperStore
      .getState()
      .loadLibrary(libraryOf('p1', [recordOf('P-001')], ['papers/p-001/paper.md']))

    const s = usePaperStore.getState()
    expect(s.projectId).toBe('p1')
    expect(s.root).toBe('/ws/.research/papers')
    expect(s.papers.map((p) => p.id)).toEqual(['P-001'])
    expect(s.records['P-001']).toBe(s.papers[0])
    expect(s.pinned).toEqual(['papers/p-001/paper.md'])
    expect(s.isLoading).toBe(false)
    expect(s.isMutating).toBe(false)
    expect(s.error).toBeNull()
    expect(s.lastSyncAt).toBeGreaterThan(0)
  })

  it('loadLibrary merges incrementally: unchanged records keep their identity', () => {
    usePaperStore
      .getState()
      .loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))
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

  it('loadLibrary resets the per-paper index on a cross-project load', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const previousRecords = usePaperStore.getState().records

    // Same id in the other project: the index must NOT be reused across projects.
    usePaperStore.getState().loadLibrary(
      libraryOf('p2', [recordOf('P-001', { title: 'Other project' })], ['papers/p-001/paper.md']),
    )

    const s = usePaperStore.getState()
    expect(s.projectId).toBe('p2')
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

  it('setLoading / setMutating / setError mutate only their slice', () => {
    const papers = usePaperStore.getState().papers
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
    usePaperStore.getState().reset()

    const s = usePaperStore.getState()
    expect(s.projectId).toBeNull()
    expect(s.papers).toEqual([])
    expect(s.records).toEqual({})
    expect(s.pinned).toEqual([])
    expect(s.lastSyncAt).toBe(0)
  })
})

describe('paperStore selectors (reference stability)', () => {
  beforeEach(() => {
    usePaperStore.getState().reset()
  })

  it('every selector returns a referentially stable value (no allocation)', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001'), recordOf('P-002')]))

    const state = usePaperStore.getState()
    const selectors = [
      selectPapers,
      selectPapersProjectId,
      selectPapersLoading,
      selectPapersMutating,
      selectPapersError,
      selectPapersSyncAt,
    ]
    for (const selector of selectors) {
      expect(Object.is(selector(state), selector(state))).toBe(true)
    }
  })

  it('selectors keep their reference across unrelated state updates', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const before = usePaperStore.getState()
    const papersBefore = selectPapers(before)

    usePaperStore.getState().setLoading(true)

    const after = usePaperStore.getState()
    expect(selectPapers(after)).toBe(papersBefore)
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

  it('usePaperBySlug returns the exact stored record (no copy)', () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    renderCapture(() => usePaperBySlug('p-001'))
    expect(last()).toBe(usePaperStore.getState().records['P-001'])

    renderCapture(() => usePaperBySlug('missing'))
    expect(last()).toBeNull()
  })
})

describe('paperStore backend sync', () => {
  beforeEach(() => {
    usePaperStore.getState().reset()
    mockedGetPapers.mockReset()
    mockedSetPaperPinned.mockReset()
    mockedRecordFlashcardReview.mockReset()
  })

  it('fetchPaperLibrary applies the fetched library, settles the spinner and reports applied', async () => {
    const pending = deferred<PaperLibrary>()
    mockedGetPapers.mockReturnValueOnce(pending.promise)

    const promise = fetchPaperLibrary('p1')
    expect(usePaperStore.getState().isLoading).toBe(true)

    pending.resolve(libraryOf('p1', [recordOf('P-001')]))
    await expect(promise).resolves.toBe(true)

    const s = usePaperStore.getState()
    expect(mockedGetPapers).toHaveBeenCalledWith('p1')
    expect(s.projectId).toBe('p1')
    expect(s.papers.map((p) => p.id)).toEqual(['P-001'])
    expect(s.isLoading).toBe(false)
  })

  it('fetchPaperLibrary records an error, keeps the previous library and reports not-applied', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedGetPapers.mockRejectedValueOnce(new Error('nope'))

    await expect(fetchPaperLibrary('p1')).resolves.toBe(false)

    const s = usePaperStore.getState()
    expect(s.error).toBe('nope')
    expect(s.isLoading).toBe(false)
    expect(s.papers.map((p) => p.id)).toEqual(['P-001'])
  })

  it('fetchPaperLibrary drops a stale (older-started) payload: last-write-wins', async () => {
    const first = deferred<PaperLibrary>()
    const second = deferred<PaperLibrary>()
    mockedGetPapers.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const firstFetch = fetchPaperLibrary('p1')
    const secondFetch = fetchPaperLibrary('p2')

    // The NEWER fetch resolves first and wins.
    second.resolve(libraryOf('p2', [recordOf('P-002')]))
    await expect(secondFetch).resolves.toBe(true)
    expect(usePaperStore.getState().projectId).toBe('p2')

    // The older fetch resolves later and must be dropped wholesale.
    first.resolve(libraryOf('p1', [recordOf('P-001')]))
    await expect(firstFetch).resolves.toBe(false)

    const s = usePaperStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.papers.map((p) => p.id)).toEqual(['P-002'])
    // The superseded fetch must not clear the spinner the newest fetch owns.
    expect(s.isLoading).toBe(false)
  })

  it('reset() invalidates an in-flight fetch so it cannot repopulate the store (Issue 8)', async () => {
    const pending = deferred<PaperLibrary>()
    mockedGetPapers.mockReturnValueOnce(pending.promise)

    const fetch = fetchPaperLibrary('p1')
    expect(usePaperStore.getState().isLoading).toBe(true)

    // The user switches to No Project before the slow fetch resolves.
    usePaperStore.getState().reset()

    pending.resolve(libraryOf('p1', [recordOf('P-001')]))
    await fetch

    const s = usePaperStore.getState()
    expect(s.projectId).toBeNull()
    expect(s.papers).toEqual([])
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

  it('togglePaperPin folds the pin into a record refreshed during the RPC (Issue 10)', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001', { title: 'Old' })]))
    const pending = deferred<void>()
    mockedSetPaperPinned.mockReturnValueOnce(pending.promise)

    const pinning = togglePaperPin('P-001', true)
    // A `papers:changed` refetch lands while the pin is in flight, updating the
    // card (the pre-await snapshot would revert this on write-back).
    usePaperStore.getState().loadPaper(recordOf('P-001', { title: 'Fresh' }))

    pending.resolve()
    await pinning

    const record = usePaperStore.getState().records['P-001']!
    expect(record.pinned).toBe(true)
    expect(record.title).toBe('Fresh')
  })

  it('togglePaperPin ignores a repeat while the first is in flight (Issue 24)', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const first = deferred<void>()
    mockedSetPaperPinned.mockReturnValueOnce(first.promise)

    const a = togglePaperPin('P-001', true)
    const b = togglePaperPin('P-001', true)

    first.resolve()
    await Promise.all([a, b])

    expect(mockedSetPaperPinned).toHaveBeenCalledTimes(1)
    expect(usePaperStore.getState().records['P-001']!.pinned).toBe(true)
  })

  it('togglePaperPin clears isMutating when the project changes mid-flight (Issue 11)', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const pending = deferred<void>()
    mockedSetPaperPinned.mockReturnValueOnce(pending.promise)

    const pinning = togglePaperPin('P-001', true)
    expect(usePaperStore.getState().isMutating).toBe(true)

    // The active project flips while the write is in flight (the new project's
    // load has not landed yet).
    usePaperStore.setState({ projectId: 'p2' })

    pending.resolve()
    await pinning

    expect(usePaperStore.getState().isMutating).toBe(false)
  })

  it('togglePaperPin does not write the previous project error into the active store (Issue 19)', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const pending = deferred<void>()
    mockedSetPaperPinned.mockReturnValueOnce(pending.promise)

    const pinning = togglePaperPin('P-001', true)
    // Switch projects while the write is in flight.
    usePaperStore.getState().loadLibrary(libraryOf('p2', [recordOf('P-002')]))

    pending.reject(new Error('denied'))
    await pinning

    const s = usePaperStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.error).toBeNull()
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

  it('ensurePaperPinned pins an unpinned paper through the pin RPC', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedSetPaperPinned.mockResolvedValueOnce(undefined)

    await ensurePaperPinned('P-001')

    expect(mockedSetPaperPinned).toHaveBeenCalledWith('p1', 'P-001', true)
    expect(usePaperStore.getState().records['P-001']!.pinned).toBe(true)
  })

  it('ensurePaperPinned is a no-op when the paper is already pinned', async () => {
    usePaperStore
      .getState()
      .loadLibrary(libraryOf('p1', [recordOf('P-001', { pinned: true })], ['papers/p-001/paper.md']))

    await ensurePaperPinned('P-001')

    expect(mockedSetPaperPinned).not.toHaveBeenCalled()
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
    expect(usePaperStore.getState().error).toBeNull()
  })

  it('surfaces a failed write-back on the store error line (Issue 30)', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    mockedRecordFlashcardReview.mockRejectedValueOnce(new Error('deck gone'))

    await commitFlashcardReview('P-001', 'P1-01', 'good')

    expect(usePaperStore.getState().error).toBe('deck gone')
  })

  it('does not surface the failure into another project', async () => {
    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    const pending = deferred<void>()
    mockedRecordFlashcardReview.mockReturnValueOnce(pending.promise)

    const commit = commitFlashcardReview('P-001', 'P1-01', 'good')
    usePaperStore.getState().loadLibrary(libraryOf('p2', [recordOf('P-002')]))
    pending.reject(new Error('boom'))
    await commit

    expect(usePaperStore.getState().error).toBeNull()
  })

  it('is a no-op without a loaded library, for an unknown paper, or a card-less id', async () => {
    await commitFlashcardReview('P-001', 'P1-01', 'good')
    expect(mockedRecordFlashcardReview).not.toHaveBeenCalled()

    usePaperStore.getState().loadLibrary(libraryOf('p1', [recordOf('P-001')]))
    await commitFlashcardReview('P-404', 'P1-01', 'good')
    await commitFlashcardReview('P-001', '', 'good')
    expect(mockedRecordFlashcardReview).not.toHaveBeenCalled()
  })
})
