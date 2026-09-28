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
  MAX_AUTO_UNLOAD_MINUTES: 525600,
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

import { useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMTuning, EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'
// The fixtures, the mini fold and every DOM interaction are shared with the
// primary-controls suite — see @/test/tuningTestHarness for why ONE copy
// matters. Only the `vi.mock` blocks above and the Advanced-only helpers below
// stay per-file.
import {
  applyPatch,
  blurField,
  container,
  editField,
  emptyField,
  enterField,
  field,
  flush,
  focusField,
  makeTuning,
  mountHarness,
  pickOption,
  reloadTuning,
  render,
  retypeField,
  seedStatus,
  touchField,
  trigger,
  typeField,
  unmountHarness,
} from '@/test/tuningTestHarness'

/** The live "backend" tuning section the mini fold mutates. */
let current: EmbeddedLLMTuning

/** The two device-offload booleans: label, YAML key, and the flag the runtime is
 *  launched with when the memory gate resolves an unset knob to OFF. */
const BOOL_KNOBS = [
  { label: 'KV cache on device', knob: 'kv_offload', offFlag: '-nkvo' },
  { label: 'Vision projector on device', knob: 'mmproj_offload', offFlag: '--no-mmproj-offload' },
] as const

/** The collapsed section opens on its trigger click. */
async function openSection(): Promise<void> {
  await render()
  const btn = Array.from(container.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('Advanced tuning'),
  )
  if (!btn) throw new Error('Advanced tuning trigger not found')
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  await flush()
}

beforeEach(() => {
  vi.clearAllMocks()
  current = makeTuning()
  mocks.getEmbeddedLLMTuning.mockImplementation(async () => current)
  mocks.setEmbeddedLLMTuning.mockImplementation(async (patch: EmbeddedLLMTuningPatch) => {
    current = applyPatch(current, patch)
  })
  useEmbeddedLLMStore.getState().reset()
  mountHarness('advanced')
})

afterEach(unmountHarness)

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
    // An UNSET Fit renders Auto — never a fallback-derived ON, which the
    // offload exclusivity rule can contradict with an effective `-fit off`.
    expect(trigger('Fit to device memory').textContent).toContain('Auto')
    // The two device-offload booleans are tri-states for the same reason: the
    // memory gate spills them to host RAM while the override stays unset.
    for (const b of BOOL_KNOBS) {
      expect(trigger(b.label).textContent).toContain('Auto')
    }
  })

  it('renders the stored overrides and swaps the Auto chip for an Auto button', async () => {
    current = makeTuning({ parallel: 3, fit: false })
    await openSection()

    expect(field('Parallel slots').value).toBe('3')
    const row = container.querySelector('[data-testid="embedded-llm-tuning-parallel"]')
    expect(row?.querySelector('button[aria-label="Parallel slots back to auto"]')).not.toBeNull()
    expect(trigger('Fit to device memory').textContent).toContain('Off')
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

describe('EmbeddedLLMAdvancedTuning — the tri-state booleans and packing', () => {
  it.each(BOOL_KNOBS)('$label persists Off, On, and back to Auto (reset)', async (b) => {
    await openSection()

    await pickOption(b.label, 'Off')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ [b.knob]: false })
    expect(trigger(b.label).textContent).toContain('Off')

    mocks.setEmbeddedLLMTuning.mockClear()
    await pickOption(b.label, 'On')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ [b.knob]: true })
    expect(trigger(b.label).textContent).toContain('On')

    // The path back to "the planner decides" — the reason this is a tri-state
    // and not a switch: `reset` is the ONLY spelling that clears an override.
    mocks.setEmbeddedLLMTuning.mockClear()
    await pickOption(b.label, 'Auto (memory gate)')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ reset: [b.knob] })
    expect(trigger(b.label).textContent).toContain('Auto')
  })

  it.each(BOOL_KNOBS)(
    '$label renders Auto — never a fabricated ON — while the plan spilled it to host RAM',
    async (b) => {
      // The memory gate could not hold it on the accelerator, so the resident
      // server was launched with the OFF flag while the override stays unset. A
      // switch would have shown ON and "flipping it to what it displays" would
      // then have silently changed the launch shape.
      seedStatus({ plan: { recorded: true, kv_offload: false, mmproj_offload: false } })
      await openSection()

      const text = trigger(b.label).textContent ?? ''
      expect(text).toContain('Auto')
      expect(text).not.toContain('On')
      expect(text).not.toContain('Off')
      // The plan's own outcome is quoted beside the knob, so Auto is not a guess.
      const row = container.querySelector(`[data-testid="embedded-llm-tuning-${b.knob}"]`)
      expect(row?.textContent).toContain(`the recorded plan keeps it in system RAM (${b.offFlag})`)
    },
  )

  it.each(BOOL_KNOBS)('$label quotes "on device" when the recorded plan kept it there', async (b) => {
    seedStatus({ plan: { recorded: true, kv_offload: true, mmproj_offload: true } })
    await openSection()

    const row = container.querySelector(`[data-testid="embedded-llm-tuning-${b.knob}"]`)
    expect(row?.textContent).toContain('the recorded plan keeps it on device')
    expect(trigger(b.label).textContent).toContain('Auto')
  })

  it.each(BOOL_KNOBS)('$label renders a stored override and quotes no plan', async (b) => {
    current = makeTuning({ [b.knob]: false } as Partial<EmbeddedLLMTuning>)
    await openSection()

    expect(trigger(b.label).textContent).toContain('Off')
    const row = container.querySelector(`[data-testid="embedded-llm-tuning-${b.knob}"]`)
    expect(row?.textContent).not.toContain('the recorded plan keeps it')
  })

  it('quotes nothing for the device-offload knobs while no plan was recorded', async () => {
    await openSection()

    for (const b of BOOL_KNOBS) {
      const row = container.querySelector(`[data-testid="embedded-llm-tuning-${b.knob}"]`)
      expect(row?.textContent).not.toContain('the recorded plan keeps it')
    }
  })

  it('persists the Fit tri-state: Off, On, and back to Auto (reset)', async () => {
    await openSection()

    await pickOption('Fit to device memory', 'Off')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ fit: false })
    expect(trigger('Fit to device memory').textContent).toContain('Off')

    mocks.setEmbeddedLLMTuning.mockClear()
    await pickOption('Fit to device memory', 'On')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ fit: true })
    expect(trigger('Fit to device memory').textContent).toContain('On')

    mocks.setEmbeddedLLMTuning.mockClear()
    await pickOption('Fit to device memory', 'Auto (offload rule)')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ reset: ['fit'] })
    expect(trigger('Fit to device memory').textContent).toContain('Auto')
  })

  it('quotes the recorded plan’s own -fit value beside an unset knob', async () => {
    // The exclusivity rule forced fit OFF for this launch shape: an unset knob
    // must say so instead of showing a switch that reads ON.
    seedStatus({
      plan: { recorded: true, fit: false, fit_arg: 'off', offload_mode: 'layers', layers: 12 },
    })
    await openSection()

    const row = container.querySelector('[data-testid="embedded-llm-tuning-fit"]')
    expect(row?.textContent).toContain('-fit off')
    expect(trigger('Fit to device memory').textContent).toContain('Auto')
  })

  it('says nothing about the plan while none was recorded', async () => {
    await openSection()

    const row = container.querySelector('[data-testid="embedded-llm-tuning-fit"]')
    expect(row?.textContent).not.toContain('the recorded plan runs')
  })

  it('persists a packing override and clears it back to Auto', async () => {
    await openSection()

    await pickOption('Packing', 'PQ2_0')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ packing: 'PQ2_0' })
    expect(trigger('Packing').textContent).toContain('PQ2_0')

    await pickOption('Packing', 'Auto (resolver)')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ reset: ['packing'] })
    expect(trigger('Packing').textContent).toContain('Auto')
  })
})

