// @vitest-environment jsdom
//
// Tests for the EmbeddedModelStatus status-bar block — WIRING ONLY.
//
// The division of labour is deliberate and recorded in BOTH suite headers:
//
//   • lib/embeddedModelView.test.ts (renderer-free) OWNS every derivation rule —
//     which surface a snapshot selects, when there is nothing to say, the exact
//     words of each tooltip, the per-artifact (never aggregated, never invented)
//     progress fraction, and the install ordering that picks the active artifact.
//     A change to a RULE is made there, once.
//   • This suite OWNS the markup and plumbing the pure module cannot see: the
//     leading `Separator` appearing ONLY together with a visible indicator,
//     `data-state`, `role="progressbar"` and the `aria-valuenow` omission for an
//     indeterminate bar, the `truncate`/`max-w-[…]` overflow classes, the
//     `view.label ?? 'Installing…'` markup fallback, the store → component →
//     `deriveView` wiring (snapshot refreshes, event-raised progress, the
//     residency indicator's lifetime across an unload), and the mount /
//     subscription lifecycle including the `backend:ready` retry.
//
// An assertion that merely restates a derivation rule does not belong here: it
// would have to be edited in two suites for one behaviour change.

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
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMPlan } from '@/api/embeddedTuning'
import type { EmbeddedLLMComponent, EmbeddedLLMStage } from '@/types/events'

/** The all-zero measured-topology / plan block (no probe, no recorded plan). */
const EMPTY_PLAN: EmbeddedLLMPlan = {
  recorded: false,
  packing: '',
  kv_type: '',
  context_size: 0,
  fit: false,
  fit_arg: '',
  fit_target_mib: 0,
  fit_min_context: 0,
  offload_mode: 'auto',
  layers: -1,
  kv_offload: false,
  mmproj_offload: false,
  parallel: 0,
  cache_ram_mib: -1,
  gpu_family: '',
  device_budget_mib: 0,
  host_budget_mib: 0,
  expected_device_mib: 0,
  expected_host_mib: 0,
  notes: [],
}

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
    devices: [],
    unified: false,
    host_ram_gib: 0,
    device_budget_mib: 0,
    host_budget_mib: 0,
    topology_probed_at: '',
    plan: EMPTY_PLAN,
    reload_required: false,
    pid: 0,
    error: '',
    install_error: '',
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

// WHEN the block has nothing to say is a derivation rule, pinned case by case by
// lib/embeddedModelView.test.ts (`deriveView` → null). This suite pins only what
// null must mean in the MARKUP: no element, no text, and above all no stray
// leading `Separator` left behind in the status bar.
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
    // WIRING: the tooltip the pure derivation built is bound to the element. Its
    // exact wording (artifact label, stage, the byte pair the compact label has
    // no room for) is pinned by lib/embeddedModelView.test.ts.
    expect(el!.getAttribute('title')).toContain('Downloading')
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

  // Which artifact is active — the install order, moving on once one is `done` —
  // is a DERIVATION rule and is pinned by lib/embeddedModelView.test.ts
  // (`activeProgress`). The two cases above are what this suite adds: a store
  // progress event actually reaches `deriveView`, and the fraction it returns is
  // painted into `aria-valuenow` and the `%` span.

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

    // WIRING: the tooltip the pure derivation built is bound to the element, and
    // the name is truncated rather than allowed to stretch the bar. The tooltip's
    // CONTENTS — identity, context, endpoint, pid and the residency policy, the
    // idle timer on or off — are pinned by lib/embeddedModelView.test.ts
    // (`loadedTitle`); restating them here would mean editing two suites for one
    // wording change.
    expect(el!.getAttribute('title')).toContain('Embedded model resident')
    const name = el!.querySelector('span.truncate')
    expect(name?.textContent).toBe('Bonsai 2 27B')
    expect(name?.className).toContain('max-w-[160px]')
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

// --- Residency throughput wiring ------------------------------------------

