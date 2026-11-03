// @vitest-environment jsdom
//
// TuningDevices — the `-dev` offload checklist leaf. What the tests pin: the
// rows render the MEASURED inventory (name + description + free/total), a
// toggle commits exactly one patch (add, middle-remove, and the last-remove
// commits the `reset` spelling rather than an empty list), an unset selection
// quotes the recorded plan's own pin, an empty inventory renders the no-probe
// line instead of rows, a disabled leaf accepts nothing, and a PINNED name the
// inventory no longer reports renders as its own stale row — removable, and
// never committed back (so emptying every rendered row still commits reset).
//
// The harness follows the project convention (createRoot + act, no
// testing-library).

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

import { TuningDevices } from './TuningDevices'
import type { EmbeddedLLMDevice, EmbeddedLLMTuningPatch } from '@/api/embeddedTuning'

let container: HTMLDivElement
let root: Root

function mount(node: React.ReactNode): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(node)
  })
}

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

function q(testId: string): HTMLElement | null {
  return document.querySelector(`[data-testid="${testId}"]`)
}

function click(el: Element | null | undefined): void {
  if (!el) throw new Error('element to click not found')
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

const DEVICES: readonly EmbeddedLLMDevice[] = [
  { name: 'CUDA0', description: 'NVIDIA GeForce RTX 5060 Ti', total_mib: 16384, free_mib: 15000 },
  { name: 'CUDA1', description: 'NVIDIA GeForce RTX 2060', total_mib: 6144, free_mib: 5500 },
]

describe('TuningDevices', () => {
  it('renders one row per measured device with its memory figures', () => {
    const onSet = vi.fn()
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={null}
        planned={null}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={onSet}
      />,
    )
    const cuda0 = q('embedded-llm-tuning-devices')?.querySelector(
      '[aria-label="Offload to CUDA0"]',
    )
    expect(cuda0).not.toBeNull()
    expect(cuda0?.textContent).toContain('NVIDIA GeForce RTX 5060 Ti')
    expect(cuda0?.textContent).toContain('14.6 GiB free of 16 GiB')
    expect(cuda0?.getAttribute('aria-pressed')).toBe('false')
    expect(
      q('embedded-llm-tuning-devices')?.querySelector('[aria-label="Offload to CUDA1"]'),
    ).not.toBeNull()
  })

  it('commits one patch adding the toggled device to the existing pin', () => {
    const onSet = vi.fn()
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={['CUDA0']}
        planned={['CUDA0']}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={onSet}
      />,
    )
    expect(
      q('embedded-llm-tuning-devices')
        ?.querySelector('[aria-label="Offload to CUDA0"]')
        ?.getAttribute('aria-pressed'),
    ).toBe('true')
    click(q('embedded-llm-tuning-devices')?.querySelector('[aria-label="Offload to CUDA1"]'))
    expect(onSet).toHaveBeenCalledWith({ devices: ['CUDA0', 'CUDA1'] } satisfies EmbeddedLLMTuningPatch)
  })

  it('commits the reset spelling when the last checked device is unchecked', () => {
    const onSet = vi.fn()
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={['CUDA1']}
        planned={null}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={onSet}
      />,
    )
    click(q('embedded-llm-tuning-devices')?.querySelector('[aria-label="Offload to CUDA1"]'))
    expect(onSet).toHaveBeenCalledWith({ reset: ['devices'] } satisfies EmbeddedLLMTuningPatch)
  })

  it('commits the remaining pin wholesale when a middle device is unchecked', () => {
    const onSet = vi.fn()
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={['CUDA0', 'CUDA1']}
        planned={null}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={onSet}
      />,
    )
    click(q('embedded-llm-tuning-devices')?.querySelector('[aria-label="Offload to CUDA0"]'))
    expect(onSet).toHaveBeenCalledWith({ devices: ['CUDA1'] } satisfies EmbeddedLLMTuningPatch)
  })

  it('renders a pinned-but-unmeasured name as a stale, clickable row', () => {
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={['CUDA0', 'CUDA9']}
        planned={null}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={vi.fn()}
      />,
    )
    const ghost = q('embedded-llm-tuning-devices')?.querySelector(
      '[aria-label="Remove stale pinned device CUDA9"]',
    )
    expect(ghost).not.toBeNull()
    expect(ghost?.getAttribute('aria-pressed')).toBe('true')
    expect(ghost?.textContent).toContain('stale')
  })

  it('removes a stale name from the pin while the measured pin survives', () => {
    const onSet = vi.fn()
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={['CUDA0', 'CUDA9']}
        planned={null}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={onSet}
      />,
    )
    click(
      q('embedded-llm-tuning-devices')?.querySelector(
        '[aria-label="Remove stale pinned device CUDA9"]',
      ),
    )
    expect(onSet).toHaveBeenCalledWith({ devices: ['CUDA0'] } satisfies EmbeddedLLMTuningPatch)
  })

  it('commits the reset spelling when unchecking the last measured row also drops a ghost', () => {
    const onSet = vi.fn()
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={['CUDA1', 'CUDA9']}
        planned={null}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={onSet}
      />,
    )
    click(q('embedded-llm-tuning-devices')?.querySelector('[aria-label="Offload to CUDA1"]'))
    // The stale name never survives a commit: unchecking the last MEASURED row
    // is the reset spelling, never a ghost-only pin.
    expect(onSet).toHaveBeenCalledWith({ reset: ['devices'] } satisfies EmbeddedLLMTuningPatch)
  })

  it('keeps a stale pin clickable after the inventory has emptied (the dropped alias row)', () => {
    const onSet = vi.fn()
    mount(
      <TuningDevices
        devices={[]}
        selected={['CUDA9']}
        planned={null}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={onSet}
      />,
    )
    const ghost = q('embedded-llm-tuning-devices')?.querySelector(
      '[aria-label="Remove stale pinned device CUDA9"]',
    )
    expect(ghost).not.toBeNull()
    click(ghost)
    expect(onSet).toHaveBeenCalledWith({ reset: ['devices'] } satisfies EmbeddedLLMTuningPatch)
  })

  it('quotes the recorded plan beside an unset selection', () => {
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={null}
        planned={['CUDA0']}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={vi.fn()}
      />,
    )
    expect(q('embedded-llm-tuning-devices')?.textContent).toContain('the recorded plan pins CUDA0')
  })

  it('renders the no-probe line instead of rows when nothing was measured', () => {
    mount(
      <TuningDevices
        devices={[]}
        selected={null}
        planned={null}
        probedAt=""
        disabled={false}
        onSet={vi.fn()}
      />,
    )
    expect(q('embedded-llm-tuning-devices')?.textContent).toContain(
      'No device probe has answered yet',
    )
    expect(q('embedded-llm-tuning-devices')?.querySelector('[aria-label^="Offload to"]')).toBeNull()
  })

  it('stamps the no-probe line with the last probe and quotes the runtime-delegating plan', () => {
    mount(
      <TuningDevices
        devices={[]}
        selected={null}
        planned={[]}
        probedAt="2026-01-01T00:00:00Z"
        disabled={false}
        onSet={vi.fn()}
      />,
    )
    const text = q('embedded-llm-tuning-devices')?.textContent ?? ''
    expect(text).toContain('No device probe has answered yet (last probe 2026-01-01T00:00:00Z)')
    expect(text).toContain('the recorded plan leaves the devices to the runtime')
    expect(q('embedded-llm-tuning-devices')?.querySelector('[aria-label^="Offload to"]')).toBeNull()
  })

  it('accepts nothing while disabled', () => {
    const onSet = vi.fn()
    mount(
      <TuningDevices
        devices={DEVICES}
        selected={null}
        planned={null}
        probedAt="2026-01-01T00:00:00Z"
        disabled
        onSet={onSet}
      />,
    )
    click(q('embedded-llm-tuning-devices')?.querySelector('[aria-label="Offload to CUDA0"]'))
    expect(onSet).not.toHaveBeenCalled()
  })
})
