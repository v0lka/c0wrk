// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run.
// Only the functions VectorIndexSettings actually calls are mocked.
const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateVectorIndexSettings: vi.fn(),
  listVectorIndexGPUs: vi.fn(),
}))

vi.mock('@/api/config', () => ({
  getConfig: mocks.getConfig,
  updateVectorIndexSettings: mocks.updateVectorIndexSettings,
}))

vi.mock('@/api/vector', () => ({
  listVectorIndexGPUs: mocks.listVectorIndexGPUs,
}))

// Failure-path tests make the component's logger.error fire; mock the logger
// so the expected errors don't pollute vitest output.
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { VectorIndexSettings } from './VectorIndexSettings'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import type { VectorIndexStatus } from '@/types/models'

// Radix dropdown-menu popper positioning observes elements with
// ResizeObserver, which jsdom does not provide.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

const GPUS = [
  { index: 0, name: 'NVIDIA GeForce RTX 5060 Ti' },
  { index: 1, name: 'NVIDIA GeForce RTX 3090' },
]

/** A config payload with only what the component reads. */
function configPayload(over: Partial<{ execution_provider: string; device_id: number }> = {}) {
  return {
    loaded: true,
    llm: {},
    vector_index: {
      execution_provider: over.execution_provider ?? 'auto',
      device_id: over.device_id ?? 0,
    },
  }
}

function statusWith(over: Partial<VectorIndexStatus> = {}): VectorIndexStatus {
  return {
    state: 'ready',
    progress: 1,
    files_indexed: 10,
    total_files: 10,
    ...over,
  }
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getConfig.mockResolvedValue(configPayload())
  mocks.updateVectorIndexSettings.mockResolvedValue(undefined)
  mocks.listVectorIndexGPUs.mockResolvedValue(GPUS)
  useVectorIndexStore.getState().reset()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
})

const render = () =>
  act(async () => {
    await root.render(<VectorIndexSettings />)
  })

/** Flush the mount-time loads plus any settled microtasks. */
const flush = () => act(async () => {})

/** Wait out the 500ms save debounce with real timers inside an act. */
const waitForSave = () => act(async () => {
  await new Promise((r) => setTimeout(r, 650))
})

const text = () => container.textContent ?? ''

const menuTriggers = () =>
  Array.from(container.querySelectorAll<HTMLButtonElement>('button[aria-haspopup="menu"]'))

const findTrigger = (ariaLabel: string) =>
  menuTriggers().find((b) => b.getAttribute('aria-label') === ariaLabel)

