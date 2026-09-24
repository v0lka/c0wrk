// @vitest-environment jsdom
//
// Tests for the EmbeddedModelStatus status-bar block: the surface it renders per
// embedded-LLM state (install download → weight load → residency → error), the
// separator it owns appearing ONLY together with a visible indicator, the
// per-artifact (never aggregated, never invented) progress fraction, and the
// residency indicator's lifetime — it stays across refreshes and disappears on
// an unload, manual or idle-timer driven.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The status getter is the block's only data source; the real event
// subscriptions stay in place (they no-op without window.runtime, which jsdom
// does not provide).
const getStatusMock = vi.hoisted(() => vi.fn<() => Promise<unknown>>())
vi.mock('@/api/embedded', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/embedded')>()
  return { ...actual, getEmbeddedLLMStatus: () => getStatusMock() }
})

// `backend:ready` handlers are captured so a test can fire the retry the block
// registers for the startup-ordering race; every other event keeps the real
// (no-op here) transport.
const readyHandlers = vi.hoisted(() => new Set<() => void>())
vi.mock('@/api/runtime', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/runtime')>()
  return {
    ...actual,
    subscribe: (event: string, cb: (...args: unknown[]) => void) => {
      if (event === 'backend:ready') {
        readyHandlers.add(cb)
        return () => {
          readyHandlers.delete(cb)
        }
      }
      return actual.subscribe(event, cb)
    },
  }
})

// The block under test logs through @/lib/logger on a failed read; the store's
// own test mocks it the same way so an expected warning is not test noise.
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// Countable separator stub (the UI primitive itself is not under test).
vi.mock('@/components/ui/separator', () => ({
  Separator: () => <span data-testid="sep" />,
}))

import { EmbeddedModelStatus } from './EmbeddedModelStatus'
import { useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMComponent, EmbeddedLLMStage } from '@/types/events'

// --- Fixtures -------------------------------------------------------------

function statusWith(overrides: Partial<EmbeddedLLMStatus> = {}): EmbeddedLLMStatus {
  return {
    state: 'not_installed',
    installed: false,
    installing: false,
    loading: false,
    loaded: false,
    packing: '',
    backend: '',
    port: 0,
    context_size: 0,
    auto_unload_enabled: true,
    auto_unload_minutes: 60,
    idle_remaining_seconds: 0,
    base_url: '',
    model_id: '',
    model_name: '',
    runtime_version: '',
    installed_at: '',
    model_file: '',
    pid: 0,
    error: '',
    available: true,
    ...overrides,
  }
}

/** Installed, resident, serving — the state the residency indicator describes. */
const LOADED = statusWith({
  state: 'loaded',
  installed: true,
  loaded: true,
  packing: 'PTQ1_0',
  backend: 'vulkan',
  port: 52341,
  context_size: 16384,
  base_url: 'http://127.0.0.1:52341/v1',
  model_id: 'embedded/Bonsai 2 27B',
  model_name: 'Bonsai 2 27B',
  runtime_version: 'prism-b10709-9a9394a',
  installed_at: '2026-09-01T10:00:00Z',
  model_file: '/home/u/.c0wrk/models/bonsai-2-27b/Ternary-Bonsai-2-27B-PTQ1_0.gguf',
  pid: 4123,
  auto_unload_enabled: true,
  auto_unload_minutes: 60,
  idle_remaining_seconds: 3599,
})

/** Installed, stopped — the bytes are on disk, nothing is resident. */
const INSTALLED_STOPPED = statusWith({
  state: 'installed',
  installed: true,
  packing: 'PQ2_0',
  backend: 'metal',
  port: 52341,
  context_size: 32768,
  base_url: 'http://127.0.0.1:52341/v1',
  model_id: 'embedded/Bonsai 2 27B',
  model_name: 'Bonsai 2 27B',
  runtime_version: 'prism-b10709-9a9394a',
})

const MODEL_BYTES_TOTAL = 6_710_886_400
const MODEL_BYTES_42PCT = 2_818_572_288 // exactly 42% of the above

function progress(
  component: EmbeddedLLMComponent,
  stage: EmbeddedLLMStage,
  bytes_done = 0,
  bytes_total = 0,
) {
  act(() => {
    useEmbeddedLLMStore.getState().applyProgress({ component, stage, bytes_done, bytes_total })
  })
}

