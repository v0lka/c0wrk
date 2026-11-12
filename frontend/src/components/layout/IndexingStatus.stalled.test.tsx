// @vitest-environment jsdom
//
// Tests for the status-bar pill's stalled overlay (backend passStallTimeout:
// an indexing pass with no progress for 5 minutes) and its "Collect
// diagnostics" action (DumpVectorIndexDiagnostics). The contract under test
// (specs/domains/frontend/loading-states.md): the stalled evidence renders in
// the LOADING class — same muted foreground as the hosting pill, its own
// native title, never the `unavailable` failure shape — and only while the
// stall window is open (the wire field is absent once the pass's next
// progress point resolves it). The action latches while the RPC is in
// flight, surfaces the written path on success, and reports a rejection
// through the app-wide runtime_error toast instead of a silent no-op.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The pill must never reach the Wails backend: mock the diagnostics RPC
// under test and the runtime emit (a rejected RPC is surfaced as a
// runtime_error toast — spied so tests can assert it stays silent on the
// happy path).
vi.mock('@/api/vector', () => ({
  dumpVectorIndexDiagnostics: dumpMock,
}))
vi.mock('@/api/runtime', () => ({
  emit: emitMock,
}))
const dumpMock = vi.hoisted(() => vi.fn())
const emitMock = vi.hoisted(() => vi.fn())

// Controllable store state — IndexingStatus reads the whole status via
// `useVectorIndexStore((s) => s.status)` (mocked like IndexingStatus.test.tsx
// to avoid zustand persist under jsdom without localStorage).
const state = vi.hoisted(() => ({
  status: {
    state: 'idle' as import('@/types/models').VectorIndexStatus['state'],
    progress: 0,
    files_indexed: 0,
    total_files: 0,
  } as import('@/types/models').VectorIndexStatus,
}))

vi.mock('@/stores/vectorIndexStore', () => ({
  useVectorIndexStore: (selector: (s: typeof state) => unknown) => selector(state),
}))

import { IndexingStatus } from './IndexingStatus'
import type { VectorIndexStatus } from '@/types/models'

function stalledIndexing(overrides: Partial<VectorIndexStatus> = {}): VectorIndexStatus {
  return {
    state: 'indexing',
    progress: 0.3,
    files_indexed: 30,
    total_files: 100,
    stalled: true,
    stalled_stage: 'embedding',
    stalled_seconds: 335,
    ...overrides,
  }
}

function collectButton(container: HTMLElement): HTMLButtonElement {
  // The pill hosts exactly one button — the action. Its LABEL changes with
  // state ("Collect diagnostics" / "Collecting…"), so match the element.
  const btn = container.querySelector<HTMLButtonElement>('button')
  if (!btn) {
    throw new Error('Collect diagnostics button not found')
  }
  return btn
}

describe('IndexingStatus — stalled overlay + Collect diagnostics', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    state.status = { state: 'idle', progress: 0, files_indexed: 0, total_files: 0 }
    dumpMock.mockReset()
    emitMock.mockReset()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
    vi.clearAllMocks()
  })

  function render() {
    act(() => {
      root.render(<IndexingStatus />)
    })
  }

  it('renders the stalled evidence in the loading class — muted, never the unavailable/warning shape', () => {
    state.status = stalledIndexing()
    render()
    expect(container.textContent).toContain('Stalled — no progress for 5m 35s')
    expect(container.textContent).toContain('Collect diagnostics')
    // Loading class: the hosting pill's muted foreground, no warning text,
    // no "Index unavailable" label.
    expect(container.textContent).not.toContain('Index unavailable')
    expect(container.querySelector('.text-warning')).toBeNull()
    // The evidence lives on its own segment's title; the hosting pill keeps
    // its plain legend (dots) tooltip.
    const chip = Array.from(container.querySelectorAll<HTMLElement>('span')).find((el) =>
      el.title.startsWith('Indexing pass stalled'),
    )
    expect(chip).not.toBeNull()
    expect(chip?.title).toContain('no progress for 5m 35s')
    expect(chip?.title).toContain('Stage: embedding')
    expect(chip?.title).toContain('~/.c0wrk/diagnostics/')
  })

  it('formats the silence duration off stalled_seconds (0 absent-safe)', () => {
    state.status = stalledIndexing({ stalled_seconds: undefined })
    render()
    expect(container.textContent).toContain('Stalled — no progress for 0s')

    state.status = stalledIndexing({ stalled_seconds: 45 })
    render()
    expect(container.textContent).toContain('Stalled — no progress for 45s')

    state.status = stalledIndexing({ stalled_seconds: 300 })
    render()
    expect(container.textContent).toContain('Stalled — no progress for 5m')
  })

  it('renders only the plain pill when the stall window is not open', () => {
    state.status = { state: 'indexing', progress: 0.3, files_indexed: 30, total_files: 100 }
    render()
    expect(container.textContent).not.toContain('Stalled')
    expect(() => collectButton(container)).toThrow()
  })

  it('collects diagnostics on click: RPC fired once, busy latch while in flight', async () => {
    state.status = stalledIndexing()
    let settle!: (v: { path: string; bytes: number; truncated: boolean }) => void
    dumpMock.mockReturnValue(
      new Promise((resolve) => {
        settle = resolve
      }),
    )
    render()
    const btn = collectButton(container)
    expect(btn.disabled).toBe(false)

    await act(async () => {
      btn.click()
    })
    expect(dumpMock).toHaveBeenCalledTimes(1)
    // Busy while the RPC is in flight: relabelled and disabled — a second
    // click must not fire a duplicate RPC.
    expect(container.textContent).toContain('Collecting…')
    expect(collectButton(container).disabled).toBe(true)
    await act(async () => {
      collectButton(container).click()
    })
    expect(dumpMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      settle({ path: '/h/.c0wrk/diagnostics/vector-index-x.txt', bytes: 10, truncated: true })
    })
    // Resolved: label back, saved path surfaced in the title, truncated
    // flagged there too.
    expect(container.textContent).toContain('Collect diagnostics')
    expect(collectButton(container).disabled).toBe(false)
    expect(collectButton(container).title).toContain('vector-index-x.txt')
    expect(collectButton(container).title).toContain('collect again')
    // The app-wide error toast stays silent on the happy path.
    expect(emitMock).not.toHaveBeenCalled()
  })

  it('surfaces a rejected RPC through the runtime_error toast and allows a retry', async () => {
    state.status = stalledIndexing()
    dumpMock.mockRejectedValueOnce(new Error('vector index not available: shutting down'))
    render()
    const btn = collectButton(container)
    await act(async () => {
      btn.click()
    })
    expect(emitMock).toHaveBeenCalledTimes(1)
    const call = emitMock.mock.calls[0]
    if (!call) throw new Error('runtime_error emit missing')
    const [event, payload] = call
    expect(event).toBe('runtime_error')
    expect((payload as { message: string }).message).toContain('Collect diagnostics failed:')
    expect((payload as { message: string }).message).toContain('shutting down')
    // The latch released — a retry is possible.
    expect(collectButton(container).disabled).toBe(false)
  })

  it('renders the overlay for the reindexing pill too (a stalled repair pass is still a pass)', () => {
    state.status = stalledIndexing({ state: 'reindexing' })
    render()
    expect(container.textContent).toContain('Stalled — no progress for 5m 35s')
    expect(container.textContent).toContain('Collect diagnostics')
  })
})
