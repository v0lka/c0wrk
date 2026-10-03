// @vitest-environment jsdom
//
// EmbeddedLLMSettings — the Settings block of the embedded local model.
//
// The RPC surface and the two `embedded_llm:*` subscriptions are mocked at the
// @/api/embedded boundary; the store and the component are REAL, so these tests
// cover the whole path an event or a status read takes into the rendered block
// (api → store → selectors → DOM).

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Radix popper/dialog positioning observes its content with ResizeObserver,
// which jsdom does not provide.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

const mocks = vi.hoisted(() => ({
  getEmbeddedLLMStatus: vi.fn(),
  installEmbeddedLLM: vi.fn(),
  cancelEmbeddedLLMInstall: vi.fn(),
  removeEmbeddedLLM: vi.fn(),
  loadEmbeddedLLM: vi.fn(),
  unloadEmbeddedLLM: vi.fn(),
  setEmbeddedLLMAutoUnload: vi.fn(),
  getEmbeddedLLMTuning: vi.fn(),
  setEmbeddedLLMTuning: vi.fn(),
  // Captured subscribers so a test can emit an event at the block.
  stateHandlers: new Set<(data: unknown) => void>(),
  progressHandlers: new Set<(data: unknown) => void>(),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

vi.mock('@/api/embedded', () => ({
  DEFAULT_AUTO_UNLOAD_MINUTES: 60,
  MIN_AUTO_UNLOAD_MINUTES: 1,
  MAX_AUTO_UNLOAD_MINUTES: 525600,
  getEmbeddedLLMStatus: mocks.getEmbeddedLLMStatus,
  installEmbeddedLLM: mocks.installEmbeddedLLM,
  cancelEmbeddedLLMInstall: mocks.cancelEmbeddedLLMInstall,
  removeEmbeddedLLM: mocks.removeEmbeddedLLM,
  loadEmbeddedLLM: mocks.loadEmbeddedLLM,
  unloadEmbeddedLLM: mocks.unloadEmbeddedLLM,
  setEmbeddedLLMAutoUnload: mocks.setEmbeddedLLMAutoUnload,
  onEmbeddedLLMState: (cb: (data: unknown) => void) => {
    mocks.stateHandlers.add(cb)
    return () => {
      mocks.stateHandlers.delete(cb)
    }
  },
  onEmbeddedLLMInstallProgress: (cb: (data: unknown) => void) => {
    mocks.progressHandlers.add(cb)
    return () => {
      mocks.progressHandlers.delete(cb)
    }
  },
}))

// The tuning RPCs the block's hook reaches through (real constants kept).
vi.mock('@/api/embeddedTuning', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/embeddedTuning')>()),
  getEmbeddedLLMTuning: mocks.getEmbeddedLLMTuning,
  setEmbeddedLLMTuning: mocks.setEmbeddedLLMTuning,
}))

import { EmbeddedLLMSettings } from './EmbeddedLLMSettings'
import { useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import { formatBytes } from '@/lib/formatters'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMPlan } from '@/api/embeddedTuning'
import type { EmbeddedLLMInstallProgressData, EmbeddedLLMStateData } from '@/types/events'
// The all-unset tuning snapshot is shared with the two tuning suites so the DTO
// mirror has exactly one test-side definition.
import { makeTuning } from '@/test/embeddedTuningFixture'

let container: HTMLDivElement
let root: Root

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

/** A complete status snapshot (every DTO field is always present) with the
 *  not-installed defaults; a test overrides only what it cares about. */
function makeStatus(overrides: Partial<EmbeddedLLMStatus> = {}): EmbeddedLLMStatus {
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
    auto_unload_enabled: false,
    auto_unload_minutes: 60,
    idle_remaining_seconds: 0,
    base_url: '',
    model_id: '',
    model_name: 'Bonsai 2 27B',
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
    guards: [],
    leftover_runtime: false,
    leftover_weights: false,
    leftover_projection: false,
    ...overrides,
  }
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

async function render(): Promise<void> {
  await act(async () => {
    root.render(<EmbeddedLLMSettings />)
  })
  await flush()
}

/** Query inside the block. */
function q(testId: string): HTMLElement | null {
  return container.querySelector(`[data-testid="${testId}"]`)
}

/** Query anywhere — a Radix dialog portals its content to document.body. */
function dq(testId: string): HTMLElement | null {
  return document.querySelector(`[data-testid="${testId}"]`)
}

function click(el: Element | null): void {
  if (!el) throw new Error('element to click not found')
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

function emitProgress(data: EmbeddedLLMInstallProgressData): void {
  act(() => {
    for (const handler of Array.from(mocks.progressHandlers)) handler(data)
  })
}

async function emitState(data: EmbeddedLLMStateData): Promise<void> {
  act(() => {
    for (const handler of Array.from(mocks.stateHandlers)) handler(data)
  })
  // The block treats a state event as an invalidation and re-reads the status.
  await flush()
}

/** Type into a controlled input through the native setter (the repo idiom —
 *  React tracks the previous value on the node and suppresses a synthetic
 *  change otherwise). */
function typeInto(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  if (!setter) throw new Error('native input setter not found')
  act(() => {
    setter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

// React 17+ delegates onBlur via the native `focusout` event.
function blur(input: HTMLInputElement): Promise<void> {
  return act(async () => {
    input.dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.stateHandlers.clear()
  mocks.progressHandlers.clear()
  mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus())
  mocks.installEmbeddedLLM.mockResolvedValue(undefined)
  mocks.cancelEmbeddedLLMInstall.mockResolvedValue(undefined)
  mocks.removeEmbeddedLLM.mockResolvedValue(undefined)
  mocks.loadEmbeddedLLM.mockResolvedValue(undefined)
  mocks.unloadEmbeddedLLM.mockResolvedValue(undefined)
  mocks.setEmbeddedLLMAutoUnload.mockResolvedValue(undefined)
  mocks.getEmbeddedLLMTuning.mockResolvedValue(makeTuning())
  mocks.setEmbeddedLLMTuning.mockResolvedValue(undefined)
  useEmbeddedLLMStore.getState().reset()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
})

// ─────────────────────────────────────────────────────────────────────────────

describe('EmbeddedLLMSettings — before an install', () => {
  it('renders a single Install action and no Remove / Load / auto-unload', async () => {
    await render()

    const install = q('embedded-llm-install')
    expect(install).not.toBeNull()
    // No tuning surface either — the block's installed-only controls stay away.
    expect(q('embedded-llm-tuning')).toBeNull()
    expect(install).not.toBeNull()
    expect(install?.textContent).toContain('Install')

    // Nothing that only exists once the model is on disk.
    expect(q('embedded-llm-remove')).toBeNull()
    expect(q('embedded-llm-load')).toBeNull()
    expect(q('embedded-llm-unload')).toBeNull()
    expect(q('embedded-llm-auto-unload')).toBeNull()
    expect(q('embedded-llm-auto-unload-minutes')).toBeNull()
    expect(q('embedded-llm-install-record')).toBeNull()
    expect(q('embedded-llm-progress')).toBeNull()
    // Nothing that only exists while an install run is in flight either.
    expect(q('embedded-llm-cancel-install')).toBeNull()
  })

  it('starts the background install on click', async () => {
    await render()
    await act(async () => {
      await clickAsync(q('embedded-llm-install'))
    })

    expect(mocks.installEmbeddedLLM).toHaveBeenCalledTimes(1)
  })

  it('shows a synchronous refusal inline (no toast channel)', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus())
    // The real gate's wording (core/embeddedllm `ErrInsufficientMemory`): the
    // combined accelerator+RAM budget, not a flat RAM floor.
    mocks.installEmbeddedLLM.mockRejectedValue(
      new Error("the embedded LLM does not fit this machine's memory"),
    )
    await render()
    await act(async () => {
      await clickAsync(q('embedded-llm-install'))
    })

    const err = q('embedded-llm-error')
    expect(err).not.toBeNull()
    expect(err?.textContent).toContain('does not fit this machine')
    // A refusal downloaded nothing, so no progress surface appeared.
    expect(q('embedded-llm-progress')).toBeNull()
  })

  it('shows a fatal background install failure from install_error (the status bar does not)', async () => {
    // The backend retries resumable download failures silently; only a FATAL
    // one reaches the snapshot, as the operator-friendly install_error line.
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({
        installing: false,
        install_error:
          'the download server could not be reached after 3 attempts — check the network connection and try again',
      }),
    )
    await render()

    const err = q('embedded-llm-error')
    expect(err).not.toBeNull()
    expect(err?.textContent).toContain('could not be reached after 3 attempts')
    // The failed run is over: the Install button (the retry) is back.
    expect(q('embedded-llm-install')).not.toBeNull()
    expect(q('embedded-llm-progress')).toBeNull()
  })
})

describe('EmbeddedLLMSettings — during an install', () => {
  beforeEach(() => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installing: true }))
  })

  it('renders one progress bar per reporting component, with its own bytes and percent', async () => {
    await render()

    emitProgress({ component: 'runtime', stage: 'downloading', bytes_done: 75_000_000, bytes_total: 150_000_000 })
    emitProgress({ component: 'model', stage: 'downloading', bytes_done: 1_000_000_000, bytes_total: 7_206_168_928 })
    emitProgress({ component: 'mmproj', stage: 'downloading', bytes_done: 300_000_000, bytes_total: 600_000_000 })

    const expectations = [
      { component: 'runtime', done: 75_000_000, total: 150_000_000, pct: '50%' },
      { component: 'model', done: 1_000_000_000, total: 7_206_168_928, pct: '14%' },
      { component: 'mmproj', done: 300_000_000, total: 600_000_000, pct: '50%' },
    ]

    for (const { component, done, total, pct } of expectations) {
      const row = q(`embedded-progress-${component}`)
      expect(row, `row for ${component}`).not.toBeNull()
      // Its OWN bytes — never an aggregate of the other components.
      expect(row?.textContent).toContain(`${formatBytes(done)} / ${formatBytes(total)}`)
      expect(row?.textContent).toContain(pct)
      expect(row?.textContent).toContain('Downloading')
      // A determinate bar with a width.
      const bar = row?.querySelector<HTMLElement>('[style*="width"]')
      expect(bar, `bar for ${component}`).not.toBeNull()
      expect(bar?.style.width).toBe(pct)
    }

    // Three separate rows, in install order.
    const rows = container.querySelectorAll('[data-testid^="embedded-progress-"]')
    expect(rows).toHaveLength(3)
  })

  it('renders no row for a component this machine never fetches (cudart)', async () => {
    await render()
    emitProgress({ component: 'runtime', stage: 'downloading', bytes_done: 1, bytes_total: 2 })

    expect(q('embedded-progress-runtime')).not.toBeNull()
    expect(q('embedded-progress-cudart')).toBeNull()
  })

  it('shows a byte-less stage without inventing a byte count, and Done at the end', async () => {
    await render()
    emitProgress({ component: 'runtime', stage: 'downloading', bytes_done: 150_000_000, bytes_total: 150_000_000 })
    emitProgress({ component: 'runtime', stage: 'verifying', bytes_done: 0, bytes_total: 0 })

    let row = q('embedded-progress-runtime')
    expect(row?.textContent).toContain('Verifying')
    expect(row?.textContent).not.toContain('B /')
    expect(row?.querySelector('[style*="width"]')).toBeNull()

    emitProgress({ component: 'runtime', stage: 'done', bytes_done: 0, bytes_total: 0 })
    row = q('embedded-progress-runtime')
    expect(row?.textContent).toContain('Done')
  })

  it('replaces the bars with the installed surface when the run completes', async () => {
    await render()
    emitProgress({ component: 'model', stage: 'downloading', bytes_done: 1, bytes_total: 10 })
    expect(q('embedded-llm-progress')).not.toBeNull()

    // The completion snapshot is what the state event triggers a re-read of.
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({
        state: 'installed',
        installed: true,
        packing: 'PQ2_0',
        backend: 'metal',
        port: 43211,
        context_size: 32768,
        auto_unload_enabled: true,
        base_url: 'http://127.0.0.1:43211/v1',
        model_id: 'embedded/Bonsai 2 27B',
      }),
    )
    await emitState({
      installed: true,
      loading: false,
      loaded: false,
      packing: 'PQ2_0',
      backend: 'metal',
      port: 43211,
      context_size: 32768,
      auto_unload_minutes: 60,
      error: '',
      install_error: '',
    })

    expect(q('embedded-llm-progress')).toBeNull()
    expect(q('embedded-llm-install-record')).not.toBeNull()
    expect(q('embedded-llm-remove')).not.toBeNull()
  })
})

