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

// Keep the real constants/types; replace only the two RPCs the hook touches.
vi.mock('@/api/embeddedTuning', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/embeddedTuning')>()),
  getEmbeddedLLMTuning: mocks.getEmbeddedLLMTuning,
  setEmbeddedLLMTuning: mocks.setEmbeddedLLMTuning,
}))

import { useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMTuning, EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'
// The fixtures, the mini fold and every DOM interaction are shared with the
// Advanced suite — see @/test/tuningTestHarness for why ONE copy matters. Only
// the `vi.mock` blocks above stay per-file (vitest hoists them).
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

beforeEach(() => {
  vi.clearAllMocks()
  current = makeTuning()
  mocks.getEmbeddedLLMTuning.mockImplementation(async () => current)
  mocks.setEmbeddedLLMTuning.mockImplementation(async (patch: EmbeddedLLMTuningPatch) => {
    current = applyPatch(current, patch)
  })
  useEmbeddedLLMStore.getState().reset()
  mountHarness('primary')
})

afterEach(unmountHarness)

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
  it('drafts an Exact context: the mode switch alone persists nothing', async () => {
    await render()
    await pickOption('Context mode', 'Exact')

    // A count-bearing mode is a DRAFT: the only count available at that instant
    // is a fallback, and committing it would persist an override nobody chose.
    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(trigger('Context mode').textContent).toContain('Exact')
    expect(field('Tokens').value).toBe('65536')
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).not.toBeNull()

    // Committing a count writes the mode AND the count, and retires the draft.
    await editField('Tokens', '49152')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      context: { mode: 'exact', tokens: 49152 },
    })
    expect(trigger('Context mode').textContent).toContain('Exact')
    expect(field('Tokens').value).toBe('49152')
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).toBeNull()
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

  it('persists All / CPU at once and drafts "N layers" until a count is committed', async () => {
    await render()
    await pickOption('Layer offload', 'All')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ offload: { mode: 'all', layers: null } })

    await pickOption('Layer offload', 'CPU')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({ offload: { mode: 'cpu', layers: null } })

    // "N layers" carries a count, and the only count available at that instant
    // is the fallback 0 — `-ngl 0`, an all-CPU launch shape for a 27B model. So
    // the mode switch is a DRAFT and persists nothing.
    mocks.setEmbeddedLLMTuning.mockClear()
    await pickOption('Layer offload', 'N layers')
    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(trigger('Layer offload').textContent).toContain('N layers')
    expect(field('Layers').value).toBe('0')
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).not.toBeNull()

    // Committing a count writes the mode AND the count, and retires the draft.
    await editField('Layers', '30')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      offload: { mode: 'layers', layers: 30 },
    })
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).toBeNull()
  })

  it('seeds a drafted layer count from the recorded plan instead of 0', async () => {
    seedStatus({
      plan: {
        recorded: true,
        packing: 'PQ2_0',
        kv_type: 'q8_0',
        context_size: 131072,
        fit: false,
        fit_arg: 'off',
        fit_target_mib: 0,
        fit_min_context: 0,
        offload_mode: 'layers',
        layers: 42,
        kv_offload: true,
        mmproj_offload: true,
        parallel: 1,
        cache_ram_mib: -1,
        gpu_family: '',
        device_budget_mib: 0,
        host_budget_mib: 0,
        expected_device_mib: 0,
        expected_host_mib: 0,
        notes: [],
      },
    })
    await render()

    await pickOption('Layer offload', 'N layers')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    // The field offers a real launch shape, not a number nobody chose.
    expect(field('Layers').value).toBe('42')
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

  it('refuses a count above the mirrored ceiling locally, without a round trip', async () => {
    mocks.setEmbeddedLLMTuning.mockClear()
    // MAX_TUNING_LAYERS mirrors embeddedllm.MaxTuningLayers (1 << 20); the
    // input's `max` and the local refusal are the same figure.
    await editField('Layers', '1048577')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('24')
  })
})

