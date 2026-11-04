// @vitest-environment jsdom
// SpecsEmptyState — the neutral !present render (ADR-105 D2): a missing
// specs/ tree is data, not an error. The CTA dispatches the SAME Initialize
// constant the quick-actions row renders (one dispatch path).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsEmptyState } from './SpecsEmptyState'
import { INITIALIZE_ACTION } from './specsActions'

const sendMock = vi.fn((..._args: unknown[]) => Promise.resolve())
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({
    send: sendMock,
    cancel: vi.fn(async () => {}),
    isProcessing: false,
  }),
}))

let activeRoot: Root | null = null

async function renderEmpty(root: string): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const r = createRoot(container)
  activeRoot = r
  await act(async () => {
    r.render(<SpecsEmptyState root={root} />)
  })
  return container
}

beforeEach(() => {
  sendMock.mockClear()
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('SpecsEmptyState', () => {
  it('states what is missing and where the tree is expected', async () => {
    const container = await renderEmpty('/ws/p1/specs')

    expect(container.querySelector('[data-testid="specs-empty-state"]')).not.toBeNull()
    expect(container.textContent).toContain('No specs/ directory yet')
    expect(container.textContent).toContain('/ws/p1/specs')
  })

  it('CTA dispatches the Initialize constant (spec-init + its prompt)', async () => {
    const container = await renderEmpty('/ws/p1/specs')
    const cta = container.querySelector<HTMLButtonElement>(
      '[data-testid="specs-initialize-cta"]',
    )!

    expect(cta.getAttribute('data-skill')).toBe('spec-init')
    await act(async () => {
      cta.click()
      for (let i = 0; i < 5; i++) await Promise.resolve()
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    expect(sendMock).toHaveBeenCalledWith(
      INITIALIZE_ACTION.prompt,
      ['spec-init'],
      undefined,
      undefined,
      undefined,
      { newSession: false },
    )
  })
})