// The header Cancel action of the installing block. It only DELIVERS the stop
// request: the RPC resolves once the request is in, while the run's actual
// unwind is asynchronous and quiet — its end arrives through
// `embedded_llm:state` (no toast, no recorded error), which re-reads the
// snapshot and drops `installing`, returning the block to the Install button.
describe('EmbeddedLLMSettings — cancelling an install', () => {
  beforeEach(() => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ installing: true }))
  })

  it('offers an enabled Cancel action in the installing block header', async () => {
    await render()

    const cancel = q('embedded-llm-cancel-install') as HTMLButtonElement
    expect(cancel).not.toBeNull()
    // During the download itself the busy window is idle — the install RPC
    // resolved long ago — so Cancel is clickable exactly when there is
    // something to stop.
    expect(cancel.disabled).toBe(false)
    expect(cancel.textContent).toContain('Cancel')
    expect(cancel.textContent).not.toContain('Cancelling…')
    // The rest icon, not a working spinner.
    expect(cancel.querySelector('svg.animate-spin')).toBeNull()
  })

  it('delivers the stop request through the cancel RPC on click', async () => {
    await render()

    await act(async () => {
      await clickAsync(q('embedded-llm-cancel-install'))
    })

    expect(mocks.cancelEmbeddedLLMInstall).toHaveBeenCalledTimes(1)
    expect(mocks.installEmbeddedLLM).not.toHaveBeenCalled()
  })

  it('disables and spinners the button for the whole cancel window', async () => {
    await render()

    // Hold the stop request in flight: the busy window stays open until the
    // post-action read-back lands, and the button reflects it live.
    let release: () => void = () => {}
    mocks.cancelEmbeddedLLMInstall.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          release = resolve
        }),
    )

    await act(async () => {
      await clickAsync(q('embedded-llm-cancel-install'))
    })

    const busy = q('embedded-llm-cancel-install') as HTMLButtonElement
    expect(useEmbeddedLLMStore.getState().busy).toBe('cancel')
    expect(busy.disabled).toBe(true)
    expect(busy.textContent).toContain('Cancelling…')
    expect(busy.querySelector('svg.animate-spin')).not.toBeNull()

    await act(async () => {
      release()
    })
    await flush()

    const idle = q('embedded-llm-cancel-install') as HTMLButtonElement
    expect(useEmbeddedLLMStore.getState().busy).toBeNull()
    expect(idle.disabled).toBe(false)
    expect(idle.textContent).not.toContain('Cancelling')
    expect(idle.querySelector('svg.animate-spin')).toBeNull()
  })

  it('returns to the Install button when the stopped run reports its quiet end', async () => {
    await render()
    emitProgress({ component: 'model', stage: 'downloading', bytes_done: 1, bytes_total: 10 })
    expect(q('embedded-llm-progress')).not.toBeNull()

    await act(async () => {
      await clickAsync(q('embedded-llm-cancel-install'))
    })

    // The request was delivered (and its read-back has landed — the window is
    // closed), but the run has not unwound yet: the snapshot still reports the
    // live install, so the block and its Cancel action stay.
    expect(mocks.cancelEmbeddedLLMInstall).toHaveBeenCalledTimes(1)
    expect(useEmbeddedLLMStore.getState().busy).toBeNull()
    expect(q('embedded-llm-progress')).not.toBeNull()
    expect(q('embedded-llm-cancel-install')).not.toBeNull()

    // The run's asynchronous, quiet end: `embedded_llm:state` invalidates the
    // snapshot and the re-read reports installing=false, not-installed.
    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus())
    await emitState({
      installed: false,
      loading: false,
      loaded: false,
      packing: '',
      backend: '',
      port: 0,
      context_size: 0,
      auto_unload_minutes: 60,
      error: '',
      install_error: '',
    })

    expect(q('embedded-llm-progress')).toBeNull()
    expect(q('embedded-progress-model')).toBeNull()
    expect(q('embedded-llm-cancel-install')).toBeNull()
    // Partial bytes were kept as the resume point, so the block offers the
    // Install action again — the retry resumes instead of restarting.
    const install = q('embedded-llm-install')
    expect(install).not.toBeNull()
    expect(install?.textContent).toContain('Install')
  })
})

