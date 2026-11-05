// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { logger } from '@/lib/logger'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before the vi.mock factories run.
// Only the functions the component actually calls are mocked; the rest of each
// module surface stays absent per the project's partial-mock convention (see
// ModelConfigDialog.test).
const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(),
  setResearchEnabled: vi.fn(),
  setSpecsEnabled: vi.fn(),
}))

vi.mock('@/api/config', () => ({
  getConfig: mocks.getConfig,
}))

vi.mock('@/api/sidebarPacks', () => ({
  setResearchEnabled: mocks.setResearchEnabled,
  setSpecsEnabled: mocks.setSpecsEnabled,
}))

import { ResearchSpecsSettings } from './ResearchSpecsSettings'
import { useSidebarPacksStore } from '@/stores/sidebarPacksStore'

let container: HTMLDivElement
let root: Root

function render(): void {
  act(() => {
    root.render(<ResearchSpecsSettings />)
  })
}

/** The two toggle checkboxes in DOM order: research first, specs second. */
function toggleInputs(): [HTMLInputElement, HTMLInputElement] {
  const inputs = Array.from(
    container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'),
  )
  const research = inputs[0]
  const specs = inputs[1]
  if (!research || !specs || inputs.length !== 2) {
    throw new Error(`expected 2 toggle inputs, found ${inputs.length}`)
  }
  return [research, specs]
}

async function flush(): Promise<void> {
  await act(async () => {})
}

