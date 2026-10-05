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

function makeSession(overrides: Partial<SessionItemSummary> = {}): SessionItemSummary {
  return {
    id: 's1',
    project_id: 'proj-1',
    name: 'Session One',
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

  it('disables only fork for a session whose task failed (unfinished); archive/delete stay enabled', () => {
    statusMock.value = 'failed'
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
    )
    const btns = actionButtons(container)
    const [, fork, , archive, del] = btns
    expect(fork?.disabled).toBe(true)
    expect(archive?.disabled).toBe(false)
    expect(del?.disabled).toBe(false)
  })

  it('disables fork for a pending (HITL-blocked, still-running) session', () => {
    // A running task blocked on a confirmation prompt is not settled: the
    // backend rejects a fork of it, so the row must not offer one.
    statusMock.value = 'pending'
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
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

describe('SessionItem status dot', () => {
  it('renders a red dot for a failed session', () => {
    statusMock.value = 'failed'
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
    )
    const dot = container.querySelector('[title^="Task failed"]')
    expect(dot).not.toBeNull()
    expect(dot?.className).toContain('bg-destructive')
  })

  it('does not render the failure dot for an idle session', () => {
    statusMock.value = 'idle'
    const { container } = render(
      <SessionItem variant="flat" session={makeSession()} isActive={false} onSelect={vi.fn()} onRename={vi.fn()} onArchive={vi.fn()} onPin={vi.fn()} onFork={vi.fn()} onDelete={vi.fn()} />,
    )
    expect(container.querySelector('[title^="Task failed"]')).toBeNull()
  })
})