describe('EmbeddedLLMSettings — after an install', () => {
  const installed = () =>
    makeStatus({
      state: 'installed',
      installed: true,
      packing: 'PQ2_0',
      backend: 'metal',
      port: 43211,
      context_size: 32768,
      auto_unload_enabled: true,
      auto_unload_minutes: 60,
      base_url: 'http://127.0.0.1:43211/v1',
      model_id: 'embedded/Bonsai 2 27B',
      model_file: '/home/u/.c0wrk/models/bonsai-2-27b/Ternary-Bonsai-2-27B-PQ2_0.gguf',
      runtime_version: 'prism-b10709-9a9394a',
    })

  beforeEach(() => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(installed())
  })

  it('shows Remove, Load, the auto-unload checkbox and a minutes field defaulting to 60', async () => {
    await render()

    expect(q('embedded-llm-remove')).not.toBeNull()
    expect(q('embedded-llm-load')?.textContent).toContain('Load')
    // Not resident, so Unload is not offered.
    expect(q('embedded-llm-unload')).toBeNull()
    // The Install action is gone — the model is already on disk.
    expect(q('embedded-llm-install')).toBeNull()
    // And so is the install-run Cancel action — there is no run to stop.
    expect(q('embedded-llm-cancel-install')).toBeNull()

    const checkbox = q('embedded-llm-auto-unload') as HTMLInputElement | null
    expect(checkbox).not.toBeNull()
    expect(checkbox?.type).toBe('checkbox')
    expect(checkbox?.checked).toBe(true)

    const minutes = q('embedded-llm-auto-unload-minutes') as HTMLInputElement | null
    expect(minutes).not.toBeNull()
    expect(minutes?.value).toBe('60')
  })

  it('offers Unload instead of Load while the model is resident', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({ ...installed(), state: 'loaded', loaded: true, pid: 4242, idle_remaining_seconds: 2500 }),
    )
    await render()

    expect(q('embedded-llm-unload')?.textContent).toContain('Unload')
    expect(q('embedded-llm-load')).toBeNull()
    expect(q('embedded-llm-idle')?.textContent).toContain('41m 40s')
  })

  it('shows the informational label of the packing the backend actually resolved', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({
        ...installed(),
        packing: 'PTQ1_0',
        backend: 'vulkan',
        context_size: 16384,
        port: 39999,
        base_url: 'http://127.0.0.1:39999/v1',
      }),
    )
    await render()

    expect(q('embedded-llm-install-record')).not.toBeNull()
    expect(q('embedded-llm-packing')?.textContent).toBe('PTQ1_0')
    expect(q('embedded-llm-backend')?.textContent).toBe('vulkan')
    expect(q('embedded-llm-context')?.textContent).toBe('16384')
    expect(q('embedded-llm-port')?.textContent).toBe('39999')
    expect(q('embedded-llm-base-url')?.textContent).toContain('http://127.0.0.1:39999/v1')
    expect(q('embedded-llm-base-url')?.textContent).toContain('embedded/Bonsai 2 27B')
  })

  it('keeps the label in sync with the backend across a state event', async () => {
    await render()
    expect(q('embedded-llm-packing')?.textContent).toBe('PQ2_0')

    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ ...installed(), packing: 'PTQ1_0', backend: 'cpu' }))
    await emitState({
      installed: true,
      loading: false,
      loaded: false,
      packing: 'PTQ1_0',
      backend: 'cpu',
      port: 43211,
      context_size: 32768,
      auto_unload_minutes: 60,
      error: '',
      install_error: '',
    })

    expect(q('embedded-llm-packing')?.textContent).toBe('PTQ1_0')
    expect(q('embedded-llm-backend')?.textContent).toBe('cpu')
    expect(mocks.getEmbeddedLLMStatus).toHaveBeenCalledTimes(2)
  })

  it('loads and unloads through the RPC', async () => {
    await render()
    await act(async () => {
      await clickAsync(q('embedded-llm-load'))
    })
    expect(mocks.loadEmbeddedLLM).toHaveBeenCalledTimes(1)

    mocks.getEmbeddedLLMStatus.mockResolvedValue(makeStatus({ ...installed(), state: 'loaded', loaded: true }))
    await emitState({
      installed: true,
      loading: false,
      loaded: true,
      packing: 'PQ2_0',
      backend: 'metal',
      port: 43211,
      context_size: 32768,
      auto_unload_minutes: 60,
      error: '',
      install_error: '',
    })

    await act(async () => {
      await clickAsync(q('embedded-llm-unload'))
    })
    expect(mocks.unloadEmbeddedLLM).toHaveBeenCalledTimes(1)
  })
})