function snapshot(status: EmbeddedLLMStatus) {
  act(() => {
    useEmbeddedLLMStore.getState().setStatus(status)
  })
}

// --- Harness --------------------------------------------------------------

let container: HTMLElement
let root: Root

/** Mount and let the initial authoritative read settle inside act. */
const mountWith = async (status: EmbeddedLLMStatus) => {
  getStatusMock.mockResolvedValue(status)
  await act(async () => {
    root.render(<EmbeddedModelStatus />)
  })
}

const block = (): HTMLElement | null =>
  container.querySelector<HTMLElement>('[data-testid="embedded-model-status"]')
const bar = (): HTMLElement | null =>
  container.querySelector<HTMLElement>('[data-testid="embedded-model-progress"]')
const seps = () => container.querySelectorAll('[data-testid="sep"]')

beforeEach(() => {
  getStatusMock.mockReset()
  readyHandlers.clear()
  act(() => {
    useEmbeddedLLMStore.getState().reset()
  })
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
})

// --- Absent surfaces ------------------------------------------------------

describe('EmbeddedModelStatus — when there is nothing to say', () => {
  it('renders nothing — separator included — before any status arrives', async () => {
    // A read that never settles: the block has no snapshot at all.
    getStatusMock.mockReturnValue(new Promise(() => {}))
    await act(async () => {
      root.render(<EmbeddedModelStatus />)
    })

    expect(container.firstElementChild).toBeNull()
    expect(seps()).toHaveLength(0)
    expect(container.textContent).toBe('')
  })

  it('renders nothing — separator included — when the model is not installed', async () => {
    await mountWith(statusWith())

    expect(block()).toBeNull()
    expect(seps()).toHaveLength(0)
    expect(container.textContent).toBe('')
  })

  it('renders nothing when the subsystem is not available yet', async () => {
    await mountWith(statusWith({ available: false }))

    expect(block()).toBeNull()
    expect(seps()).toHaveLength(0)
  })

  it('renders nothing while the model is installed but stopped', async () => {
    await mountWith(INSTALLED_STOPPED)

    // An idle install is not an event: the Settings block is where it is acted
    // on, so the bar stays clean (and leaves no separator behind).
    expect(block()).toBeNull()
    expect(seps()).toHaveLength(0)
  })

  it('keeps a failed read silent: the block stays hidden and nothing throws', async () => {
    getStatusMock.mockRejectedValue(new Error('Wails App bindings are not available'))
    await act(async () => {
      root.render(<EmbeddedModelStatus />)
    })

    expect(block()).toBeNull()
    expect(seps()).toHaveLength(0)
  })
})

// --- Install (download) surface -------------------------------------------

