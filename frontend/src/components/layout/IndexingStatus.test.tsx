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

  it('renders "Preparing index…" for the preparing attach window (ADR-104 stage 2, no fake bar)', () => {
    // The view-model attach window (snapshot identity resolution, corpus
    // lease acquisition): pre-work, not a pass — the same honest shape as
    // `loading`, never the "Indexing…" 0% bar.
    state.status = { state: 'preparing', phase: 'preparation', progress: 0, files_indexed: 0, total_files: 0 }
    render()
    expect(container.textContent).toContain('Preparing index…')
    expect(container.textContent).not.toContain('Indexing')
    expect(() => fillElement(container)).toThrow()
  })

  it('renders "Migrating store…" for the migration-rebuild promotion (ADR-104 stage 2, no fake bar)', () => {
    // The one-time legacy store promotion: store-shape repair, not fresh
    // content — its own honest label, never the "Indexing…" progress bar.
    state.status = { state: 'migration-rebuild', phase: 'migration-rebuild', progress: 0, files_indexed: 0, total_files: 0 }
    render()
    expect(container.textContent).toContain('Migrating store…')
    expect(container.textContent).not.toContain('Indexing')
    expect(() => fillElement(container)).toThrow()
  })

  it('renders an honest "Index unavailable" for the terminal unavailable state (no fake bar)', () => {
    // A failed DB open / init failure: nothing is running and nothing can be
    // repaired by a reindex (there is no collection). The state used to fall
    // through to the "Indexing…" 0% bar — the exact dishonesty ADR-064
    // removed for `loading`.
    state.status = { state: 'unavailable', progress: 0, files_indexed: 0, total_files: 0 }
    render()
    expect(container.textContent).toContain('Index unavailable')
    expect(container.textContent).not.toContain('Indexing')
    expect(() => fillElement(container)).toThrow()
  })

  it('renders an explicit "Indexing off" pill for the disabled state (ADR-103, no fake bar)', () => {
    // The master switch is off: an explicit off state with idle dots — never
    // the "Indexing…" 0% bar, and never a stale pre-disable state.
    state.status = { state: 'disabled', progress: 0, files_indexed: 0, total_files: 0 }
    render()
    expect(container.textContent).toContain('Indexing off')
    expect(container.textContent).not.toContain('Indexing...')
    expect(container.textContent).not.toContain('ready')
    expect(() => fillElement(container)).toThrow()
  })

  /** The pill's outer element — the only rendered div carrying a native title. */
  function pill(): HTMLElement {
    const el = container.querySelector<HTMLElement>('div[title]')
    if (!el) throw new Error('status pill not rendered')
    return el
  }

  it('carries the backend cause plus its kind in the unavailable pill tooltip', () => {
    // The "unavailable always explains itself" invariant reaches the UI: the
    // visible label stays short, the hover names the real failure — so a
    // broken index is never a dead end that requires reading service logs.
    state.status = {
      state: 'unavailable',
      progress: 0,
      files_indexed: 0,
      total_files: 0,
      last_error: 'corpus store is corrupt (bad header)',
      error_kind: 'repairable',
    }
    render()
    expect(pill().title).toContain('corpus store is corrupt (bad header)')
    expect(pill().title).toContain('Kind: repairable')
    // Still the short honest label on screen — the diagnosis is the hover.
    expect(container.textContent).toContain('Index unavailable')
    expect(container.textContent).not.toContain('corrupt')
  })

  it('falls back to the classification alone when no cause text is present', () => {
    state.status = {
      state: 'unavailable',
      progress: 0,
      files_indexed: 0,
      total_files: 0,
      error_kind: 'no_target',
    }
    render()
    expect(pill().title).toContain('Kind: no_target')
  })

  it('keeps the plain legend when an unavailable status carries neither cause nor kind', () => {
    state.status = { state: 'unavailable', progress: 0, files_indexed: 0, total_files: 0 }
    render()
    // No invented diagnosis and no dangling separator.
    expect(pill().title).toBe('Vector: idle\nLexical: idle')
  })

  it('never flickers through a progress shape when the index moves ready ↔ unavailable', () => {
    state.status = { state: 'ready', progress: 1, files_indexed: 3, total_files: 3 }
    render()
    expect(container.textContent).toContain('Index ready')

    state.status = {
      state: 'unavailable',
      progress: 0,
      files_indexed: 3,
      total_files: 3,
      last_error: 'failed to open the corpus',
      error_kind: 'fatal',
    }
    render()
    expect(container.textContent).toContain('Index unavailable')
    expect(container.textContent).not.toContain('Indexing')
    // No intermediate bar for a state that is not running a pass.
    expect(() => fillElement(container)).toThrow()

    state.status = { state: 'ready', progress: 1, files_indexed: 3, total_files: 3 }
    render()
    expect(container.textContent).toContain('Index ready')
    expect(container.textContent).not.toContain('unavailable')
    expect(() => fillElement(container)).toThrow()
    // The cause belongs to the failed attempt, not to the recovered index.
    expect(pill().title).not.toContain('failed to open the corpus')
  })
})