describe('EmbeddedLLMSettings — Remove is confirmed before it runs', () => {
  beforeEach(() => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({ state: 'installed', installed: true, packing: 'PQ2_0', backend: 'metal', port: 43211 }),
    )
  })

  it('opens a confirmation and does NOT call the RPC until it is answered', async () => {
    await render()

    click(q('embedded-llm-remove'))
    await flush()

    const dialog = dq('embedded-llm-remove-dialog')
    expect(dialog).not.toBeNull()
    expect(dialog?.textContent).toContain('Remove the embedded model?')
    expect(mocks.removeEmbeddedLLM).not.toHaveBeenCalled()

    click(dq('embedded-llm-remove-confirm'))
    await flush()

    expect(mocks.removeEmbeddedLLM).toHaveBeenCalledTimes(1)
  })

  it('cancels without touching the install', async () => {
    await render()

    click(q('embedded-llm-remove'))
    await flush()
    const dialog = dq('embedded-llm-remove-dialog')
    const cancel = Array.from(dialog?.querySelectorAll('button') ?? []).find(
      (b) => b.textContent === 'Cancel',
    )
    click(cancel ?? null)
    await flush()

    expect(mocks.removeEmbeddedLLM).not.toHaveBeenCalled()
    expect(dq('embedded-llm-remove-dialog')).toBeNull()
    // The block is still in its installed state.
    expect(q('embedded-llm-remove')).not.toBeNull()
  })

  it('reports a removal failure inline', async () => {
    mocks.removeEmbeddedLLM.mockRejectedValue(new Error('the server refused to stop'))
    await render()

    click(q('embedded-llm-remove'))
    await flush()
    click(dq('embedded-llm-remove-confirm'))
    await flush()

    expect(mocks.removeEmbeddedLLM).toHaveBeenCalledTimes(1)
    expect(q('embedded-llm-error')?.textContent).toContain('the server refused to stop')
  })
})