describe('EmbeddedModelStatus — install download', () => {
  it('renders a progress bar with the active artifact’s own percent while installing', async () => {
    await mountWith(statusWith({ installing: true }))
    progress('model', 'downloading', MODEL_BYTES_42PCT, MODEL_BYTES_TOTAL)

    const el = block()
    expect(el).not.toBeNull()
    expect(el!.getAttribute('data-state')).toBe('install')
    expect(seps()).toHaveLength(1)

    const progressbar = bar()
    expect(progressbar).not.toBeNull()
    expect(progressbar!.getAttribute('role')).toBe('progressbar')
    expect(progressbar!.getAttribute('aria-valuenow')).toBe('42')
    expect(el!.textContent).toContain('Model weights')
    expect(el!.textContent).toContain('42%')
    // The tooltip carries the bytes the compact label has no room for.
    expect(el!.getAttribute('title')).toMatch(/Model weights: Downloading \(.+ \/ .+\)/)
  })

  it('advances the bar as bytes arrive', async () => {
    await mountWith(statusWith({ installing: true }))

    progress('model', 'downloading', 671_088_640, MODEL_BYTES_TOTAL)
    expect(bar()!.getAttribute('aria-valuenow')).toBe('10')
    expect(block()!.textContent).toContain('10%')

    progress('model', 'downloading', 3_690_987_520, MODEL_BYTES_TOTAL)
    expect(bar()!.getAttribute('aria-valuenow')).toBe('55')
    expect(block()!.textContent).toContain('55%')
  })

  it('moves on to the next artifact once the previous one is done', async () => {
    await mountWith(statusWith({ installing: true }))

    progress('runtime', 'downloading', 50_000_000, 100_000_000)
    expect(block()!.textContent).toContain('Inference runtime')
    expect(bar()!.getAttribute('aria-valuenow')).toBe('50')

    progress('runtime', 'done')
    progress('model', 'downloading', 671_088_640, MODEL_BYTES_TOTAL)

    // The finished runtime must not keep the bar: the weights are in flight.
    expect(block()!.textContent).toContain('Model weights')
    expect(block()!.textContent).not.toContain('Inference runtime')
    expect(bar()!.getAttribute('aria-valuenow')).toBe('10')
  })

  it('renders an indeterminate bar — no invented percentage — for a byte-less stage', async () => {
    await mountWith(statusWith({ installing: true }))
    progress('runtime', 'verifying')

    const progressbar = bar()
    expect(progressbar).not.toBeNull()
    expect(progressbar!.getAttribute('aria-valuenow')).toBeNull()
    expect(block()!.textContent).not.toContain('%')
    expect(block()!.textContent).toContain('Inference runtime')
    expect(block()!.getAttribute('title')).toContain('Verifying')
  })

  it('renders an indeterminate bar before the first progress event of a run', async () => {
    await mountWith(statusWith({ installing: true }))

    expect(bar()).not.toBeNull()
    expect(bar()!.getAttribute('aria-valuenow')).toBeNull()
    expect(block()!.textContent).toContain('Installing')
  })

  it('shows the install surface from a progress event alone, before any snapshot refresh', async () => {
    // The store raises `installing` from the event itself: a progress payload IS
    // the proof of a live run and can precede a stale snapshot.
    await mountWith(statusWith())
    expect(block()).toBeNull()

    progress('mmproj', 'downloading', 300_000_000, 600_000_000)

    expect(block()!.getAttribute('data-state')).toBe('install')
    expect(block()!.textContent).toContain('Vision projector')
    expect(bar()!.getAttribute('aria-valuenow')).toBe('50')
  })
})

// --- Weight-load surface ---------------------------------------------------

describe('EmbeddedModelStatus — weight load', () => {
  it('renders an indeterminate bar while the weights are loading', async () => {
    await mountWith(
      statusWith({ ...INSTALLED_STOPPED, state: 'loading', loading: true }),
    )

    const el = block()
    expect(el!.getAttribute('data-state')).toBe('loading')
    expect(el!.textContent).toContain('Loading model')
    // LoadEmbeddedLLM reports no fraction — only the state — so no percentage.
    expect(bar()).not.toBeNull()
    expect(bar()!.getAttribute('aria-valuenow')).toBeNull()
    expect(el!.textContent).not.toContain('%')
    expect(seps()).toHaveLength(1)
  })
})

// --- Residency surface -----------------------------------------------------

describe('EmbeddedModelStatus — residency indicator', () => {
  it('renders the resident model with its identity in the tooltip once loaded', async () => {
    await mountWith(LOADED)

    const el = block()
    expect(el).not.toBeNull()
    expect(el!.getAttribute('data-state')).toBe('loaded')
    expect(el!.textContent).toContain('Bonsai 2 27B')
    expect(bar()).toBeNull()
    expect(seps()).toHaveLength(1)

    const title = el!.getAttribute('title') ?? ''
    expect(title).toContain('resident')
    expect(title).toContain('PTQ1_0/vulkan')
    expect(title).toContain('context 16384')
    expect(title).toContain('http://127.0.0.1:52341/v1')
    expect(title).toContain('pid 4123')
    expect(title).toContain('auto-unloads after 60 min idle')
  })

  it('states the residency policy when the idle timer is off', async () => {
    await mountWith({ ...LOADED, auto_unload_enabled: false })

    expect(block()!.getAttribute('title')).toContain('stays resident until unloaded')
  })

  it('keeps the indicator across snapshot refreshes until the model is unloaded', async () => {
    await mountWith(LOADED)
    expect(block()!.getAttribute('data-state')).toBe('loaded')

    // A refresh that only moves the idle budget must not drop the indicator…
    snapshot({ ...LOADED, idle_remaining_seconds: 1200 })
    expect(block()!.getAttribute('data-state')).toBe('loaded')
    // …and neither does the budget reaching zero: `loaded` is the authority,
    // not a countdown this block does not own.
    snapshot({ ...LOADED, idle_remaining_seconds: 0 })
    expect(block()!.getAttribute('data-state')).toBe('loaded')

    // A manual Unload does.
    snapshot({ ...INSTALLED_STOPPED, state: 'unloading' })
    expect(block()).toBeNull()
    expect(seps()).toHaveLength(0)

    snapshot(INSTALLED_STOPPED)
    expect(block()).toBeNull()
    expect(seps()).toHaveLength(0)
  })

  it('drops the indicator when the idle timer unloads the model', async () => {
    await mountWith(LOADED)
    expect(block()!.getAttribute('data-state')).toBe('loaded')

    // The supervisor stops the process on idle: same transition, no user click.
    snapshot({ ...INSTALLED_STOPPED, state: 'unloading' })
    snapshot({ ...INSTALLED_STOPPED, idle_remaining_seconds: 0 })

    expect(block()).toBeNull()
    expect(seps()).toHaveLength(0)
    expect(container.textContent).toBe('')
  })

  it('comes back when the model is loaded again', async () => {
    await mountWith(INSTALLED_STOPPED)
    expect(block()).toBeNull()

    snapshot({ ...INSTALLED_STOPPED, state: 'loading', loading: true })
    expect(block()!.getAttribute('data-state')).toBe('loading')

    snapshot(LOADED)
    expect(block()!.getAttribute('data-state')).toBe('loaded')
    expect(block()!.textContent).toContain('Bonsai 2 27B')
  })
})

