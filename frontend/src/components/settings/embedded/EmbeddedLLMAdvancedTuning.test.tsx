// @vitest-environment jsdom
//
// EmbeddedLLMAdvancedTuning — the collapsed Advanced section, driven through
// the real useEmbeddedLLMTuning hook and the real store (the RPC boundary
// mocked at @/api/embeddedTuning with a mini fold, so a committed patch
// round-trips through the re-read exactly as the backend would).
//
// Each of the five numeric knobs carries the four cases the auto-unload suite
// establishes; "the backend reports 0" is spelled per knob — 0 is OUT of range
// for the two >= 1 knobs, and a negative value plays that role for the three
// >= 0 knobs where an explicit 0 is a legal, meaningful choice.

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

vi.mock('@/api/embeddedTuning', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/embeddedTuning')>()),
  getEmbeddedLLMTuning: mocks.getEmbeddedLLMTuning,
  setEmbeddedLLMTuning: mocks.setEmbeddedLLMTuning,
}))

import { EmbeddedLLMAdvancedTuning } from './EmbeddedLLMAdvancedTuning'
import { useEmbeddedLLMTuning } from '@/hooks/useEmbeddedLLMTuning'
import { refreshEmbeddedLLMTuning, useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMTuning, EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'

let container: HTMLDivElement
let root: Root

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
  return <EmbeddedLLMAdvancedTuning {...tuning.advanced} />
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

async function reloadTuning(): Promise<void> {
  await act(async () => {
    await refreshEmbeddedLLMTuning()
  })
  await flush()
}

/** The collapsed section opens on its trigger click. */
async function openSection(): Promise<void> {
  await render()
  const trigger = Array.from(container.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('Advanced tuning'),
  )
  if (!trigger) throw new Error('Advanced tuning trigger not found')
  await act(async () => {
    trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  await flush()
}

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

/** One Toggle checkbox, found by its label text. */
function toggleInput(labelText: string): HTMLInputElement {
  const span = Array.from(container.querySelectorAll('span')).find(
    (s) => s.textContent === labelText,
  )
  const input = span?.previousElementSibling?.querySelector<HTMLInputElement>('input[type="checkbox"]')
  if (!input) throw new Error(`toggle ${labelText} not found`)
  return input
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

/** The five numeric knobs: label, YAML key, a valid value, an out-of-range
 *  spelling, the value the backend reports for the fallback case, and the
 *  fallback display (the knob's documented default). */
const KNOBS = [
  { label: 'Fit target (MiB)', knob: 'fit_target_mib', valid: '2048', validNum: 2048, bad: '-1', badNum: -1, fallback: '1024' },
  { label: 'Fit floor (tokens)', knob: 'fit_min_context', valid: '32768', validNum: 32768, bad: '0', badNum: 0, fallback: '65536' },
  { label: 'Parallel slots', knob: 'parallel', valid: '2', validNum: 2, bad: '0', badNum: 0, fallback: '1' },
  { label: 'Prompt cache (MiB)', knob: 'cache_ram_mib', valid: '512', validNum: 512, bad: '-4', badNum: -4, fallback: '0' },
  { label: 'Host reserve (GiB)', knob: 'host_reserve_gib', valid: '6', validNum: 6, bad: '-2', badNum: -2, fallback: '0' },
] as const

describe('EmbeddedLLMAdvancedTuning — render', () => {
  it('renders collapsed; the knobs appear on open', async () => {
    await render()
    expect(container.querySelector('input[data-field="Parallel slots"]')).toBeNull()

    await openSection()
    expect(field('Parallel slots')).toBeDefined()
  })

  it('shows the documented defaults with an Auto chip while nothing is set', async () => {
    await openSection()

    for (const k of KNOBS) {
      expect(field(k.label).value).toBe(k.fallback)
      const row = container.querySelector(`[data-testid="embedded-llm-tuning-${k.knob}"]`)
      expect(row?.textContent).toContain('Auto')
    }
    // Boolean knobs default to their planner-effective value.
    expect(toggleInput('Fit to device memory').checked).toBe(true)
    expect(toggleInput('KV cache on device').checked).toBe(true)
    expect(toggleInput('Vision projector on device').checked).toBe(true)
  })

  it('renders the stored overrides and swaps the Auto chip for an Auto button', async () => {
    current = makeTuning({ parallel: 3, fit: false })
    await openSection()

    expect(field('Parallel slots').value).toBe('3')
    const row = container.querySelector('[data-testid="embedded-llm-tuning-parallel"]')
    expect(row?.querySelector('button[aria-label="Parallel slots back to auto"]')).not.toBeNull()
    expect(toggleInput('Fit to device memory').checked).toBe(false)
  })
})

describe('EmbeddedLLMAdvancedTuning — numeric knobs (the four cases)', () => {
  it.each(KNOBS)('$label commits a valid value on blur', async (k) => {
    await openSection()
    await editField(k.label, k.valid)

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ [k.knob]: k.validNum })
  })

  it.each(KNOBS)('$label persists through the RPC (the re-read lands the value)', async (k) => {
    await openSection()
    await editField(k.label, k.valid)

    expect(field(k.label).value).toBe(k.valid)
    // The chip flipped: an explicit value is no longer Auto.
    const row = container.querySelector(`[data-testid="embedded-llm-tuning-${k.knob}"]`)
    expect(row?.querySelector('button[aria-label*="back to auto"]')).not.toBeNull()
  })

  it.each(KNOBS)('$label falls back when the backend reports an unpaintable value', async (k) => {
    await openSection()
    current = makeTuning({ [k.knob]: k.badNum } as Partial<EmbeddedLLMTuning>)
    await reloadTuning()

    expect(field(k.label).value).toBe(k.fallback)
  })

  it.each(KNOBS)('$label refuses an out-of-range value locally, without a round trip', async (k) => {
    current = makeTuning({ [k.knob]: k.validNum } as Partial<EmbeddedLLMTuning>)
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()
    await editField(k.label, k.bad)

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field(k.label).value).toBe(k.valid)
  })

  it.each(KNOBS)('$label clears back to unset through the Auto button', async (k) => {
    current = makeTuning({ [k.knob]: k.validNum } as Partial<EmbeddedLLMTuning>)
    await openSection()

    const btn = container.querySelector<HTMLButtonElement>(
      `[data-testid="embedded-llm-tuning-${k.knob}"] button[aria-label*="back to auto"]`,
    )
    if (!btn) throw new Error(`Auto button for ${k.label} not found`)
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ reset: [k.knob] })
    expect(field(k.label).value).toBe(k.fallback)
  })
})