describe('EmbeddedLLMSettings — auto unload', () => {
  beforeEach(() => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({
        state: 'installed',
        installed: true,
        packing: 'PQ2_0',
        backend: 'metal',
        port: 43211,
        auto_unload_enabled: true,
        auto_unload_minutes: 60,
      }),
    )
  })

  it('persists the toggle through the RPC', async () => {
    await render()

    const checkbox = q('embedded-llm-auto-unload') as HTMLInputElement
    act(() => {
      checkbox.click()
    })
    await flush()

    expect(mocks.setEmbeddedLLMAutoUnload).toHaveBeenCalledWith(false, 60)
  })

  it('falls back to a 60-minute budget when the backend reports none', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({ state: 'installed', installed: true, auto_unload_enabled: false, auto_unload_minutes: 0 }),
    )
    await render()

    const minutes = q('embedded-llm-auto-unload-minutes') as HTMLInputElement
    expect(minutes.value).toBe('60')
  })

  it('commits a valid budget on blur', async () => {
    await render()

    const minutes = q('embedded-llm-auto-unload-minutes') as HTMLInputElement
    typeInto(minutes, '15')
    await blur(minutes)
    await flush()

    expect(mocks.setEmbeddedLLMAutoUnload).toHaveBeenCalledWith(true, 15)
  })

  it('refuses an out-of-range budget locally, without a round trip', async () => {
    await render()

    const minutes = q('embedded-llm-auto-unload-minutes') as HTMLInputElement
    typeInto(minutes, '0')
    await blur(minutes)
    await flush()

    expect(mocks.setEmbeddedLLMAutoUnload).not.toHaveBeenCalled()
    // The field reverted to the authoritative value.
    expect((q('embedded-llm-auto-unload-minutes') as HTMLInputElement).value).toBe('60')
  })

  it('carries the ceiling on the input and refuses a larger budget locally', async () => {
    await render()

    const minutes = q('embedded-llm-auto-unload-minutes') as HTMLInputElement
    // MAX_AUTO_UNLOAD_MINUTES mirrors embeddedllm.MaxAutoUnloadMinutes: above
    // it the backend's minutes→nanoseconds multiply overflows, and an
    // "effectively never" budget inverts into "unload immediately".
    expect(minutes.getAttribute('min')).toBe('1')
    expect(minutes.getAttribute('max')).toBe('525600')

    typeInto(minutes, '525601')
    await blur(minutes)
    await flush()

    expect(mocks.setEmbeddedLLMAutoUnload).not.toHaveBeenCalled()
    expect((q('embedded-llm-auto-unload-minutes') as HTMLInputElement).value).toBe('60')
  })

  it('accepts the ceiling itself', async () => {
    await render()

    const minutes = q('embedded-llm-auto-unload-minutes') as HTMLInputElement
    typeInto(minutes, '525600')
    await blur(minutes)
    await flush()

    expect(mocks.setEmbeddedLLMAutoUnload).toHaveBeenCalledWith(true, 525600)
  })

  it('holds the switch and the field disabled until the read-back lands', async () => {
    await render()

    let release: (s: EmbeddedLLMStatus) => void = () => {}
    mocks.getEmbeddedLLMStatus.mockImplementation(
      () =>
        new Promise<EmbeddedLLMStatus>((resolve) => {
          release = resolve
        }),
    )

    const checkbox = q('embedded-llm-auto-unload') as HTMLInputElement
    act(() => {
      checkbox.click()
    })
    await flush()

    expect(mocks.setEmbeddedLLMAutoUnload).toHaveBeenCalledTimes(1)
    expect(useEmbeddedLLMStore.getState().busy).toBe('auto-unload')
    expect((q('embedded-llm-auto-unload') as HTMLInputElement).disabled).toBe(true)
    expect((q('embedded-llm-auto-unload-minutes') as HTMLInputElement).disabled).toBe(true)

    await act(async () => {
      release(makeStatus({ state: 'installed', installed: true, auto_unload_enabled: false }))
    })
    await flush()

    expect(useEmbeddedLLMStore.getState().busy).toBeNull()
  })
})