// --- Error surface ---------------------------------------------------------

describe('EmbeddedModelStatus — error', () => {
  it('renders an explicit hint carrying the supervisor’s message', async () => {
    await mountWith(
      statusWith({
        ...INSTALLED_STOPPED,
        state: 'error',
        error: 'llama-server exited after 3s: vulkan driver refused the device',
      }),
    )

    const el = block()
    expect(el!.getAttribute('data-state')).toBe('error')
    expect(el!.textContent).toContain('vulkan driver refused the device')
    expect(el!.getAttribute('title')).toContain('Settings → LLM')
    expect(seps()).toHaveLength(1)
  })

  it('surfaces a failed install even though nothing is installed', async () => {
    await mountWith(
      statusWith({ state: 'not_installed', error: 'checksum mismatch for the model weights' }),
    )

    // This is the one case where a NOT-installed snapshot still has something
    // to say: the failure is otherwise invisible outside the Settings dialog.
    expect(block()!.getAttribute('data-state')).toBe('error')
    expect(block()!.textContent).toContain('checksum mismatch')
    expect(seps()).toHaveLength(1)
  })

  it('gives way to a live install run', async () => {
    await mountWith(statusWith({ state: 'error', error: 'previous run failed' }))
    expect(block()!.getAttribute('data-state')).toBe('error')

    snapshot(statusWith({ installing: true }))
    progress('runtime', 'downloading', 10_000_000, 100_000_000)

    expect(block()!.getAttribute('data-state')).toBe('install')
    expect(block()!.textContent).not.toContain('previous run failed')
  })
})

// --- Lifecycle -------------------------------------------------------------

describe('EmbeddedModelStatus — lifecycle', () => {
  it('reads the authoritative status once on mount and releases its subscription on unmount', async () => {
    await mountWith(LOADED)
    expect(getStatusMock).toHaveBeenCalledTimes(1)
    expect(block()!.getAttribute('data-state')).toBe('loaded')

    act(() => {
      root.unmount()
    })
    expect(readyHandlers.size).toBe(0)

    // A remount reads again — the shared event subscription is refcounted in the
    // store, so mounting next to the Settings block never double-applies.
    root = createRoot(container)
    await mountWith(LOADED)
    expect(getStatusMock).toHaveBeenCalledTimes(2)
  })

  it('retries the read on backend:ready when the first one raced startup', async () => {
    // The startup snapshot event is emitted in desktop phase 5 and can precede
    // the first paint; the retry on `backend:ready` is what recovers it.
    getStatusMock.mockReturnValue(new Promise(() => {}))
    await act(async () => {
      root.render(<EmbeddedModelStatus />)
    })
    expect(block()).toBeNull()
    expect(readyHandlers.size).toBe(1)

    getStatusMock.mockResolvedValue(LOADED)
    await act(async () => {
      for (const handler of Array.from(readyHandlers)) handler()
      await Promise.resolve()
    })

    expect(block()!.getAttribute('data-state')).toBe('loaded')
    expect(block()!.textContent).toContain('Bonsai 2 27B')
  })
})
