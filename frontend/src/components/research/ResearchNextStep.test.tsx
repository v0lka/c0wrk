// @vitest-environment jsdom
// ResearchNextStep — the one-click Execute card. Covers [22]a: send()
// rethrows when the auto-created session fails; the rejection must land on
// the research store's error banner, not a global toast.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchNextStep } from './ResearchNextStep'
import { useResearchStore } from '@/stores/researchStore'
import type { ResearchNextStep as NextStep } from '@/types/models'

const sendMock = vi.fn<(text: string, skills?: string[]) => Promise<void>>()
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({
    send: sendMock,
    cancel: vi.fn(async () => {}),
    isProcessing: false,
  }),
}))

let activeRoot: Root | null = null

async function renderCard(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<ResearchNextStep />)
  })
  return container
}

beforeEach(() => {
  sendMock.mockReset()
  useResearchStore.getState().reset()
  useResearchStore.getState().loadNextStep({
    project_id: 'R-001',
    action: 'research-hypothesis',
    reason: 'Formulate the first hypothesis.',
    skill: 'research-hypothesis',
  } as NextStep)
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('ResearchNextStep — Execute failure surfacing ([22]a)', () => {
  it('routes a createSession failure into the research store error', async () => {
    sendMock.mockRejectedValue(new Error('session create failed'))

    const container = await renderCard()
    const execute = container.querySelector<HTMLButtonElement>(
      'button[title*="Execute"]',
    )!

    await act(async () => {
      execute.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    const error = useResearchStore.getState().error
    expect(error).toContain('Failed to dispatch research-hypothesis')
    expect(error).toContain('session create failed')
  })
})
