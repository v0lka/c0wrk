// @vitest-environment jsdom

// EmbeddedLLMDeviceMemory + EmbeddedLLMNotice — the Settings block's live
// device-memory panel and the supervisor-notice banner.
//
// The panel must render the LIVE snapshot (never the recorded topology the
// install record carries), refresh on the button (manual — the only mode that
// paints a refusal), and react to store updates the sync module feeds from
// the `embedded_llm:*` events (auto — the mode that stays quiet). The banner
// must render the notice verbatim and disappear on dismissal. Both are
// store-driven: the tests drive the store directly, exactly as the sync
// module's event handlers would.

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  probeEmbeddedLLMDevices: vi.fn(),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

vi.mock('@/api/embeddedTuning', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/embeddedTuning')>()),
  probeEmbeddedLLMDevices: mocks.probeEmbeddedLLMDevices,
}))

import { EmbeddedLLMDeviceMemory } from './EmbeddedLLMDeviceMemory'
import { EmbeddedLLMNotice } from './EmbeddedLLMNotice'
import { useEmbeddedLLMStore } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMDeviceSnapshot } from '@/api/embeddedTuning'

let container: HTMLDivElement
let root: Root

const snapshot = (free: number): EmbeddedLLMDeviceSnapshot => ({
  devices: [
    { name: 'CUDA0', description: 'NVIDIA GeForce RTX 4090', total_mib: 24576, free_mib: free },
  ],
  unified: false,
  host_ram_gib: 128,
  device_budget_mib: 23552,
  host_budget_mib: 114688,
  probed_at: '2026-11-03T12:00:00Z',
})

function q(testId: string): HTMLElement | null {
  return container.querySelector(`[data-testid="${testId}"]`)
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

async function render(ui: React.ReactNode): Promise<void> {
  await act(async () => {
    root.render(ui)
  })
  await flush()
}

beforeEach(() => {
  vi.clearAllMocks()
  useEmbeddedLLMStore.getState().reset()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(async () => {
  await act(async () => {
    root.unmount()
  })
  document.body.replaceChildren()
})

describe('EmbeddedLLMDeviceMemory', () => {
  it('renders nothing before an install exists', async () => {
    await render(<EmbeddedLLMDeviceMemory installed={false} />)
    expect(container.children).toHaveLength(0)
    expect(mocks.probeEmbeddedLLMDevices).not.toHaveBeenCalled()
  })

  it('renders nothing under the mtplx engine and skips the mount probe', async () => {
    await render(<EmbeddedLLMDeviceMemory installed={true} engine="mtplx" />)
    expect(container.children).toHaveLength(0)
    expect(mocks.probeEmbeddedLLMDevices).not.toHaveBeenCalled()
  })

  it('probes once on mount and renders the live free/total per device', async () => {
    mocks.probeEmbeddedLLMDevices.mockResolvedValue(snapshot(8000))

    await render(<EmbeddedLLMDeviceMemory installed />)

    await flush()
    expect(q('embedded-llm-device-memory')?.textContent).toBe('7.8 GiB free of 24 GiB')
    expect(q('embedded-llm-device-row')?.textContent).toContain('CUDA0')
    expect(mocks.probeEmbeddedLLMDevices).toHaveBeenCalledTimes(1)
    // The measured stamp comes from the snapshot, not the wall clock.
    expect(container.textContent).toContain('measured')
  })

  it('re-renders when the store snapshot is replaced (the event-driven update)', async () => {
    mocks.probeEmbeddedLLMDevices.mockResolvedValue(snapshot(8000))
    await render(<EmbeddedLLMDeviceMemory installed />)
    expect(q('embedded-llm-device-memory')?.textContent).toContain('7.8 GiB')

    // The sync module's state-event handler would do exactly this: measure
    // again and replace the snapshot whole.
    await act(async () => {
      useEmbeddedLLMStore.getState().setDeviceSnapshot(snapshot(20000))
    })

    expect(q('embedded-llm-device-memory')?.textContent).toBe('19.5 GiB free of 24 GiB')
  })

  it('refreshes through the manual probe on button click and paints its refusal', async () => {
    mocks.probeEmbeddedLLMDevices.mockResolvedValueOnce(snapshot(8000))
    await render(<EmbeddedLLMDeviceMemory installed />)
    expect(q('embedded-llm-device-memory')).not.toBeNull()

    mocks.probeEmbeddedLLMDevices.mockRejectedValueOnce(
      new Error('the device probe did not answer: the provisioned runtime reported no recognizable accelerator inventory'),
    )
    await act(async () => {
      q('embedded-llm-device-refresh')?.dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      )
    })
    await flush()

    expect(q('embedded-llm-device-error')?.textContent).toContain('the device probe did not answer')
    // The previous measurement stays on screen beside the refusal.
    expect(q('embedded-llm-device-memory')).not.toBeNull()
    expect(mocks.probeEmbeddedLLMDevices).toHaveBeenCalledTimes(2)
  })

  it('notes a unified pool as one aliased memory, never a sum', async () => {
    mocks.probeEmbeddedLLMDevices.mockResolvedValue({ ...snapshot(8000), unified: true })

    await render(<EmbeddedLLMDeviceMemory installed />)

    await flush()
    expect(q('embedded-llm-device-unified')?.textContent).toContain('Unified memory')
  })

  it('keeps the no-accelerator answer distinct from a failed probe', async () => {
    mocks.probeEmbeddedLLMDevices.mockResolvedValue({ ...snapshot(0), devices: [] })

    await render(<EmbeddedLLMDeviceMemory installed />)

    await flush()
    expect(container.textContent).toContain('reports no accelerator')
    expect(q('embedded-llm-device-error')).toBeNull()
  })
})

describe('EmbeddedLLMNotice', () => {
  const notice = {
    kind: 'low_free_device_memory',
    message:
      'the accelerator has 8000 MiB free, below the 20000 MiB this launch plans to place on it — the plan is unchanged (budgets price total capacity), but the launch may not fit while other processes hold the memory',
    free_device_mib: 8000,
    expected_device_mib: 20000,
  }

  it('renders nothing without a notice', async () => {
    await render(<EmbeddedLLMNotice />)
    expect(container.children).toHaveLength(0)
  })

  it('renders the finding verbatim with its evidence, keyed by kind', async () => {
    await act(async () => {
      useEmbeddedLLMStore.getState().applyNotice(notice)
    })
    await render(<EmbeddedLLMNotice />)

    const banner = q('embedded-llm-notice')
    expect(banner?.getAttribute('data-notice-kind')).toBe('low_free_device_memory')
    expect(banner?.textContent).toContain('the accelerator has 8000 MiB free')
    expect(banner?.textContent).toContain('8000 MiB free against a 20000 MiB projection')
  })

  it('is replaced by a newer notice and disappears on dismissal', async () => {
    await act(async () => {
      useEmbeddedLLMStore.getState().applyNotice(notice)
    })
    await render(<EmbeddedLLMNotice />)
    expect(q('embedded-llm-notice')).not.toBeNull()

    await act(async () => {
      useEmbeddedLLMStore.getState().applyNotice({ ...notice, free_device_mib: 100 })
    })
    expect(q('embedded-llm-notice')?.textContent).toContain('100 MiB free against')

    await act(async () => {
      q('embedded-llm-notice-dismiss')?.dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      )
    })
    expect(q('embedded-llm-notice')).toBeNull()
    expect(useEmbeddedLLMStore.getState().notice).toBeNull()
  })
})
