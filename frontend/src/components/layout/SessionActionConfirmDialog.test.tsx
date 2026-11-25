// @vitest-environment jsdom
//
// Unit tests for SessionActionConfirmDialog: no dedicated file existed, so
// this follows the BranchDeleteConfirmDialog.test.tsx harness. Covers the
// single/group and delete/archive copy variants, the confirm/cancel wiring,
// and the PathWithBreaks long-token rendering of `sessionName`.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { SessionActionConfirmDialog } from './SessionActionConfirmDialog'
import type { PendingSessionAction } from '@/hooks/useSessionActions'

let onConfirm: () => void
let onCancel: () => void
let root: Root
let container: HTMLDivElement

function makePending(overrides: Partial<PendingSessionAction> = {}): PendingSessionAction {
  return {
    kind: 'delete',
    sessionId: 's-1',
    sessionName: 'my-session',
    isArchived: false,
    ...overrides,
  }
}

function renderDialog(pending: PendingSessionAction | null) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(
      <SessionActionConfirmDialog pending={pending} onConfirm={onConfirm} onCancel={onCancel} />,
    )
  })
}

// Radix Dialog portals content into document.body.
function allButtons(): HTMLButtonElement[] {
  return Array.from(document.body.querySelectorAll('button'))
}

function buttonByText(text: string): HTMLButtonElement | undefined {
  return allButtons().find((b) => b.textContent?.includes(text))
}

/**
 * Long-token assertions for PathWithBreaks: the token renders as ONE span
 * whose textContent is char-identical to the source (a <wbr> is an element,
 * not a character), with exactly one <wbr> per `/`/`\` separator, each
 * sitting immediately after its separator, and no injected zero-width chars.
 */
function assertTokenBreaks(token: string, scope: ParentNode = document.body): void {
  const text = scope.textContent ?? ''
  expect(text).toContain(token)
  expect(text).not.toContain('\u200b')
  expect(text).not.toContain('\u200c')
  expect(text).not.toContain('\u200d')
  expect(text).not.toContain('\ufeff')
  const span = Array.from(scope.querySelectorAll('span')).find((s) => s.textContent === token)
  expect(span).not.toBeUndefined()
  const wbrs = span!.querySelectorAll('wbr')
  expect(wbrs.length).toBe((token.match(/[/\\]/g) ?? []).length)
  for (const wbr of wbrs) {
    const prev = (wbr.previousSibling?.textContent ?? '').slice(-1)
    expect(prev === '/' || prev === '\\').toBe(true)
  }
}

beforeEach(() => {
  onConfirm = vi.fn<() => void>()
  onCancel = vi.fn<() => void>()
  document.body.innerHTML = ''
})

afterEach(() => {
  if (root) {
    act(() => {
      root.unmount()
    })
  }
  if (container) container.remove()
  document.body.innerHTML = ''
})

describe('SessionActionConfirmDialog', () => {
  it('renders nothing when there is no pending action', () => {
    renderDialog(null)
    expect(document.body.textContent).not.toContain('Delete session')
    expect(allButtons().length).toBe(0)
  })

  it('names the session in delete copy for a single busy session', () => {
    renderDialog(makePending())
    expect(document.body.textContent).toContain('Delete session?')
    expect(document.body.textContent).toContain(
      '"my-session" has a task in progress or unfinished. Deleting will cancel it and permanently remove the session and its files.',
    )
    expect(buttonByText('Cancel')).toBeTruthy()
    expect(buttonByText('Delete')).toBeTruthy()
    expect(buttonByText('Archive')).toBeUndefined()
  })

  it('names the session in archive copy', () => {
    renderDialog(makePending({ kind: 'archive' }))
    expect(document.body.textContent).toContain('Archive session?')
    expect(document.body.textContent).toContain(
      '"my-session" has a task in progress or unfinished. Archiving will cancel it and make the session read-only.',
    )
    expect(buttonByText('Archive')).toBeTruthy()
  })

  it('uses count-aware group copy for a branch group', () => {
    renderDialog(
      makePending({
        sessionName: 'feature/long-branch',
        sessionIds: ['s-1', 's-2'],
      }),
    )
    expect(document.body.textContent).toContain('Delete sessions?')
    expect(document.body.textContent).toContain(
      'Branch "feature/long-branch" groups 2 sessions. Deleting will cancel any running tasks and permanently remove the sessions and their files.',
    )
  })

  it('keeps the singular grammatical for a single-member group', () => {
    renderDialog(makePending({ sessionName: 'solo/branch', sessionIds: ['s-1'] }))
    expect(document.body.textContent).toContain('Delete session?')
    expect(document.body.textContent).toContain(
      'Branch "solo/branch" groups 1 session. Deleting will cancel any running tasks and permanently remove the session and its files.',
    )
  })

  it('calls onConfirm for the confirm button', () => {
    renderDialog(makePending())
    act(() => {
      buttonByText('Delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onConfirm).toHaveBeenCalledTimes(1)
    expect(onCancel).not.toHaveBeenCalled()
  })

  it('calls onCancel for the Cancel button', () => {
    renderDialog(makePending())
    act(() => {
      buttonByText('Cancel')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('wraps a long session-name token at separators without altering the text', () => {
    const longName = 'sess/very/deeply/nested/long-session-name'
    renderDialog(makePending({ sessionName: longName }))
    assertTokenBreaks(longName)
    // The quoted context is char-identical to the pre-PathWithBreaks copy.
    expect(document.body.textContent).toContain(
      `"${longName}" has a task in progress or unfinished. Deleting will cancel it`,
    )
  })
})
