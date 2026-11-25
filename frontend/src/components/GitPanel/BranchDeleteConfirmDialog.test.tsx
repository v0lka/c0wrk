// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { BranchDeleteConfirmDialog } from './BranchDeleteConfirmDialog'
import type { PendingBranchDelete } from '@/hooks/useBranchActions'

let onConfirm: (mode: 'safe' | 'force') => void
let onCancel: () => void
let root: Root
let container: HTMLDivElement

function renderDialog(pending: PendingBranchDelete | null) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(
      <BranchDeleteConfirmDialog pending={pending} onConfirm={onConfirm} onCancel={onCancel} />,
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
  onConfirm = vi.fn<(mode: 'safe' | 'force') => void>()
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

describe('BranchDeleteConfirmDialog', () => {
  it('renders nothing when there is no pending action', () => {
    renderDialog(null)
    expect(document.body.textContent).not.toContain('Delete branch')
    expect(allButtons().length).toBe(0)
  })

  it('offers safe and force options for a local branch', () => {
    renderDialog({ kind: 'local', name: 'feature' })
    expect(document.body.textContent).toContain('Delete branch?')
    expect(document.body.textContent).toContain('feature')
    expect(buttonByText('Cancel')).toBeTruthy()
    expect(buttonByText('Force delete')).toBeTruthy()
    expect(buttonByText('Delete')).toBeTruthy()
  })

  it('calls onConfirm(safe) for the local Delete button', () => {
    renderDialog({ kind: 'local', name: 'feature' })
    act(() => {
      buttonByText('Delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onConfirm).toHaveBeenCalledWith('safe')
  })

  it('calls onConfirm(force) for the local Force delete button', () => {
    renderDialog({ kind: 'local', name: 'feature' })
    act(() => {
      buttonByText('Force delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onConfirm).toHaveBeenCalledWith('force')
  })

  it('calls onCancel for the Cancel button', () => {
    renderDialog({ kind: 'local', name: 'feature' })
    act(() => {
      buttonByText('Cancel')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onCancel).toHaveBeenCalled()
  })

  it('offers only a single confirm for a remote branch', () => {
    renderDialog({ kind: 'remote', name: 'feature', remote: 'origin' })
    expect(document.body.textContent).toContain('Delete remote branch?')
    expect(document.body.textContent).toContain('origin/feature')
    expect(buttonByText('Force delete')).toBeUndefined()
    expect(buttonByText('Delete')).toBeTruthy()
    act(() => {
      buttonByText('Delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onConfirm).toHaveBeenCalledWith('safe')
  })

  it('wraps long branch tokens at separators without altering the text', () => {
    const longName = 'feature/very/deeply/nested/long-branch-name'

    // Local: <wbr> break points inside the quoted `pending.name` token.
    renderDialog({ kind: 'local', name: longName })
    assertTokenBreaks(longName)
    expect(document.body.textContent).toContain(
      `"${longName}" will be deleted locally. Safe delete (git branch -d)`,
    )
    act(() => {
      root.unmount()
    })

    // Remote: the quoted `remote/name` token breaks after every separator.
    renderDialog({ kind: 'remote', name: longName, remote: 'origin' })
    assertTokenBreaks(`origin/${longName}`)
    expect(document.body.textContent).toContain(
      `"origin/${longName}" will be deleted from the remote.`,
    )
  })
})
