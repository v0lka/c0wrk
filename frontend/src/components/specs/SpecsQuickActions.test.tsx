// @vitest-environment jsdom
// SpecsQuickActions — the spec-* skill dispatch row (ADR-106 D4). Covers:
// every button dispatches ITS constant prompt with ITS skill through
// sendMessage's activeSkills slot; derived enablement (Initialize without a
// tree, the rest only with one — no workflow-dead buttons, every disabled
// button explains why); Shift = new session; a failed send lands on the
// specs store's error banner (the research [22]a contract), not a toast.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsQuickActions } from './SpecsQuickActions'
import { INITIALIZE_ACTION } from './specsActions'
import { useSpecsStore } from '@/stores/specsStore'

const sendMock = vi.fn(
  (..._args: unknown[]) => Promise.resolve(),
)
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({
    send: sendMock,
    cancel: vi.fn(async () => {}),
    isProcessing: false,
  }),
}))

let activeRoot: Root | null = null

async function renderActions(present: boolean): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<SpecsQuickActions present={present} />)
  })
  return container
}

function buttonBySkill(container: HTMLElement, skill: string): HTMLButtonElement {
  return Array.from(
    container.querySelectorAll<HTMLButtonElement>('[data-testid="specs-quick-action"]'),
  ).find((b) => b.getAttribute('data-skill') === skill)!
}

function click(el: HTMLElement, shiftKey = false): void {
  el.dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey }))
}

beforeEach(() => {
  // mockReset (not mockClear): a per-test rejected implementation must not
  // leak into the next test.
  sendMock.mockReset()
  useSpecsStore.getState().reset()
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('SpecsQuickActions — dispatch', () => {
  it('dispatches Initialize with the constant prompt + spec-init skill', async () => {
    const container = await renderActions(false)
    click(buttonBySkill(container, 'spec-init'))

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

  it('dispatches Check drift / Consult / Explore with their own skills and prompts', async () => {
    const container = await renderActions(true)
    click(buttonBySkill(container, 'spec-check'))
    click(buttonBySkill(container, 'spec-consult'))
    click(buttonBySkill(container, 'spec-explore'))

    expect(sendMock).toHaveBeenCalledTimes(3)
    const skills = sendMock.mock.calls.map((c) => (c[1] as string[])[0])
    expect(skills).toEqual(['spec-check', 'spec-consult', 'spec-explore'])
    const prompts = sendMock.mock.calls.map((c) => c[0] as string)
    expect(prompts[0]).toMatch(/drift/i)
    expect(prompts.every((p) => p.length > 10)).toBe(true)
  })

  it('Shift-click dispatches into a brand-new session', async () => {
    const container = await renderActions(true)
    click(buttonBySkill(container, 'spec-check'), true)

    expect(sendMock.mock.calls[0]![5]).toEqual({ newSession: true })
  })
})

describe('SpecsQuickActions — derived enablement', () => {
  it('without a tree: Initialize enabled, the rest disabled with the reason tooltip', async () => {
    const container = await renderActions(false)
    const buttons = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="specs-quick-action"]',
    )
    expect(buttons).toHaveLength(4)

    const init = buttonBySkill(container, 'spec-init')
    expect(init.disabled).toBe(false)
    for (const skill of ['spec-check', 'spec-consult', 'spec-explore']) {
      const b = buttonBySkill(container, skill)
      expect(b.disabled).toBe(true)
      expect(b.title).toBe('No specs/ directory yet — initialize first')
      // A disabled button dispatches nothing.
      click(b)
    }
    expect(sendMock).not.toHaveBeenCalled()
  })

  it('with a tree: every action enabled', async () => {
    const container = await renderActions(true)
    for (const b of Array.from(
      container.querySelectorAll<HTMLButtonElement>('[data-testid="specs-quick-action"]'),
    )) {
      expect(b.disabled).toBe(false)
    }
  })
})

describe('SpecsQuickActions — dispatch failure surfacing', () => {
  it('routes a send rejection into the specs store error (not a throw)', async () => {
    sendMock.mockRejectedValue(new Error('runtime not ready'))
    const container = await renderActions(true)

    await act(async () => {
      click(buttonBySkill(container, 'spec-check'))
      for (let i = 0; i < 5; i++) await Promise.resolve()
    })

    const error = useSpecsStore.getState().error
    expect(error).toContain('Failed to dispatch spec-check')
    expect(error).toContain('runtime not ready')
  })

  it('leaves the store clean when the dispatch succeeds', async () => {
    const container = await renderActions(true)
    await act(async () => {
      click(buttonBySkill(container, 'spec-init'))
      for (let i = 0; i < 5; i++) await Promise.resolve()
    })
    expect(useSpecsStore.getState().error).toBeNull()
  })
})
