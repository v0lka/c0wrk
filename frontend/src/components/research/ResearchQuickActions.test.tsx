// @vitest-environment jsdom
// ResearchQuickActions — the research-* skill dispatch row. Covers [22]a:
// send() rethrows when the auto-created session fails (the splash race);
// the rejection must land on the research store's error banner (rendered on
// the research panel), NOT a global toast.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchQuickActions } from './ResearchQuickActions'
import { useResearchStore } from '@/stores/researchStore'

const sendMock = vi.fn<(text: string, skills?: string[]) => Promise<void>>()
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({
    send: sendMock,
    cancel: vi.fn(async () => {}),
    isProcessing: false,
  }),
}))

let activeRoot: Root | null = null

async function renderActions(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<ResearchQuickActions />)
  })
  return container
}

beforeEach(() => {
  sendMock.mockReset()
  useResearchStore.getState().reset()
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('ResearchQuickActions — dispatch failure surfacing ([22]a)', () => {
  it('routes a createSession failure into the research store error', async () => {
    sendMock.mockRejectedValue(new Error('runtime not ready'))

    const container = await renderActions()
    const buttons = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="research-quick-action"]',
    )
    expect(buttons.length).toBeGreaterThan(0)

    await act(async () => {
      buttons[0]!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    const error = useResearchStore.getState().error
    expect(error).toContain('Failed to dispatch')
    expect(error).toContain('runtime not ready')
  })

  it('leaves the store clean when the dispatch succeeds', async () => {
    sendMock.mockResolvedValue(undefined)

    const container = await renderActions()
    const buttons = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="research-quick-action"]',
    )

    await act(async () => {
      buttons[0]!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(useResearchStore.getState().error).toBeNull()
  })
})