describe('EmbeddedLLMSettings — tuning section', () => {
  beforeEach(() => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({
        state: 'installed',
        installed: true,
        packing: 'PQ2_0',
        backend: 'metal',
        port: 43211,
      }),
    )
  })

  it('renders the three primary controls and the collapsed Advanced section', async () => {
    await render()

    expect(q('embedded-llm-tuning')).not.toBeNull()
    const triggers = Array.from(container.querySelectorAll('button[aria-haspopup="menu"]'))
    const labels = triggers.map((b) => b.getAttribute('aria-label'))
    expect(labels).toContain('Context mode')
    expect(labels).toContain('KV cache precision')
    expect(labels).toContain('Layer offload')
    // Collapsed: the Advanced knobs render only once the section opens.
    expect(container.querySelector('input[data-field="Parallel slots"]')).toBeNull()
    expect(container.textContent).toContain('Advanced tuning')
  })

  it('renders the measured devices, the unified flag and the effective plan with notes', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({
        state: 'installed',
        installed: true,
        devices: [
          { name: 'Apple M4 Max', description: 'Metal unified memory', total_mib: 49152, free_mib: 24576 },
        ],
        unified: true,
        topology_probed_at: '2026-01-01T00:00:00Z',
        plan: {
          ...EMPTY_PLAN,
          recorded: true,
          packing: 'PQ2_0',
          kv_type: 'q8_0',
          context_size: 131072,
          offload_mode: 'layers',
          layers: 30,
          parallel: 1,
          fit: true,
          expected_device_mib: 12288,
          expected_host_mib: 8192,
          notes: ['--fit was disabled explicitly, so the layer count and the context are computed here'],
        },
      }),
    )
    await render()

    expect(q('embedded-llm-unified')?.textContent).toBe('Yes')
    expect(q('embedded-llm-devices')?.textContent).toContain('Apple M4 Max')
    expect(q('embedded-llm-devices')?.textContent).toContain('24 GiB')
    const plan = q('embedded-llm-plan')?.textContent ?? ''
    expect(plan).toContain('PQ2_0')
    expect(plan).toContain('KV q8_0')
    expect(plan).toContain('131072 ctx')
    expect(plan).toContain('30 layers')
    expect(plan).toContain('12 GiB device')
    expect(q('embedded-llm-plan-notes')?.textContent).toContain('--fit was disabled explicitly')
  })

  it('surfaces the fit-contract warning of the last failed launch', async () => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({
        state: 'error',
        installed: true,
        fit_warning:
          'the runtime\'s --fit pass aborted: "failed to fit params to free device memory" — the recorded memory plan and this pin\'s fit contract disagree; re-plan or re-review the pin before reloading',
      }),
    )
    await render()

    expect(q('embedded-llm-fit-warning')?.textContent).toContain('failed to fit params')
  })

  it('keeps the empty-value convention when nothing was measured or recorded', async () => {
    await render()

    expect(q('embedded-llm-unified')?.textContent).toBe('No')
    expect(q('embedded-llm-plan')?.textContent).toBe('—')
    expect(q('embedded-llm-devices')?.textContent).toContain('No device topology measured')
    expect(q('embedded-llm-plan-notes')).toBeNull()
    expect(q('embedded-llm-fit-warning')).toBeNull()
  })
})

