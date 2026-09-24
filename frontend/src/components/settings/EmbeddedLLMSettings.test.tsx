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
  removeEmbeddedLLM: vi.fn(),
  loadEmbeddedLLM: vi.fn(),
  unloadEmbeddedLLM: vi.fn(),
  setEmbeddedLLMAutoUnload: vi.fn(),
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
  getEmbeddedLLMStatus: mocks.getEmbeddedLLMStatus,
  installEmbeddedLLM: mocks.installEmbeddedLLM,
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

import { EmbeddedLLMSettings } from './EmbeddedLLMSettings'
import { useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import { formatBytes } from '@/lib/formatters'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMInstallProgressData, EmbeddedLLMStateData } from '@/types/events'

let container: HTMLDivElement
let root: Root

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
    pid: 0,
    error: '',
    available: true,
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
  mocks.removeEmbeddedLLM.mockResolvedValue(undefined)
  mocks.loadEmbeddedLLM.mockResolvedValue(undefined)
  mocks.unloadEmbeddedLLM.mockResolvedValue(undefined)
  mocks.setEmbeddedLLMAutoUnload.mockResolvedValue(undefined)
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
    expect(install?.textContent).toContain('Install')

    // Nothing that only exists once the model is on disk.
    expect(q('embedded-llm-remove')).toBeNull()
    expect(q('embedded-llm-load')).toBeNull()
    expect(q('embedded-llm-unload')).toBeNull()
    expect(q('embedded-llm-auto-unload')).toBeNull()
    expect(q('embedded-llm-auto-unload-minutes')).toBeNull()
    expect(q('embedded-llm-install-record')).toBeNull()
    expect(q('embedded-llm-progress')).toBeNull()
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
    mocks.installEmbeddedLLM.mockRejectedValue(
      new Error('the embedded model needs 16 GiB of RAM; this machine has 8.0 GiB'),
    )
    await render()
    await act(async () => {
      await clickAsync(q('embedded-llm-install'))
    })

    const err = q('embedded-llm-error')
    expect(err).not.toBeNull()
    expect(err?.textContent).toContain('16 GiB of RAM')
    // A refusal downloaded nothing, so no progress surface appeared.
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
    })

    expect(q('embedded-llm-progress')).toBeNull()
    expect(q('embedded-llm-install-record')).not.toBeNull()
    expect(q('embedded-llm-remove')).not.toBeNull()
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
})

/** Click and let the handler's promise settle (the actions await an RPC and a
 *  status re-read). */
async function clickAsync(el: Element | null): Promise<void> {
  if (!el) throw new Error('element to click not found')
  el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}