/** Open the combobox with `ariaLabel` and click its `optionLabel` option. */
async function pickOption(ariaLabel: string, optionLabel: string): Promise<void> {
  const trigger = findTrigger(ariaLabel)
  if (!trigger) throw new Error(`${ariaLabel} dropdown not found`)
  // Radix's DropdownMenuTrigger toggles on `pointerdown`, not `click`; the
  // awaited act also lets Radix install its document-level listeners.
  await act(async () => {
    trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  const items = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]'))
  // Exact match first: "CPU" is a substring of "Auto (CUDA → CPU fallback)",
  // and clicking the already-selected option is a no-op by design.
  const option = items.find((o) => o.textContent?.trim() === optionLabel)
    ?? items.find((o) => o.textContent?.includes(optionLabel))
  if (!option) throw new Error(`Option "${optionLabel}" not found in ${ariaLabel} menu`)
  await act(async () => {
    option.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Type into the numeric device input via the native value setter. */
async function typeDevice(value: string): Promise<void> {
  const input = container.querySelector<HTMLInputElement>('input[aria-label="Embedder GPU device"]')
  if (!input) throw new Error('device input not found')
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!
  await act(async () => {
    setter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('VectorIndexSettings — render', () => {
  it('shows the saved provider in the combobox trigger', async () => {
    await render()
    await flush()

    const provider = findTrigger('Embedder execution provider')
    expect(provider).toBeDefined()
    expect(provider!.textContent).toContain('Auto')
  })

  it('renders the GPU dropdown with driver-reported names once the probe settles', async () => {
    await render()
    await flush()

    // Probe resolved with 2 GPUs → device control is the named dropdown.
    const device = findTrigger('Embedder GPU device')
    expect(device).toBeDefined()
    expect(device!.textContent).toContain('NVIDIA GeForce RTX 5060 Ti')

    // Open it: the options list every GPU with its ONNX index.
    await act(async () => {
      device!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    const items = Array.from(document.body.querySelectorAll('[role="menuitem"]'))
      .map((o) => o.textContent ?? '')
    expect(items.some((t) => t.includes('0') && t.includes('NVIDIA GeForce RTX 5060 Ti'))).toBe(true)
    expect(items.some((t) => t.includes('1') && t.includes('NVIDIA GeForce RTX 3090'))).toBe(true)
  })

  it('falls back to a numeric input when the driver reports no GPUs', async () => {
    mocks.listVectorIndexGPUs.mockResolvedValue([])
    await render()
    await flush()

    const input = container.querySelector<HTMLInputElement>('input[aria-label="Embedder GPU device"]')
    expect(input).not.toBeNull()
    expect(input!.value).toBe('0')
    expect(text()).toContain('No NVIDIA GPUs reported')
    // No named dropdown is rendered in this mode.
    expect(findTrigger('Embedder GPU device')).toBeUndefined()
  })

  it('shows a probe-failure warning next to the numeric fallback', async () => {
    mocks.listVectorIndexGPUs.mockRejectedValue(new Error('driver down'))
    await render()
    await flush()

    expect(text()).toContain('GPU probe failed — enter the device index manually.')
    expect(
      container.querySelector<HTMLInputElement>('input[aria-label="Embedder GPU device"]'),
    ).not.toBeNull()
  })
})

describe('VectorIndexSettings — save flow', () => {
  it('changing the provider saves the full settings pair after the debounce', async () => {
    await render()
    await flush()

    await pickOption('Embedder execution provider', 'CPU')

    expect(mocks.updateVectorIndexSettings).not.toHaveBeenCalled()
    await waitForSave()
    expect(mocks.updateVectorIndexSettings).toHaveBeenCalledTimes(1)
    expect(mocks.updateVectorIndexSettings).toHaveBeenCalledWith({
      execution_provider: 'cpu',
      device_id: 0,
    })
  })

  it('re-reading the config on remount reflects the persisted provider (reopen)', async () => {
    mocks.getConfig.mockResolvedValue(configPayload({ execution_provider: 'cuda', device_id: 1 }))
    await render()
    await flush()

    const provider = findTrigger('Embedder execution provider')
    expect(provider!.textContent).toContain('CUDA')
    // Saved device 1 is selected by value in the dropdown.
    const device = findTrigger('Embedder GPU device')
    expect(device!.textContent).toContain('NVIDIA GeForce RTX 3090')
  })

  it('picking another GPU from the dropdown persists its index', async () => {
    await render()
    await flush()

    await pickOption('Embedder GPU device', 'NVIDIA GeForce RTX 3090')
    await waitForSave()

    expect(mocks.updateVectorIndexSettings).toHaveBeenCalledWith({
      execution_provider: 'auto',
      device_id: 1,
    })
  })

  it('typing a valid numeric device id persists it', async () => {
    mocks.listVectorIndexGPUs.mockResolvedValue([])
    await render()
    await flush()

    await typeDevice('2')
    await waitForSave()

    expect(mocks.updateVectorIndexSettings).toHaveBeenCalledWith({
      execution_provider: 'auto',
      device_id: 2,
    })
  })

  it('rejects invalid device input without saving', async () => {
    mocks.listVectorIndexGPUs.mockResolvedValue([])
    await render()
    await flush()

    await typeDevice('-1')
    expect(text()).toContain('Device id must be a whole number ≥ 0.')

    await typeDevice('1.5')
    expect(text()).toContain('Device id must be a whole number ≥ 0.')

    await typeDevice('')
    expect(text()).toContain('Device id must be a whole number ≥ 0.')

    await waitForSave()
    expect(mocks.updateVectorIndexSettings).not.toHaveBeenCalled()
  })

  it('disables the device control while the provider is CPU', async () => {
    await render()
    await flush()

    await pickOption('Embedder execution provider', 'CPU')

    const device = findTrigger('Embedder GPU device')
    expect(device!.disabled).toBe(true)
  })

  it('flushes a pending debounced save on unmount', async () => {
    await render()
    await flush()

    await pickOption('Embedder execution provider', 'CPU')
    // Unmount inside the 500ms window: the pending save must still fire.
    act(() => {
      root.unmount()
    })

    expect(mocks.updateVectorIndexSettings).toHaveBeenCalledTimes(1)
    expect(mocks.updateVectorIndexSettings).toHaveBeenCalledWith({
      execution_provider: 'cpu',
      device_id: 0,
    })
  })

  it('surfaces a backend save failure', async () => {
    mocks.updateVectorIndexSettings.mockRejectedValue(new Error('yaml write failed'))
    await render()
    await flush()

    await pickOption('Embedder execution provider', 'CPU')
    await waitForSave()

    expect(text()).toContain('Failed to save vector index settings')
  })
})

describe('VectorIndexSettings — diagnostics', () => {
  it('renders requested → effective with the fallback reason on a CUDA→CPU fallback', async () => {
    useVectorIndexStore.setState({
      status: statusWith({
        requested_execution_provider: 'cuda',
        execution_provider: 'cpu',
        provider_fallback_reason: 'CUDA error: no kernel image available',
      }),
    })
    await render()
    await flush()

    const provider = container.querySelector('[data-testid="vector-diagnostics-provider"]')
    expect(provider).not.toBeNull()
    expect(provider!.textContent).toContain('CUDA')
    expect(provider!.textContent).toContain('→')
    expect(provider!.textContent).toContain('CPU')

    const reason = container.querySelector('[data-testid="vector-fallback-reason"]')
    expect(reason).not.toBeNull()
    expect(reason!.textContent).toContain('no kernel image available')
    // No CUDA badge when the effective provider is CPU.
    expect(container.querySelector('[data-testid="vector-cuda-verified-badge"]')).toBeNull()
  })

  it('treats Auto resolving to CUDA as a success — no fallback UI', async () => {
    useVectorIndexStore.setState({
      status: statusWith({
        requested_execution_provider: 'auto',
        execution_provider: 'cuda',
        cuda_verified: true,
      }),
    })
    await render()
    await flush()

    const provider = container.querySelector('[data-testid="vector-diagnostics-provider"]')
    expect(provider).not.toBeNull()
    expect(provider!.textContent).toContain('Auto')
    expect(provider!.textContent).toContain('→')
    expect(provider!.textContent).toContain('CUDA')

    // Auto winning with CUDA is the best outcome, not a fallback: no
    // warning block, no info note, no "unknown reason" anywhere.
    expect(container.querySelector('[data-testid="vector-fallback-reason"]')).toBeNull()
    expect(container.querySelector('[data-testid="vector-auto-cpu-note"]')).toBeNull()
    expect(text()).not.toContain('Fell back')
    expect(text()).not.toContain('unknown reason')
  })

  it('shows the expected-degradation info note (not a warning) for Auto → CPU', async () => {
    useVectorIndexStore.setState({
      status: statusWith({
        requested_execution_provider: 'auto',
        execution_provider: 'cpu',
        // No provider_fallback_reason on this path: the concrete cause is
        // WARN-logged at startup only, never part of the status payload.
      }),
    })
    await render()
    await flush()

    const note = container.querySelector('[data-testid="vector-auto-cpu-note"]')
    expect(note).not.toBeNull()
    expect(note!.textContent).toContain('Auto resolved to the CPU provider')
    expect(note!.textContent).toContain('not an error')

    // The info note replaces the warning-styled fallback block entirely.
    expect(container.querySelector('[data-testid="vector-fallback-reason"]')).toBeNull()
    expect(text()).not.toContain('Fell back')
    expect(text()).not.toContain('unknown reason')
    // No CUDA badge on a CPU embedder.
    expect(container.querySelector('[data-testid="vector-cuda-verified-badge"]')).toBeNull()
  })

  it('renders the cuda_verified badge with the driver-probe verdict', async () => {
    useVectorIndexStore.setState({
      status: statusWith({
        requested_execution_provider: 'cuda',
        execution_provider: 'cuda',
        cuda_verified: true,
      }),
    })
    await render()
    await flush()

    const badge = container.querySelector('[data-testid="vector-cuda-verified-badge"]')
    expect(badge).not.toBeNull()
    expect(badge!.textContent).toContain('CUDA verified')
    // Honored request → no fallback reason block.
    expect(container.querySelector('[data-testid="vector-fallback-reason"]')).toBeNull()
  })

  it('renders an unverified badge when the probe cannot confirm CUDA', async () => {
    useVectorIndexStore.setState({
      status: statusWith({
        requested_execution_provider: 'cuda',
        execution_provider: 'cuda',
        cuda_verified: false,
      }),
    })
    await render()
    await flush()

    const badge = container.querySelector('[data-testid="vector-cuda-verified-badge"]')
    expect(badge).not.toBeNull()
    expect(badge!.textContent).toContain('CUDA not verified by driver probe')
  })

  it('degrades to a muted note when no embedder is running', async () => {
    useVectorIndexStore.setState({
      status: statusWith({ state: 'unavailable' }),
    })
    await render()
    await flush()

    expect(container.querySelector('[data-testid="vector-diagnostics-idle"]')).not.toBeNull()
    expect(text()).toContain('Embedder is not running')
    expect(container.querySelector('[data-testid="vector-diagnostics-provider"]')).toBeNull()
  })

  it('shows the restart hint only when the saved config diverges from the running embedder', async () => {
    // Running embedder was created with auto/0; config still auto/0 → no hint.
    useVectorIndexStore.setState({
      status: statusWith({ requested_execution_provider: 'auto', device_id: 0 }),
    })
    await render()
    await flush()
    expect(container.querySelector('[data-testid="vector-restart-hint"]')).toBeNull()

    // Now the saved config points at cuda/1 while the embedder runs auto/0.
    // Reopening the settings section is a REMOUNT: render null first so the
    // mount-time config load re-runs against the new mock.
    mocks.getConfig.mockResolvedValue(configPayload({ execution_provider: 'cuda', device_id: 1 }))
    await act(async () => {
      await root.render(null)
    })
    await act(async () => {
      await root.render(<VectorIndexSettings />)
    })
    await flush()

    const hint = container.querySelector('[data-testid="vector-restart-hint"]')
    expect(hint).not.toBeNull()
    expect(hint!.textContent).toContain('Restart required')
    expect(hint!.textContent).toContain('cuda')
    expect(hint!.textContent).toContain('auto')
  })

  it('shows the restart hint when only the device id diverges', async () => {
    useVectorIndexStore.setState({
      status: statusWith({ requested_execution_provider: 'auto', device_id: 1 }),
    })
    mocks.getConfig.mockResolvedValue(configPayload({ execution_provider: 'auto', device_id: 0 }))
    await render()
    await flush()

    expect(container.querySelector('[data-testid="vector-restart-hint"]')).not.toBeNull()
  })

  it('hides the restart hint when the running embedder facts are unknown', async () => {
    // No embedder ever reported → nothing to compare against.
    useVectorIndexStore.setState({ status: statusWith() })
    await render()
    await flush()

    expect(container.querySelector('[data-testid="vector-restart-hint"]')).toBeNull()
  })
})