// An Auto knob renders its documented DEFAULT, not a stored value: the snapshot
// says the knob is unset ("the planner decides"). So a bare focus+blur must
// persist nothing — otherwise one keyboard sweep through Advanced tuning writes
// five overrides nobody chose, and `cache_ram_mib: 0` is the worst of them (nil
// merely OMITS --cache-ram, while 0 DISABLES the prompt cache).
describe('EmbeddedLLMAdvancedTuning — a blur without an edit persists nothing', () => {
  it.each(KNOBS)('$label stays Auto across a focus+blur with no keystroke', async (k) => {
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await touchField(k.label)

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field(k.label).value).toBe(k.fallback)
    const row = container.querySelector(`[data-testid="embedded-llm-tuning-${k.knob}"]`)
    // Still the Auto CHIP, not the Auto button an explicit value renders.
    expect(row?.querySelector('button[aria-label*="back to auto"]')).toBeNull()
    expect(row?.textContent).toContain('Auto')
  })

  it.each(KNOBS)('$label still pins the fallback when the user actually types it', async (k) => {
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    // An EDIT-DIRTY check — not `parsed !== value` — is what keeps this working:
    // the retyped text ends up equal to the rendered fallback and still commits.
    await retypeField(k.label, k.fallback)

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ [k.knob]: Number(k.fallback) })
    const row = container.querySelector(`[data-testid="embedded-llm-tuning-${k.knob}"]`)
    expect(row?.querySelector('button[aria-label*="back to auto"]')).not.toBeNull()
  })

  it('persists nothing on a focus+blur of an already-explicit knob either', async () => {
    current = makeTuning({ cache_ram_mib: 512 })
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await touchField('Prompt cache (MiB)')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Prompt cache (MiB)').value).toBe('512')
  })
})