/** Click and let the handler's promise settle (the actions await an RPC and a
 *  status re-read). */
async function clickAsync(el: Element | null): Promise<void> {
  if (!el) throw new Error('element to click not found')
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

// The buttons' `disabled` reads the store's `busy`, and `busy` is cleared only
// AFTER the post-action read-back has landed. Clearing it first would re-enable
// the row while the rendered snapshot is still the pre-action one, so a fast
// second click would fire a duplicate RPC.
describe('EmbeddedLLMSettings — the busy window covers the read-back', () => {
  beforeEach(() => {
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({ state: 'installed', installed: true, packing: 'PQ2_0' }),
    )
  })

  it('holds Load and Remove disabled while the post-action read is pending', async () => {
    await render()

    let release: (s: EmbeddedLLMStatus) => void = () => {}
    mocks.getEmbeddedLLMStatus.mockImplementation(
      () =>
        new Promise<EmbeddedLLMStatus>((resolve) => {
          release = resolve
        }),
    )

    await clickAsync(q('embedded-llm-load'))

    // The load RPC has returned; the snapshot has not been re-read yet.
    expect(mocks.loadEmbeddedLLM).toHaveBeenCalledTimes(1)
    expect(useEmbeddedLLMStore.getState().busy).toBe('load')
    expect((q('embedded-llm-load') as HTMLButtonElement).disabled).toBe(true)
    expect((q('embedded-llm-remove') as HTMLButtonElement).disabled).toBe(true)

    await act(async () => {
      release(makeStatus({ state: 'loaded', installed: true, loaded: true }))
    })
    await flush()

    expect(useEmbeddedLLMStore.getState().busy).toBeNull()
    expect(q('embedded-llm-unload')).not.toBeNull()
  })

  it('releases the busy window even when the action was refused', async () => {
    mocks.unloadEmbeddedLLM.mockRejectedValue(new Error('an install is in flight'))
    mocks.getEmbeddedLLMStatus.mockResolvedValue(
      makeStatus({ state: 'installed', installed: true, loaded: true }),
    )
    await render()

    await clickAsync(q('embedded-llm-unload'))
    await flush()

    expect(useEmbeddedLLMStore.getState().busy).toBeNull()
    expect(q('embedded-llm-error')?.textContent).toContain('an install is in flight')
  })
})

