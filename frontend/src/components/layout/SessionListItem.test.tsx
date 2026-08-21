// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'

// --- Drive the row's status directly so tests don't depend on the chat store ---
const { statusMock } = vi.hoisted(() => ({ statusMock: { value: 'idle' as string } }))
vi.mock('@/hooks/useSessionStatusIndicator', () => ({
  useSessionStatusIndicator: () => statusMock.value,
}))

import { SessionItem, type SessionItemSummary } from './SessionListItem'

function makeSession(overrides: Partial<SessionItemSummary> = {}): SessionItemSummary {
  return {
    id: 's1',
    name: 'Session One',
    archived: false,
    pinned: false,
    last_active_at: new Date(Date.now() - 60_000).toISOString(),
    has_unfinished_task: false,
    ...overrides,
  }
}

function render(ui: React.ReactNode): { root: Root; container: HTMLElement } {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => {
    root.render(<TooltipProvider>{ui}</TooltipProvider>)
  })
  return { root, container }
}

/**
 * The action overlay renders buttons in a fixed order:
 * 0 Pin | 1 Fork | 2 Rename | 3 Archive | 4 Delete
 */
function actionButtons(container: HTMLElement): HTMLButtonElement[] {
  // The flat variant's outer element is a div[role=button]; the only real
  // <button> descendants are the SessionAction triggers inside the overlay.
  return Array.from(container.querySelectorAll('button'))
}

beforeEach(() => {
  statusMock.value = 'idle'
})

describe('SessionItem busy-state guards', () => {
  it('enables fork/archive/delete for an idle session', () => {
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
    )
    const btns = actionButtons(container)
    expect(btns.length).toBe(5)
    const [, fork, , archive, del] = btns
    expect(fork?.disabled).toBe(false)
    expect(archive?.disabled).toBe(false)
    expect(del?.disabled).toBe(false)
  })

  it('disables only fork while a task is running (active); archive/delete stay enabled', () => {
    statusMock.value = 'active'
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
    )
    const btns = actionButtons(container)
    const [, fork, , archive, del] = btns
    expect(fork?.disabled).toBe(true)
    expect(archive?.disabled).toBe(false)
    expect(del?.disabled).toBe(false)
  })

  it('disables only fork for a session with an unfinished task; archive/delete stay enabled', () => {
    const { container } = render(
      <SessionItem variant="flat" session={makeSession({ has_unfinished_task: true })} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
    )
    const btns = actionButtons(container)
    const [, fork, , archive, del] = btns
    expect(fork?.disabled).toBe(true)
    expect(archive?.disabled).toBe(false)
    expect(del?.disabled).toBe(false)
  })

  it('still allows pin & rename for a busy session', () => {
    statusMock.value = 'active'
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
    )
    const btns = actionButtons(container)
    const [pin, , rename] = btns
    expect(pin?.disabled).toBe(false)
    expect(rename?.disabled).toBe(false)
  })
})