// The controls render their FALLBACKS until the first `GetEmbeddedLLMTuning`
// read lands, and `busy` is what their `disabled` reads — so both the first
// read and the post-commit read-back must keep them disabled. A commit inside
// either window would persist a patch derived from values the user never saw.
describe('EmbeddedLLMTuning — the disabled windows', () => {
  it('disables every control while the first tuning read is in flight', async () => {
    mocks.getEmbeddedLLMTuning.mockImplementation(() => new Promise<EmbeddedLLMTuning>(() => {}))
    await render()

    expect(useEmbeddedLLMStore.getState().tuning).toBeNull()
    expect(useEmbeddedLLMStore.getState().tuningLoading).toBe(true)
    expect(trigger('Context mode').disabled).toBe(true)
    expect(trigger('KV cache precision').disabled).toBe(true)
    expect(trigger('Layer offload').disabled).toBe(true)
  })

  it('holds the busy flag — and the controls — until the post-commit read-back lands', async () => {
    await render()
    let release: (t: EmbeddedLLMTuning) => void = () => {}
    mocks.getEmbeddedLLMTuning.mockImplementation(
      () =>
        new Promise<EmbeddedLLMTuning>((resolve) => {
          release = resolve
        }),
    )

    await pickOption('KV cache precision', 'q8_0')

    // The write is done, the re-read is not: a second click here must not be
    // able to fire another RPC against a stale snapshot.
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledTimes(1)
    expect(useEmbeddedLLMStore.getState().busy).toBe('tuning')
    expect(trigger('KV cache precision').disabled).toBe(true)

    await act(async () => {
      release(makeTuning({ kv_cache_type: 'q8_0' }))
    })
    await flush()

    expect(useEmbeddedLLMStore.getState().busy).toBeNull()
    expect(trigger('KV cache precision').disabled).toBe(false)
  })
})

// The count beside a drafted count-bearing mode is a FALLBACK (`seedCount`), so
// a bare focus+blur must not persist it: that is how a keyboard user tabbing
// through the tuning surface would end up with `-ngl 0` (an all-CPU launch
// shape) and a hint line that lies. The edit-dirty check in NumberField is what
// separates "tabbed through" from "typed the fallback on purpose" — a value
// comparison could not, since the two look identical at commit time.
describe('EmbeddedLLMTuning — a blur without an edit persists nothing', () => {
  it('leaves a drafted Layers count unpersisted on focus+blur, and still pins a typed 0', async () => {
    await render()
    await pickOption('Layer offload', 'N layers')
    mocks.setEmbeddedLLMTuning.mockClear()

    await touchField('Layers')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('0')
    // Still a draft: nothing was written, so the hint stays honest.
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).not.toBeNull()

    // (ii) Typing the very value that was already rendered DOES pin it — the
    // operator asked for `-ngl 0` explicitly, so it must round-trip.
    await retypeField('Layers', '0')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      offload: { mode: 'layers', layers: 0 },
    })
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).toBeNull()
  })

  it('leaves a drafted Tokens count unpersisted on focus+blur, and still pins a typed fallback', async () => {
    await render()
    await pickOption('Context mode', 'Exact')
    mocks.setEmbeddedLLMTuning.mockClear()

    await touchField('Tokens')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Tokens').value).toBe('65536')
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).not.toBeNull()

    // (ii) 65536 is the rendered fallback (DEFAULT_FIT_MIN_CONTEXT) and still
    // commits when the user actually typed it.
    await retypeField('Tokens', '65536')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      context: { mode: 'exact', tokens: 65536 },
    })
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).toBeNull()
  })

  it('persists nothing when tabbing through an already-stored count either', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
    mocks.setEmbeddedLLMTuning.mockClear()

    await touchField('Layers')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('24')
  })
})

// An EMPTIED Layers field is "no value", not `-ngl 0`. The operator select-all +
// Backspaces to retype the 42 the recorded plan seeded, is interrupted, clicks
// away — and ECMAScript `ToNumber('')` is +0, which passes `Number.isFinite` and
// this knob's `min` of 0. Committing it would launch a 27B model on the CPU alone,
// retire the draft (so the "picking the mode alone saves nothing" hint disappears)
// and report the change as applied. `<input type="number">` reports '' for a
// half-typed value too, so this is not only select-all-and-delete.
describe('EmbeddedLLMTuning — an emptied count field persists nothing', () => {
  it('reverts a drafted Layers count seeded from the recorded plan', async () => {
    seedStatus({ plan: { recorded: true, offload_mode: 'layers', layers: 42 } })
    await render()
    await pickOption('Layer offload', 'N layers')
    expect(field('Layers').value).toBe('42')
    mocks.setEmbeddedLLMTuning.mockClear()

    await emptyField('Layers')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('42')
    // Nothing was written, so the draft hint stays honest.
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).not.toBeNull()
  })

  it('reverts a stored Layers count and keeps the override', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
    mocks.setEmbeddedLLMTuning.mockClear()

    await emptyField('Layers')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('24')
  })

  it('still commits an explicitly typed 0 (all-CPU) after an empty revert', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
    mocks.setEmbeddedLLMTuning.mockClear()

    await emptyField('Layers')
    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()

    // `0` stays reachable by typing `0`: the operator asked for `-ngl 0`.
    await editField('Layers', '0')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      offload: { mode: 'layers', layers: 0 },
    })
    expect(field('Layers').value).toBe('0')
  })
})