// An EMPTIED field is "no value", not zero: ECMAScript `ToNumber('')` is +0, so
// without an empty check a select-all+Backspace — or a half-typed `1e` on the way
// to `1e3`, whose DOM `value` is '' too — sails through `Number.isFinite` and a
// `min` of 0 and commits a 0 nobody typed. These are the three knobs whose floor
// IS 0, so the range check cannot catch it: `cache_ram_mib: 0` DISABLES the
// prompt cache (unset merely omits --cache-ram) and `host_reserve_gib: 0`
// REPLACES the planner's derived reserve.
const ZERO_FLOOR_KNOBS = [
  { label: 'Fit target (MiB)', knob: 'fit_target_mib', stored: 2048 },
  { label: 'Prompt cache (MiB)', knob: 'cache_ram_mib', stored: 512 },
  { label: 'Host reserve (GiB)', knob: 'host_reserve_gib', stored: 6 },
] as const

describe('EmbeddedLLMAdvancedTuning — an emptied field persists nothing', () => {
  it.each(ZERO_FLOOR_KNOBS)('$label reverts and writes nothing when the draft is emptied', async (k) => {
    current = makeTuning({ [k.knob]: k.stored } as Partial<EmbeddedLLMTuning>)
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await emptyField(k.label)

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    // The same outcome as the out-of-range branch: the field reverts.
    expect(field(k.label).value).toBe(String(k.stored))
  })

  // Not a separate branch: `<input type="number">` sanitizes a whitespace-only
  // draft to `''` before React ever sees it, so typing spaces IS the emptied
  // gesture above. Kept as its own case because it drives the real keystrokes
  // (and would catch a regression that made the field accept `'  '` as 0).
  it.each(ZERO_FLOOR_KNOBS)(
    '$label takes the emptied branch for a whitespace draft the number input sanitized to empty',
    async (k) => {
      current = makeTuning({ [k.knob]: k.stored } as Partial<EmbeddedLLMTuning>)
      await openSection()
      mocks.setEmbeddedLLMTuning.mockClear()

      await editField(k.label, '  ')

      expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
      expect(field(k.label).value).toBe(String(k.stored))
    },
  )

  it.each(ZERO_FLOOR_KNOBS)('$label still commits an explicitly typed 0', async (k) => {
    current = makeTuning({ [k.knob]: k.stored } as Partial<EmbeddedLLMTuning>)
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await editField(k.label, '0')

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ [k.knob]: 0 })
    expect(field(k.label).value).toBe('0')
  })

  it('reverts an emptied Auto knob to its fallback instead of pinning 0', async () => {
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await emptyField('Prompt cache (MiB)')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Prompt cache (MiB)').value).toBe('0')
    const row = container.querySelector('[data-testid="embedded-llm-tuning-cache_ram_mib"]')
    // Still the Auto CHIP: nothing was written, so `--cache-ram` stays omitted.
    expect(row?.querySelector('button[aria-label*="back to auto"]')).toBeNull()
  })
})

