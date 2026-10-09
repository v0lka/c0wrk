// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { promoteMock, switchStateMock } = vi.hoisted(() => ({
  promoteMock: vi.fn(),
  switchStateMock: vi.fn(),
}))
vi.mock('@/api/sessions', () => ({
  promoteSessionToProject: promoteMock,
}))
vi.mock('@/hooks/useProjectSwitchState', () => ({
  useProjectSwitchState: () => switchStateMock,
}))

import { PromoteSessionDialog } from './PromoteSessionDialog'

function renderDialog(open: boolean, handlers: { onOpenChange: (o: boolean) => void }): { root: Root; container: HTMLElement } {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => {
    root.render(
      <PromoteSessionDialog
        sessionId={open ? 's1' : null}
        sessionName="My Chat"
        onOpenChange={handlers.onOpenChange}
      />,
    )
  })
  return { root, container }
}

// Radix portals the dialog content to document.body, so queries must look at
// the document, not the React container.
function findButton(text: string): HTMLButtonElement | undefined {
  return Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
}

function findInput(): HTMLInputElement | undefined {
  return document.querySelector('input')
}

beforeEach(() => {
  promoteMock.mockReset()
  switchStateMock.mockReset()
  switchStateMock.mockResolvedValue(undefined)
})

describe('PromoteSessionDialog', () => {
  it('pre-fills the session name and promotes with the trimmed name, then switches project', async () => {
    promoteMock.mockResolvedValue({ id: 'proj-9', name: 'My Chat', workspace_path: '/ws', is_external: false, is_no_project: false, research_pins: { research: [], hypotheses: {} }, created_at: '', last_active_at: '' })
    const onOpenChange = vi.fn()
    const { root } = renderDialog(true, { onOpenChange })

    const input = findInput()
    expect(input?.value).toBe('My Chat')

    const promote = findButton('Promote')
    expect(promote).toBeDefined()
    await act(async () => {
      promote?.click()
    })
    expect(promoteMock).toHaveBeenCalledWith('s1', 'My Chat')
    expect(switchStateMock).toHaveBeenCalledWith('proj-9')
    expect(onOpenChange).toHaveBeenCalledWith(false)
    root.unmount()
  })

  it('keeps the dialog open and shows the backend error when the promotion fails', async () => {
    promoteMock.mockRejectedValue(new Error('cannot promote a session that has an unfinished task'))
    const onOpenChange = vi.fn()
    renderDialog(true, { onOpenChange })

    await act(async () => {
      findButton('Promote')?.click()
    })
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
    const alert = document.querySelector('[role="alert"]')
    expect(alert?.textContent).toContain('unfinished task')
  })

  it('disables the confirm button while the name is blank', async () => {
    renderDialog(true, { onOpenChange: vi.fn() })
    const input = findInput()
    await act(async () => {
      if (input) {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
        setter?.call(input, '   ')
        input.dispatchEvent(new Event('input', { bubbles: true }))
      }
    })
    expect(findButton('Promote')?.disabled).toBe(true)
    expect(promoteMock).not.toHaveBeenCalled()
  })
})