// `type="number"` outside a `<form>` gives Enter no default action, so without an
// explicit handler these fields would commit on blur only — while the sibling
// auto-unload minutes field in the SAME Settings block has always committed on
// Enter. One gesture, one persist: `commit` consumes the dirty flag, so the blur
// that follows writes nothing more.
//
// The same "no default action" is why Enter must NOT clear the focus flag: the
// DOM node KEEPS focus, so a false flag would leave the field rendering
// `String(value)` while every further keystroke still landed in `draft` and
// re-armed the dirty flag — text the operator never sees, persisted by the
// eventual blur. The blur half of the gesture is therefore dispatched with
// `blurField` (a bare `focusout`); `touchField` would re-focus first, and
// `onFocus` clearing the dirty flag would make these tests pass with `commit`'s
// flag reset deleted.
describe('EmbeddedLLMTuning — Enter commits like blur', () => {
  it('commits a drafted Layers count on Enter, retiring the draft hint', async () => {
    await render()
    await pickOption('Layer offload', 'N layers')
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Layers', '30')

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      offload: { mode: 'layers', layers: 30 },
    })
    expect(field('Layers').value).toBe('30')
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).toBeNull()
  })

  it('commits an exact Tokens count on Enter', async () => {
    current = makeTuning({ context: { mode: 'exact', tokens: 32768 } })
    await render()
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Tokens', '49152')

    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      context: { mode: 'exact', tokens: 49152 },
    })
    expect(field('Tokens').value).toBe('49152')
  })

  it('persists nothing when Enter is pressed on an unchanged draft', async () => {
    await render()
    await pickOption('Layer offload', 'N layers')
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Layers')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(container.querySelector('[data-testid="embedded-llm-tuning-draft"]')).not.toBeNull()
  })

  it('refuses an out-of-range count on Enter exactly as on blur', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Layers', '-3')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('24')
  })

  it('consumes the dirty flag once — the blur after an Enter writes nothing more', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
    await enterField('Layers', '30')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledTimes(1)
    mocks.setEmbeddedLLMTuning.mockClear()

    // The REAL gesture: Enter, then click away. The DOM node never lost focus, so
    // the blur arrives as a bare `focusout` with no `focusin` in between — which
    // is exactly what `blurField` dispatches and `touchField` does not.
    await blurField('Layers')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('30')
  })

  it('keeps the field editable after an Enter — the next keystroke is rendered, not swallowed', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
    await enterField('Layers', '30')
    expect(field('Layers').value).toBe('30')
    mocks.setEmbeddedLLMTuning.mockClear()

    // Enter left the DOM focus where it was, so this IS a real keystroke and it
    // must be echoed. Before the fix the field cleared its focus flag on Enter
    // and rendered `String(value)` from then on: "300" below went into `draft`,
    // re-armed the dirty flag, stayed invisible, and the eventual blur persisted
    // it — a `-ngl 300` the operator never saw on screen.
    await typeField('Layers', '300')
    expect(field('Layers').value).toBe('300')

    // …and because it is a genuine NEW edit, the bare blur that follows commits
    // it exactly once — the Enter value is not persisted a second time.
    await blurField('Layers')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledTimes(1)
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      offload: { mode: 'layers', layers: 300 },
    })
    expect(field('Layers').value).toBe('300')
  })

  it('re-seeds the draft when the field is re-focused after an Enter', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
    await enterField('Layers', '30')
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
    await blurField('Layers')
    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()

    await focusField('Layers')
    expect(field('Layers').value).toBe('30')

    await typeField('Layers', '31')
    expect(field('Layers').value).toBe('31')

    await blurField('Layers')
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledTimes(1)
    expect(mocks.setEmbeddedLLMTuning).toHaveBeenCalledWith({
      offload: { mode: 'layers', layers: 31 },
    })
  })

  it('never renders a decimal it would refuse — an Enter on 2.5 reverts', async () => {
    current = makeTuning({ offload: { mode: 'layers', layers: 24 } })
    await render()
    mocks.setEmbeddedLLMTuning.mockClear()

    await enterField('Layers', '2.5')

    expect(mocks.setEmbeddedLLMTuning).not.toHaveBeenCalled()
    expect(field('Layers').value).toBe('24')
  })
})
