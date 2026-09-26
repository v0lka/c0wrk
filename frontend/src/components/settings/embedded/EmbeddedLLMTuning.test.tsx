// @vitest-environment jsdom
//
// EmbeddedLLMTuning — the three PRIMARY memory-plan controls, driven through
// the real useEmbeddedLLMTuning hook and the real store (the RPC boundary
// mocked at @/api/embeddedTuning, with a mini fold so a committed patch
// round-trips through the re-read exactly as the backend would).
//
// Per numeric knob, the four cases the auto-unload suite establishes:
// persists through the RPC / falls back when the backend reports an
// unpaintable value / commits a valid value on blur / refuses out-of-range
// locally with no round trip.

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

const mocks = vi.hoisted(() => ({
  getEmbeddedLLMTuning: vi.fn(),
  setEmbeddedLLMTuning: vi.fn(),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

vi.mock('@/api/embedded', () => ({
  DEFAULT_AUTO_UNLOAD_MINUTES: 60,
  MIN_AUTO_UNLOAD_MINUTES: 1,
  getEmbeddedLLMStatus: vi.fn(async () => {
    throw new Error('Wails App bindings are not available')
  }),
  installEmbeddedLLM: vi.fn(),
  removeEmbeddedLLM: vi.fn(),
  loadEmbeddedLLM: vi.fn(),
  unloadEmbeddedLLM: vi.fn(),
  setEmbeddedLLMAutoUnload: vi.fn(),
  onEmbeddedLLMState: () => () => {},
  onEmbeddedLLMInstallProgress: () => () => {},
}))

// Keep the real constants/types; replace only the two RPCs the hook touches.
vi.mock('@/api/embeddedTuning', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/embeddedTuning')>()),
  getEmbeddedLLMTuning: mocks.getEmbeddedLLMTuning,
  setEmbeddedLLMTuning: mocks.setEmbeddedLLMTuning,
}))

import { EmbeddedLLMTuning as TuningSection } from './EmbeddedLLMTuning'
import { useEmbeddedLLMTuning } from '@/hooks/useEmbeddedLLMTuning'
import { refreshEmbeddedLLMTuning, useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMTuning, EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'

let container: HTMLDivElement
let root: Root

/** The live "backend" tuning section the mini fold mutates. */
let current: EmbeddedLLMTuning

function makeTuning(overrides: Partial<EmbeddedLLMTuning> = {}): EmbeddedLLMTuning {
  return {
    context: { mode: null, tokens: null },
    kv_cache_type: null,
    offload: { mode: null, layers: null },
    fit: null,
    fit_target_mib: null,
    fit_min_context: null,
    kv_offload: null,
    mmproj_offload: null,
    packing: null,
    parallel: null,
    cache_ram_mib: null,
    host_reserve_gib: null,
    ...overrides,
  }
}

/** The fold the backend performs: nil keeps, present replaces, reset clears. */
/** The fold the backend performs: nil keeps, present replaces, reset clears.
 *  Works on a mutable record copy — the DTO mirror is deeply readonly. */
function applyPatch(t: EmbeddedLLMTuning, patch: EmbeddedLLMTuningPatch): EmbeddedLLMTuning {
  const next = {
    ...t,
    context: { ...t.context },
    offload: { ...t.offload },
  } as unknown as Record<string, unknown>
  if (patch.context) next.context = { ...patch.context }
  if (patch.kv_cache_type !== undefined) next.kv_cache_type = patch.kv_cache_type
  if (patch.offload) next.offload = { ...patch.offload }
  for (const key of ['fit', 'fit_target_mib', 'fit_min_context', 'kv_offload', 'mmproj_offload', 'packing', 'parallel', 'cache_ram_mib', 'host_reserve_gib'] as const) {
    if (patch[key] !== undefined) next[key] = patch[key]
  }
  if (patch.reset) {
    for (const knob of patch.reset) {
      if (knob === 'context') next.context = { mode: null, tokens: null }
      else if (knob === 'offload') next.offload = { mode: null, layers: null }
      else next[knob] = null
    }
  }
  return next as unknown as EmbeddedLLMTuning
}

function Harness() {
  const tuning = useEmbeddedLLMTuning()
  return <TuningSection {...tuning.primary} />
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
    root.render(<Harness />)
  })
  await flush()
}