describe('EmbeddedLLMAdvancedTuning — toggles and packing', () => {
  it('persists every boolean knob through the RPC', async () => {
    await openSection()

    for (const [label, key] of [
      ['Fit to device memory', 'fit'],
      ['KV cache on device', 'kv_offload'],
      ['Vision projector on device', 'mmproj_offload'],
    ] as const) {
      mocks.setEmbeddedLLMTuning.mockClear()
      const cb = toggleInput(label)
      await act(async () => {
        cb.click()
      })
      await flush()
      expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ [key]: false })
    }
  })

  it('persists a packing override and clears it back to Auto', async () => {
    await openSection()

    const trigger = Array.from(container.querySelectorAll<HTMLButtonElement>('button[aria-haspopup="menu"]')).find(
      (b) => b.getAttribute('aria-label') === 'Packing',
    )
    if (!trigger) throw new Error('Packing trigger not found')
    await act(async () => {
      trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    const option = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')).find(
      (o) => o.textContent?.trim() === 'PQ2_0',
    )
    if (!option) throw new Error('PQ2_0 option not found')
    await act(async () => {
      option.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ packing: 'PQ2_0' })
    expect(trigger.textContent).toContain('PQ2_0')

    await act(async () => {
      trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    const auto = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')).find(
      (o) => o.textContent?.trim() === 'Auto (resolver)',
    )
    if (!auto) throw new Error('Auto option not found')
    await act(async () => {
      auto.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ reset: ['packing'] })
    expect(trigger.textContent).toContain('Auto')
  })
})
