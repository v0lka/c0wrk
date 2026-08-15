// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Controllable store state. VectorSearchResults uses the hook form
// `useVectorIndexStore(selector)`, so the mock applies the selector to this
// shared object. Mocking (rather than the real persisted store) avoids the
// zustand `persist` middleware crashing under jsdom when localStorage is
// undefined.
const state = vi.hoisted(() => ({
  status: {
    state: 'ready' as 'idle' | 'indexing' | 'ready' | 'reindexing' | 'unavailable',
    progress: 0,
    files_indexed: 0,
    total_files: 0,
  },
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
})