/** Seed the store's status snapshot (reload_required, etc.). */
function seedStatus(overrides: Partial<EmbeddedLLMStatus>): void {
  act(() => {
    useEmbeddedLLMStore.getState().setStatus({
      state: 'installed',
      installed: true,
      installing: false,
      loading: false,
      loaded: false,
      packing: 'PQ2_0',
      backend: 'metal',
      port: 43211,
      context_size: 131072,
      auto_unload_enabled: false,
      auto_unload_minutes: 60,
      idle_remaining_seconds: 0,
      base_url: 'http://127.0.0.1:43211/v1',
      model_id: 'embedded/Bonsai 2 27B',
      model_name: 'Bonsai 2 27B',
      runtime_version: 'prism-b10735',
      installed_at: '2026-01-01T00:00:00Z',
      model_file: '/x.gguf',
      devices: [],
      unified: false,
      host_ram_gib: 0,
      device_budget_mib: 0,
      host_budget_mib: 0,
      topology_probed_at: '',
      plan: {
        recorded: false, packing: '', kv_type: '', context_size: 0, fit: false, fit_arg: '',
        fit_target_mib: 0, fit_min_context: 0, offload_mode: 'auto', layers: -1, kv_offload: false,
        mmproj_offload: false, parallel: 0, cache_ram_mib: -1, gpu_family: '', device_budget_mib: 0,
        host_budget_mib: 0, expected_device_mib: 0, expected_host_mib: 0, notes: [],
      },
      reload_required: false,
      pid: 0,
      error: '',
      available: true,
      ...overrides,
    })
  })
}

/** A fresh authoritative tuning read (what a commit / event triggers). */
async function reloadTuning(): Promise<void> {
  await act(async () => {
    await refreshEmbeddedLLMTuning()
  })
  await flush()
}

/** One combobox trigger, by its accessible name. */
function trigger(ariaLabel: string): HTMLButtonElement {
  const el = Array.from(container.querySelectorAll<HTMLButtonElement>('button[aria-haspopup="menu"]')).find(
    (b) => b.getAttribute('aria-label') === ariaLabel,
  )
  if (!el) throw new Error(`trigger ${ariaLabel} not found`)
  return el
}