// The install refusal is a COMBINED memory gate (CheckMemoryBudget prices the
// accelerator pool and system RAM together); the flat 16 GiB floor it replaced
// is gone, so the copy must not state it — an 8 GiB-RAM machine with a 24 GiB
// accelerator is admitted, and a 16 GiB-RAM-only machine is not guaranteed.
describe('EmbeddedLLMSettings — the install copy describes the real gate', () => {
  it('names no flat RAM floor anywhere in the block', async () => {
    await render()

    expect(container.textContent).not.toContain('16 GiB')
    expect(container.textContent).not.toContain('less than 16')
  })

  it('keeps the disk figure and describes the memory gate as both pools', async () => {
    await render()

    expect(container.textContent).toContain('8 GiB of disk')
    const hint = q('embedded-llm-install-hint')?.textContent ?? ''
    expect(hint).toContain('accelerator memory')
    expect(hint).toContain('system RAM')
    expect(hint).toContain('Refused up front')
  })

  it('shows the reading hint only while the FIRST snapshot read is in flight', async () => {
    mocks.getEmbeddedLLMStatus.mockImplementation(
      () => new Promise<EmbeddedLLMStatus>(() => {}),
    )
    await render()
    expect(q('embedded-llm-install-hint')?.textContent).toBe('Reading the installed state…')
  })

  it('does not blink the helper text when an event re-reads the snapshot', async () => {
    await render()
    expect(q('embedded-llm-install-hint')?.textContent).toContain('Refused up front')

    // A state event invalidates the snapshot; the re-read must not re-arm the
    // first-read spinner, or this line swaps to "Reading…" on every event.
    let release: (s: EmbeddedLLMStatus) => void = () => {}
    mocks.getEmbeddedLLMStatus.mockImplementation(
      () =>
        new Promise<EmbeddedLLMStatus>((resolve) => {
          release = resolve
        }),
    )
    await emitState({
      installed: false,
      loading: false,
      loaded: false,
      packing: '',
      backend: '',
      port: 0,
      context_size: 0,
      auto_unload_minutes: 60,
      error: '',
      install_error: '',
    })

    expect(useEmbeddedLLMStore.getState().statusLoading).toBe(false)
    expect(q('embedded-llm-install-hint')?.textContent).toContain('Refused up front')

    await act(async () => {
      release(makeStatus())
    })
    await flush()
    expect(q('embedded-llm-install-hint')?.textContent).not.toContain('Reading the installed state')
  })
})