beforeEach(() => {
  vi.clearAllMocks()
  // nil default = on: a payload without the blocks reads as enabled.
  mocks.getConfig.mockResolvedValue({
    loaded: true,
    research: { enabled: true },
    specs: { enabled: true },
  })
  mocks.setResearchEnabled.mockResolvedValue(undefined)
  mocks.setSpecsEnabled.mockResolvedValue(undefined)
  useSidebarPacksStore.setState({ research: true, specs: true, loaded: false })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

describe('ResearchSpecsSettings', () => {
  it('renders both toggles reflecting the config values (on)', async () => {
    render()
    await flush()
    const [research, specs] = toggleInputs()
    expect(research.checked).toBe(true)
    expect(specs.checked).toBe(true)
    expect(research.disabled).toBe(false)
    expect(specs.disabled).toBe(false)
    expect(container.textContent).toContain('Sidebar Research & Specs')
    expect(container.textContent).toContain('Research')
    expect(container.textContent).toContain('Specs')
  })

  it('renders the toggles off when the config gates are explicitly false', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      research: { enabled: false },
      specs: { enabled: false },
    })
    render()
    await flush()
    const [research, specs] = toggleInputs()
    expect(research.checked).toBe(false)
    expect(specs.checked).toBe(false)
  })

  it('defaults to on when the payload predates the gate blocks', async () => {
    // The backend's nil default is ON; older fixtures without the blocks
    // must read as enabled, not as off.
    mocks.getConfig.mockResolvedValue({ loaded: true })
    render()
    await flush()
    const [research, specs] = toggleInputs()
    expect(research.checked).toBe(true)
    expect(specs.checked).toBe(true)
  })

  it('latches each gate independently from the config payload', async () => {
    mocks.getConfig.mockResolvedValue({
      loaded: true,
      research: { enabled: true },
      specs: { enabled: false },
    })
    render()
    await flush()
    const [research, specs] = toggleInputs()
    expect(research.checked).toBe(true)
    expect(specs.checked).toBe(false)
  })

  it('keeps the toggles disabled until the config read answers', () => {
    // getConfig intentionally left pending in this test: a never-resolving
    // promise keeps the read in flight, so the pre-answer disabled state is
    // asserted without any post-render state update escaping act().
    mocks.getConfig.mockReturnValue(new Promise(() => {}))
    render()
    for (const input of toggleInputs()) {
      expect(input.disabled).toBe(true)
      expect(input.checked).toBe(false)
    }
  })

  it('toggling research writes the research RPC and updates the sidebar store', async () => {
    mocks.getConfig.mockResolvedValue({ loaded: true, research: { enabled: false } })
    render()
    await flush()

    await act(async () => {
      toggleInputs()[0].click()
    })
    await flush()

    expect(mocks.setResearchEnabled).toHaveBeenCalledTimes(1)
    expect(mocks.setResearchEnabled).toHaveBeenCalledWith(true)
    expect(mocks.setSpecsEnabled).not.toHaveBeenCalled()
    // The workspace-tab gate follows in the same beat as the persisted
    // config — the store is the tab strip's single source of truth.
    expect(useSidebarPacksStore.getState().research).toBe(true)
    expect(toggleInputs()[0].checked).toBe(true)
  })

  it('toggling specs writes the specs RPC and updates the sidebar store', async () => {
    render()
    await flush()

    await act(async () => {
      toggleInputs()[1].click()
    })
    await flush()

    expect(mocks.setSpecsEnabled).toHaveBeenCalledTimes(1)
    expect(mocks.setSpecsEnabled).toHaveBeenCalledWith(false)
    expect(mocks.setResearchEnabled).not.toHaveBeenCalled()
    expect(useSidebarPacksStore.getState().specs).toBe(false)
    expect(toggleInputs()[1].checked).toBe(false)
  })

  it('rolls the control back when the backend rejects the write', async ({ onTestFinished }) => {
    // The rejection diagnostic (logger.error at the failure site) is
    // intercepted in the specific test that triggers it.
    const logSpy = vi.spyOn(logger, 'error').mockImplementation(() => {})
    onTestFinished(() => logSpy.mockRestore())
    mocks.setResearchEnabled.mockRejectedValue(new Error('persist failed'))
    render()
    await flush()

    await act(async () => {
      toggleInputs()[0].click()
    })
    await flush()

    // The optimistic update must roll back to what the backend still holds,
    // and the sidebar store must NOT have moved.
    expect(toggleInputs()[0].checked).toBe(true)
    expect(useSidebarPacksStore.getState().research).toBe(true)
    expect(logSpy).toHaveBeenCalledExactlyOnceWith(
      'Failed to toggle research:',
      new Error('persist failed'),
    )
  })

  it('disables both toggles while a write is in flight', async () => {
    let resolveWrite!: () => void
    mocks.setSpecsEnabled.mockImplementation(
      () => new Promise<void>((res) => { resolveWrite = res }),
    )
    render()
    await flush()

    await act(async () => {
      toggleInputs()[1].click()
    })

    expect(mocks.setSpecsEnabled).toHaveBeenCalledWith(false)
    // One surface write at a time: the whole section waits.
    expect(toggleInputs()[0].disabled).toBe(true)
    expect(toggleInputs()[1].disabled).toBe(true)

    await act(async () => {
      resolveWrite()
    })
    await flush()

    expect(toggleInputs()[0].disabled).toBe(false)
    expect(toggleInputs()[1].disabled).toBe(false)
    // The optimistic flip held through the successful write (on → off).
    expect(toggleInputs()[1].checked).toBe(false)
  })

  it('stays disabled and quiet when the config read fails', async ({ onTestFinished }) => {
    // The read-failure diagnostic (logger.warn at the failure site) is
    // intercepted in the specific test that triggers it.
    const logSpy = vi.spyOn(logger, 'warn').mockImplementation(() => {})
    onTestFinished(() => logSpy.mockRestore())
    mocks.getConfig.mockRejectedValue(new Error('backend unavailable'))
    render()
    await flush()

    // Fail visible: no guessed state, no write path armed.
    for (const input of toggleInputs()) {
      expect(input.disabled).toBe(true)
    }
    await act(async () => {
      toggleInputs()[0].click()
    })
    expect(mocks.setResearchEnabled).not.toHaveBeenCalled()
    expect(logSpy).toHaveBeenCalledExactlyOnceWith(
      '[sidebar-packs] gate read failed',
      new Error('backend unavailable'),
    )
  })
})