/** Open the combobox with `ariaLabel` and click its `optionLabel` option. */
async function pickOption(ariaLabel: string, optionLabel: string): Promise<void> {
  const t = trigger(ariaLabel)
  await act(async () => {
    t.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  const items = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]'))
  const option = items.find((o) => o.textContent?.trim() === optionLabel)
  if (!option) throw new Error(`Option "${optionLabel}" not found in ${ariaLabel} menu`)
  await act(async () => {
    option.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  await flush()
}

/** Focus + type + blur a NumberField by its label. */
async function editField(label: string, value: string): Promise<void> {
  const input = container.querySelector<HTMLInputElement>(`input[data-field="${label}"]`)
  if (!input) throw new Error(`field ${label} not found`)
  await act(async () => {
    input.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    await Promise.resolve()
  })
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!
  act(() => {
    setter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => {
    input.dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
  })
  await flush()
}

function field(label: string): HTMLInputElement {
  const el = container.querySelector<HTMLInputElement>(`input[data-field="${label}"]`)
  if (!el) throw new Error(`field ${label} not found`)
  return el
}

beforeEach(() => {
  vi.clearAllMocks()
  current = makeTuning()
  mocks.getEmbeddedLLMTuning.mockImplementation(async () => current)
  mocks.setEmbeddedLLMTuning.mockImplementation(async (patch: EmbeddedLLMTuningPatch) => {
    current = applyPatch(current, patch)
  })
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

describe('EmbeddedLLMTuning — render', () => {
  it('defaults every control to Auto while nothing is overridden', async () => {
    await render()

    expect(trigger('Context mode').textContent).toContain('Auto')
    expect(trigger('KV cache precision').textContent).toContain('Auto')
    expect(trigger('Layer offload').textContent).toContain('Auto')
    // The explicit-value fields render only in their explicit mode.
    expect(container.querySelector('input[data-field="Tokens"]')).toBeNull()
    expect(container.querySelector('input[data-field="Layers"]')).toBeNull()
  })

  it('renders the stored overrides (exact context, q8_0 KV, layer offload)', async () => {
    current = makeTuning({
      context: { mode: 'exact', tokens: 49152 },
      kv_cache_type: 'q8_0',
      offload: { mode: 'layers', layers: 24 },
    })
    await render()

    expect(trigger('Context mode').textContent).toContain('Exact')
    expect(field('Tokens').value).toBe('49152')
    expect(trigger('KV cache precision').textContent).toContain('q8_0')
    expect(trigger('Layer offload').textContent).toContain('N layers')
    expect(field('Layers').value).toBe('24')
  })

  it('falls back a KV spelling outside the closed set to Auto', async () => {
    current = makeTuning({ kv_cache_type: 'q5_0' })
    await render()

    expect(trigger('KV cache precision').textContent).toContain('Auto')
  })

  it('shows the pending-changes hint exactly while reload_required is set', async () => {
    await render()
    expect(container.querySelector('[data-testid="embedded-llm-tuning-reload"]')).toBeNull()

    seedStatus({ reload_required: true })
    await flush()
    expect(container.querySelector('[data-testid="embedded-llm-tuning-reload"]')).not.toBeNull()
  })
})

describe('EmbeddedLLMTuning — combobox commits', () => {
  it('persists an exact context through the RPC', async () => {
    await render()
    await pickOption('Context mode', 'Exact')

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      context: { mode: 'exact', tokens: 65536 },
    })
    // The re-read lands the override: the mode and the tokens field persist.
    expect(trigger('Context mode').textContent).toContain('Exact')
    expect(field('Tokens').value).toBe('65536')
  })

  it('clears the context back to Auto via reset', async () => {
    current = makeTuning({ context: { mode: 'exact', tokens: 49152 } })
    await render()
    await pickOption('Context mode', 'Auto (RAM-tiered)')

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ reset: ['context'] })
    expect(container.querySelector('input[data-field="Tokens"]')).toBeNull()
  })

  it('persists a KV precision and clears it back to Auto', async () => {
    await render()
    await pickOption('KV cache precision', 'q8_0')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ kv_cache_type: 'q8_0' })

    await pickOption('KV cache precision', 'Auto (adaptive)')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ reset: ['kv_cache_type'] })
  })

  it('persists the offload shape (All / CPU / N layers)', async () => {
    await render()
    await pickOption('Layer offload', 'All')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ offload: { mode: 'all', layers: null } })

    await pickOption('Layer offload', 'CPU')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ offload: { mode: 'cpu', layers: null } })

    await pickOption('Layer offload', 'N layers')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      offload: { mode: 'layers', layers: 0 },
    })
    expect(field('Layers').value).toBe('0')
  })
})

describe('EmbeddedLLMTuning — context tokens (the four cases)', () => {
  beforeEach(async () => {
    current = makeTuning({ context: { mode: 'exact', tokens: 32768 } })
    await render()
  })

  it('commits a valid value on blur', async () => {
    await editField('Tokens', '49152')

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      context: { mode: 'exact', tokens: 49152 },
    })
  })

  it('persists through the RPC (the re-read lands the value)', async () => {
    await editField('Tokens', '49152')

    expect(field('Tokens').value).toBe('49152')
  })

  it('falls back when the backend reports 0', async () => {
    current = makeTuning({ context: { mode: 'exact', tokens: 0 } })
    await reloadTuning()

    expect(field('Tokens').value).toBe('65536')
  })

  it('refuses an out-of-range value locally, without a round trip', async () => {
    mocks.setEmbeddedLLMTuning.mockClear()
    await editField('Tokens', '0')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Tokens').value).toBe('32768')
  })
})

describe('EmbeddedLLMTuning — offload layers (the four cases)', () => {
  beforeEach(async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
  })

  it('commits a valid value on blur', async () => {
    await editField('Layers', '30')

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      offload: { mode: 'layers', layers: 30 },
    })
  })

  it('persists through the RPC (the re-read lands the value)', async () => {
    await editField('Layers', '30')

    expect(field('Layers').value).toBe('30')
  })

  it('falls back when the backend reports the omitted-flag sentinel (-1)', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: -1 } })
    await reloadTuning()

    expect(field('Layers').value).toBe('0')
  })

  it('refuses a negative value locally, without a round trip', async () => {
    mocks.setEmbeddedLLMTuning.mockClear()
    await editField('Layers', '-3')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('24')
  })
})