// The WHICH/WHEN of the throughput metric (model gate, >= 3 samples, the exact
// tooltip fragment) is a derivation rule pinned by lib/embeddedModelView.test.ts.
// This suite pins only the plumbing: the ACTIVE session's chatStore tokens
// reach `deriveView` live, and a session without the metric renders exactly
// the pre-metric surface. (Every other describe above already runs with the
// real sessionStore/chatStore holding no active session — the no-tokens
// baseline for "renders exactly as before".)
describe('EmbeddedModelStatus — residency throughput wiring', () => {
  afterEach(() => {
    act(() => {
      useSessionStore.setState({ activeSessionId: null })
      useChatStore.setState({ sessionTokens: {} })
    })
  })

  it('paints the active session’s median rate as it arrives in the store', async () => {
    act(() => {
      useSessionStore.getState().setActiveSessionId('s-1')
      useChatStore.getState().setSessionTokens('s-1', {
        total_input_tokens: 12_000,
        total_output_tokens: 3_400,
        model: 'embedded/Bonsai 2 27B',
        family: 'embedded',
      })
    })
    await mountWith(LOADED)
    expect(block()!.getAttribute('data-state')).toBe('loaded')
    // No metric yet: the pre-metric surface — name only, no rate span.
    expect(block()!.textContent).toContain('Bonsai 2 27B')
    expect(block()!.textContent).not.toContain('tok/s')

    // The session_tokens event lands AFTER mount; the store selector is the
    // subscription, so the rate paints without a remount.
    act(() => {
      useChatStore.getState().setSessionTokens('s-1', {
        total_input_tokens: 12_500,
        total_output_tokens: 3_600,
        model: 'embedded/Bonsai 2 27B',
        family: 'embedded',
        median_output_tok_s: 27.44,
        tok_s_samples: 5,
      })
    })

    const el = block()
    expect(el?.textContent).toContain('Bonsai 2 27B')
    expect(el?.textContent).toContain('27.4 tok/s')
    expect(el?.getAttribute('title')).toContain('median of last 5 samples')
  })

  it('renders the pre-metric surface while the active session runs another model', async () => {
    // The tokens reach the derivation and the gate holds through the whole
    // wiring: a foreign-model session never paints a rate here.
    act(() => {
      useSessionStore.getState().setActiveSessionId('s-2')
      useChatStore.getState().setSessionTokens('s-2', {
        total_input_tokens: 10,
        total_output_tokens: 5,
        model: 'qwen3.6-35b-a3b',
        family: 'openai_compatible',
        median_output_tok_s: 99.9,
        tok_s_samples: 12,
      })
    })
    await mountWith(LOADED)

    const el = block()
    expect(el?.getAttribute('data-state')).toBe('loaded')
    expect(el?.textContent).toContain('Bonsai 2 27B')
    expect(el?.textContent).not.toContain('tok/s')
    expect(el?.getAttribute('title')).not.toContain('median of last')
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
    expect(seps()).toHaveLength(1)

    // WIRING: the message is truncated instead of stretching the bar, and the
    // tooltip is bound. Its exact wording — including the "Settings → LLM →
    // Embedded LLM" pointer and the empty-message fallback — is pinned by
    // lib/embeddedModelView.test.ts.
    const msg = el!.querySelector('span.truncate')
    expect(msg?.textContent).toBe('llama-server exited after 3s: vulkan driver refused the device')
    expect(msg?.className).toContain('max-w-[200px]')
    expect(el!.getAttribute('title')).toContain('vulkan driver refused the device')
  })

  it('surfaces a failed install even though nothing is installed', async () => {
    await mountWith(
      statusWith({ state: 'not_installed', error: 'checksum mismatch for the model weights' }),
    )

    // WIRING: the error surface renders — separator and all — for a snapshot the
    // block stays silent about in every other respect. That it SHOULD speak here
    // is the derivation rule, pinned by lib/embeddedModelView.test.ts ("surfaces
    // a failure message even while nothing is installed").
    expect(block()!.getAttribute('data-state')).toBe('error')
    expect(block()!.textContent).toContain('checksum mismatch')
    expect(seps()).toHaveLength(1)
  })

  // That a live install run OUTRANKS an erroring snapshot is a derivation rule,
  // pinned by lib/embeddedModelView.test.ts ("gives way to a live install run",
  // "outranks every snapshot, including a loaded one"). The event-raised
  // `installing` flag reaching this component at all is pinned by the
  // "from a progress event alone" case above and by stores/embeddedLLMStore.test.ts.
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
