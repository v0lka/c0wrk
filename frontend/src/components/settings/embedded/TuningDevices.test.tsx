// @vitest-environment jsdom
//
// TuningDevices — the `-dev` offload checklist leaf. What the tests pin: the
// rows render the MEASURED inventory (name + description + free/total), a
// toggle commits exactly one patch (add, and the last-remove commits the
// `reset` spelling rather than an empty list), an unset selection quotes the
// recorded plan's own pin, an empty inventory renders the no-probe line
// instead of rows, and a disabled leaf accepts nothing.
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