// `type="number"` outside a `<form>` gives Enter no default action, so without an
// explicit handler the seven tuning fields would commit on blur only — while the
// sibling auto-unload minutes field in the SAME Settings block has always
// committed on Enter. One gesture, one persist: `commit` consumes the dirty flag,
// so the blur that follows writes nothing more.
//
// Enter must NOT clear the focus flag, for the same "no default action" reason:
// the DOM node keeps focus, and a false flag would leave the field rendering
// `String(value)` while further keystrokes still landed in `draft` — invisible
// text the eventual blur persists. Hence `blurField` (a bare `focusout`) for the
// blur half of the gesture: `touchField` re-focuses first, and `onFocus` clearing
// the dirty flag would make these tests pass with `commit`'s reset deleted.
describe('EmbeddedLLMAdvancedTuning — Enter commits like blur', () => {
  it.each(KNOBS)('$label commits on Enter without a blur', async (k) => {
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField(k.label, k.valid)

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ [k.knob]: k.validNum })
    expect(field(k.label).value).toBe(k.valid)
  })

  it('persists nothing when Enter is pressed on an unchanged field', async () => {
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Parallel slots')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Parallel slots').value).toBe('1')
    const row = container.querySelector('[data-testid="embedded-llm-tuning-parallel"]')
    expect(row?.querySelector('button[aria-label*="back to auto"]')).toBeNull()
  })

  it('consumes the dirty flag once — the blur after an Enter writes nothing more', async () => {
    await openSection()
    await enterField('Parallel slots', '4')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledTimes(1)
    mocks.setEmbeddedLLMTuning.mockClear()

    // The REAL gesture: Enter, then click away. The DOM node never lost focus, so
    // the blur arrives as a bare `focusout` with no `focusin` in between — which
    // is exactly what `blurField` dispatches and `touchField` does not.
    await blurField('Parallel slots')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Parallel slots').value).toBe('4')
  })

  it('keeps the field editable after an Enter — the next keystroke is rendered, not swallowed', async () => {
    await openSection()
    await enterField('Parallel slots', '4')
    expect(field('Parallel slots').value).toBe('4')
    mocks.setEmbeddedLLMTuning.mockClear()

    // Enter left the DOM focus where it was, so this IS a real keystroke and it
    // must be echoed. Before the fix the field cleared its focus flag on Enter
    // and rendered `String(value)` from then on: the "40" below went into
    // `draft`, re-armed the dirty flag, stayed invisible, and the eventual blur
    // persisted it — a `-np 40` the operator never saw on screen.
    await typeField('Parallel slots', '40')
    expect(field('Parallel slots').value).toBe('40')

    // …and because it is a genuine NEW edit, the bare blur that follows commits
    // it exactly once — the Enter value is not persisted a second time.
    await blurField('Parallel slots')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledTimes(1)
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ parallel: 40 })
    expect(field('Parallel slots').value).toBe('40')
  })

  it('re-seeds the draft when the field is re-focused after an Enter', async () => {
    await openSection()
    await enterField('Parallel slots', '4')
    mocks.setEmbeddedLLMTuning.mockClear()

    // The other half of the same rule, stated from the re-focus side: clicking
    // back into the field shows the COMMITTED value (not a stale draft), and the
    // next keystroke is visible from the first character.
    //
    // The blur is part of the gesture, not padding: a browser never fires a
    // second `focus` without a preceding `blur`, so Enter → click away → click
    // back is the sequence a user actually produces. It also re-pins the other
    // half of the Enter contract on the way — the blur persists nothing,
    // because the Enter already consumed the edit-dirty flag.
    await blurField('Parallel slots')
    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()

    await focusField('Parallel slots')
    expect(field('Parallel slots').value).toBe('4')

    await typeField('Parallel slots', '5')
    expect(field('Parallel slots').value).toBe('5')

    await blurField('Parallel slots')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledTimes(1)
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ parallel: 5 })
  })

  it('refuses a decimal for an `int` knob on Enter, without a round trip', async () => {
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    // `-np` is an `int` on the wire; `step=1` constrains only the spinner
    // buttons, so typed text needs an explicit integrality check. Without one the
    // patch reached Go and the raw `json: cannot unmarshal number 2.5 into Go
    // struct field … of type int` was painted in the Settings error line.
    await enterField('Parallel slots', '2.5')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Parallel slots').value).toBe('1')
  })

  it('still accepts a decimal for the one `float64` knob', async () => {
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Host reserve (GiB)', '6.5')

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ host_reserve_gib: 6.5 })
    expect(field('Host reserve (GiB)').value).toBe('6.5')
  })

  it('refuses an out-of-range value on Enter exactly as on blur', async () => {
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Parallel slots', '0')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Parallel slots').value).toBe('1')
  })

  it('reverts an emptied field on Enter too', async () => {
    current = makeTuning({ cache_ram_mib: 512 })
    await openSection()
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Prompt cache (MiB)', '')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Prompt cache (MiB)').value).toBe('512')
  })
})
