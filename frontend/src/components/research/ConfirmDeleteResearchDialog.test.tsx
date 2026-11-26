// @vitest-environment jsdom
// ConfirmDeleteResearchDialog — extracted from ResearchProjectPicker (the
// picker's own tests cover the delete flow end-to-end through the dropdown;
// these tests pin the dialog component's own contract: target rendering,
// button wiring, the deleting lock, and the inline error).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ConfirmDeleteResearchDialog } from './ConfirmDeleteResearchDialog'
import { assertTokenBreaks } from '@/test/wrapAssertions'

// Radix Dialog positioning observes content with ResizeObserver, which jsdom
// does not provide (same stub as the picker tests).
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

let container: HTMLDivElement
let root: Root
const onConfirm = vi.fn()
const onClose = vi.fn()

function renderDialog(props: {
  target: { id: string; title: string } | null
  deleting?: boolean
  deleteError?: string | null
}): HTMLElement {
  act(() => {
    root.render(
      <ConfirmDeleteResearchDialog
        target={props.target}
        deleting={props.deleting ?? false}
        deleteError={props.deleteError ?? null}
        onConfirm={onConfirm}
        onClose={onClose}
      />,
    )
  })
  return document.body
}

function dialogEl(): HTMLElement | null {
  return document.body.querySelector('[role="dialog"]')
}

function button(label: string): HTMLButtonElement {
  const btn = Array.from(dialogEl()!.querySelectorAll('button')).find(
    (b) => b.textContent === label,
  )
  expect(btn).toBeDefined()
  return btn as HTMLButtonElement
}

beforeEach(() => {
  onConfirm.mockClear()
  onClose.mockClear()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

describe('ConfirmDeleteResearchDialog', () => {
  it('stays closed while no target is set', () => {
    renderDialog({ target: null })
    expect(dialogEl()).toBeNull()
  })

  it('renders the target id, title, and the permanence warning', () => {
    renderDialog({ target: { id: 'R-007', title: 'Fork investigation' } })
    const text = dialogEl()!.textContent!
    expect(text).toContain('R-007')
    expect(text).toContain('Fork investigation')
    expect(text).toContain('cannot be undone')
  })

  it('wraps a long title at separators without altering the rendered text', () => {
    renderDialog({ target: { id: 'R-007', title: 'very/long/user/authored research title' } })
    assertTokenBreaks('very/long/user/authored research title')
    // The surrounding copy stays char-identical to the pre-PathWithBreaks text.
    expect(dialogEl()!.textContent).toContain(
      'R-007 — very/long/user/authored research title. Its directory tree',
    )
  })

  it('confirm and cancel invoke their callbacks', () => {
    renderDialog({ target: { id: 'R-001', title: 'First research' } })
    act(() => {
      button('Delete').click()
    })
    expect(onConfirm).toHaveBeenCalledTimes(1)

    act(() => {
      button('Cancel').click()
    })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('locks both buttons while the delete RPC is in flight', () => {
    renderDialog({ target: { id: 'R-001', title: 'First research' }, deleting: true })
    expect(button('Delete').disabled).toBe(true)
    expect(button('Cancel').disabled).toBe(true)
  })

  it('renders the delete error inline (retry or cancel stays possible)', () => {
    renderDialog({
      target: { id: 'R-001', title: 'First research' },
      deleteError: 'The workspace project changed — re-open the research panel and retry.',
    })
    const alert = dialogEl()!.querySelector('[role="alert"]')
    expect(alert?.textContent).toContain('workspace project changed')
    // Not deleting → the buttons stay usable for a retry or cancel.
    expect(button('Delete').disabled).toBe(false)
  })
})
