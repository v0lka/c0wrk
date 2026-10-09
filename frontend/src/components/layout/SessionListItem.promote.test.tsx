// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Drive the row's status directly so tests don't depend on the chat store ---
const { statusMock } = vi.hoisted(() => ({ statusMock: { value: 'idle' as string } }))
vi.mock('@/hooks/useSessionStatusIndicator', () => ({
  useSessionStatusIndicator: () => statusMock.value,
}))

import { SessionItem, type SessionItemSummary } from './SessionListItem'
import { NO_PROJECT_ID } from '@/types/models'

function makeSession(overrides: Partial<SessionItemSummary> = {}): SessionItemSummary {
  return {
    id: 's1',
    project_id: NO_PROJECT_ID,
    name: 'Chat Session',
    archived: false,
    pinned: false,
    last_active_at: new Date(Date.now() - 60_000).toISOString(),
    ...overrides,
  }
}

function render(ui: React.ReactNode): { root: Root; container: HTMLElement } {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => {
    root.render(ui)
  })
  return { root, container }
}

/** With the promote action present the order is:
 *  0 Promote | 1 Pin | 2 Fork | 3 Rename | 4 Archive | 5 Delete */
function actionButtons(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll('button'))
}

beforeEach(() => {
  statusMock.value = 'idle'
})

describe('SessionItem promote action', () => {
  it('renders Promote first for a No Project row and wires the callback', () => {
    const onPromote = vi.fn()
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onPromote={onPromote} onDelete={vi.fn()} />,
    )
    const btns = actionButtons(container)
    expect(btns.length).toBe(6)
    const promote = btns[0]
    expect(promote?.title).toBe('Promote to project')
    expect(promote?.disabled).toBe(false)
    act(() => {
      promote?.click()
    })
    expect(onPromote).toHaveBeenCalledTimes(1)
  })

  it('is busy-gated like fork: disabled with a promote-specific reason while a task runs', () => {
    statusMock.value = 'active'
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onPromote={vi.fn()} onDelete={vi.fn()} />,
    )
    const promote = actionButtons(container)[0]
    expect(promote?.disabled).toBe(true)
    // The disabled button's own title carries the reason (mirrored by the
    // focusable wrapper for pointer-less access).
    expect(promote?.title).toContain('Cannot promote while a task is running')
  })

  it('never renders for a CODE-project row even when the callback is wired', () => {
    const { container } = render(
      <SessionItem variant="flat" session={makeSession({ project_id: 'proj-1' })} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onPromote={vi.fn()} onDelete={vi.fn()} />,
    )
    const btns = actionButtons(container)
    // Baseline overlay: Pin, Fork, Rename, Archive, Delete.
    expect(btns.length).toBe(5)
    expect(btns.some((b) => b.title === 'Promote to project')).toBe(false)
  })

  it('never renders without the callback, even for a No Project row', () => {
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
    )
    expect(actionButtons(container).length).toBe(5)
    expect(actionButtons(container).some((b) => b.title === 'Promote to project')).toBe(false)
  })
})
