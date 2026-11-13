// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Controllable store state. VectorSearchResults uses the hook form
// `useVectorIndexStore(selector)`, so the mock applies the selector to this
// shared object. Mocking (rather than the real persisted store) avoids the
// zustand `persist` middleware crashing under jsdom when localStorage is
// undefined.
interface MockVectorStatus {
  state: 'idle' | 'indexing' | 'ready' | 'reindexing' | 'unavailable' | 'loading'
  // error_kind rides the wire payload (optional, omitempty); the ADR-122
  // suspension tests below set it.
  error_kind?: string
  progress: number
  files_indexed: number
  total_files: number
}

const state = vi.hoisted(() => ({
  status: {
    state: 'ready',
    progress: 0,
    files_indexed: 0,
    total_files: 0,
  } as MockVectorStatus,
  entries: [] as unknown[],
  isLoading: false,
}))

vi.mock('@/stores/vectorIndexStore', () => ({
  useVectorIndexStore: (selector: (s: typeof state) => unknown) => selector(state),
}))
// fileViewerStore is only consumed inside VectorStoreEntryItem (never reached
// by the placeholder branches under test), but mock it to keep the component
// isolated from its real persisted store.
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: (selector: (s: { openFileAtLine: () => void }) => unknown) =>
    selector({ openFileAtLine: () => {} }),
}))

import { VectorSearchResults } from './VectorSearchResults'

describe('VectorSearchResults — index-not-ready placeholder', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    state.status = { state: 'ready', progress: 0, files_indexed: 0, total_files: 0 }
    state.entries = []
    state.isLoading = false
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function render(isSearchMode: boolean) {
    act(() => {
      root.render(<VectorSearchResults isSearchMode={isSearchMode} />)
    })
  }

  it('promises automatic results while indexing with an active query', () => {
    state.status = { state: 'indexing', progress: 0.3, files_indexed: 30, total_files: 100 }
    render(true)
    expect(container.textContent).toContain('results will appear automatically when ready')
  })

  it('stays terse while indexing without an active query', () => {
    state.status = { state: 'indexing', progress: 0.3, files_indexed: 30, total_files: 100 }
    render(false)
    expect(container.textContent).toContain('Indexing in progress')
    expect(container.textContent).not.toContain('results will appear')
  })

  it('reports unavailability regardless of an active query', () => {
    state.status = { state: 'unavailable', progress: 0, files_indexed: 0, total_files: 0 }
    render(true)
    expect(container.textContent).toContain('Vector index unavailable')
  })

  // ADR-122: the suspension keeps the search path OPEN (the breaker stops
  // the UPDATE pipeline only, and a live collection keeps serving the
  // queries the still-enabled inputs fire) — the fetched entries render
  // instead of the "unavailable" placeholder, in all three result states.
  const suspendedStatus = {
    state: 'unavailable' as const,
    error_kind: 'suspended',
    progress: 0.5,
    files_indexed: 50,
    total_files: 100,
  }

  it('renders the fetched results while the ADR-122 suspension holds', () => {
    state.status = { ...suspendedStatus }
    state.entries = [
      { file_path: 'pkg/changed.go', start_line: 1, end_line: 3, content: 'package main', score: 0.9 },
    ]
    render(true)
    expect(container.textContent).not.toContain('Vector index unavailable')
    expect(container.textContent).toContain('changed.go')
    expect(container.textContent).toContain('package main')
  })

  it('renders the no-results state while suspended with an empty result set', () => {
    state.status = { ...suspendedStatus }
    state.entries = []
    render(true)
    expect(container.textContent).not.toContain('Vector index unavailable')
    expect(container.textContent).toContain('No results found')
  })

  it('renders the idle search surface while suspended without an active query', () => {
    state.status = { ...suspendedStatus }
    state.entries = []
    render(false)
    expect(container.textContent).not.toContain('Vector index unavailable')
    expect(container.textContent).not.toContain('No results found')
  })

  // ADR-064: the branch-scoped DB open is not an indexing pass, so it must not
  // borrow the "Indexing in progress" claim.
  it('says "Preparing index" while the DB is opening, with an active query', () => {
    state.status = { state: 'loading', progress: 0, files_indexed: 0, total_files: 0 }
    render(true)
    expect(container.textContent).toContain('Preparing index')
    expect(container.textContent).toContain('results will appear automatically when ready')
    expect(container.textContent).not.toContain('Indexing in progress')
  })

  it('stays terse while the DB is opening without an active query', () => {
    state.status = { state: 'loading', progress: 0, files_indexed: 0, total_files: 0 }
    render(false)
    expect(container.textContent).toContain('Preparing index')
    expect(container.textContent).not.toContain('results will appear')
  })
})
