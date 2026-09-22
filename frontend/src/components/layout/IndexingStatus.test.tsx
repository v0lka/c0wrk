// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { VectorIndexStatus } from '@/types/models'

// Controllable store state. IndexingStatus reads the whole status via
// `useVectorIndexStore((s) => s.status)`, so the mock applies the selector to
// this shared object. Mocking (rather than the real persisted store) avoids
// zustand `persist` crashing under jsdom when localStorage is undefined.
const state = vi.hoisted(() => ({
  status: {
    state: 'idle' as VectorIndexStatus['state'],
    progress: 0,
    files_indexed: 0,
    total_files: 0,
  } as VectorIndexStatus,
}))

vi.mock('@/stores/vectorIndexStore', () => ({
  useVectorIndexStore: (selector: (s: typeof state) => unknown) => selector(state),
}))

import { IndexingStatus } from './IndexingStatus'

// The fill is the only element that renders an inline `width` style.
function fillElement(container: HTMLElement): HTMLElement {
  const fill = Array.from(container.querySelectorAll<HTMLElement>('div')).find(
    (el) => el.className.includes('h-full') && el.className.includes('rounded-full'),
  )
  if (!fill) throw new Error('progress-bar fill element not found')
  return fill
}

describe('IndexingStatus — progress bar', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    state.status = { state: 'idle', progress: 0, files_indexed: 0, total_files: 0 }
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function render() {
    act(() => {
      root.render(<IndexingStatus />)
    })
  }

  it('renders the fill width from a 0–1 progress fraction', () => {
    state.status = { state: 'indexing', progress: 0.3, files_indexed: 30, total_files: 100 }
    render()
    // `progress` is a fraction: 0.3 → 30%, not a full bar.
    expect(fillElement(container).style.width).toBe('30%')
    expect(container.textContent).toContain('30/100')
  })

  it('keeps the bar near empty at the very start (guards the percent/fraction regression)', () => {
    // The backend once emitted 0–100 here; combined with the component's
    // `progress * 100` that turned ~1% indexed into a full bar that stayed
    // pinned until indexing finished. A fraction of 0.01 must stay at 1%.
    state.status = { state: 'indexing', progress: 0.01, files_indexed: 1, total_files: 100 }
    render()
    expect(fillElement(container).style.width).toBe('1%')
  })

  it('hides itself when the index is idle', () => {
    state.status = { state: 'idle', progress: 0, files_indexed: 0, total_files: 0 }
    render()
    expect(container.textContent).toBe('')
  })

  it('renders an honest "Preparing index…" while the DB is opening (ADR-064, no fake bar)', () => {
    state.status = { state: 'loading', phase: 'open', progress: 0, files_indexed: 0, total_files: 0 }
    render()
    expect(container.textContent).toContain('Preparing index…')
    expect(container.textContent).not.toContain('Indexing')
    // No progress bar and no n/m while only opening.
    expect(() => fillElement(container)).toThrow()
  })
})
