// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Mock the stores and runtime the component selects.
const { updateMessage, emit, openFile, setFileContent } = vi.hoisted(() => ({
  updateMessage: vi.fn(),
  emit: vi.fn(),
  openFile: vi.fn(),
  setFileContent: vi.fn(),
}))
vi.mock('@/stores/chatStore', () => ({
  useChatStore: {
    getState: () => ({ updateMessage }),
  },
}))
vi.mock('@/stores/sessionStore', () => ({
  useSessionStore: (selector: (s: { activeSessionId: string | null }) => unknown) =>
    selector({ activeSessionId: 'sess-1' }),
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: {
    getState: () => ({ openFile, setFileContent }),
  },
}))
// Mock the Wails event emitter so no real runtime binding is touched.
vi.mock('@/api/runtime', () => ({ emit }))

import { PlanApprovalPanel } from './PlanApprovalPanel'
import type { DisplayItem } from '@/types/messages'

type PlanReviewItem = Extract<DisplayItem, { kind: 'plan_review' }>

function makeItem(): PlanReviewItem {
  return {
    kind: 'plan_review',
    message: {
      id: 'msg-1',
      sessionId: 'sess-1',
      type: 'plan_review',
      content: '# Plan',
      metadata: { request_id: 'req-1' },
      timestamp: 1,
    },
  }
}

function render(el: React.ReactElement): { container: HTMLElement; root: Root } {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  act(() => { root.render(el) })
  return { container, root }
}

/** Open the feedback editor and focus its textarea. */
function openFeedbackEditor(container: HTMLElement): HTMLTextAreaElement {
  const requestChangesBtn = Array.from(container.querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').includes('Request Changes'),
  )! as HTMLButtonElement
  act(() => { requestChangesBtn.click() })
  const textarea = container.querySelector('textarea') as HTMLTextAreaElement
  expect(textarea).toBeTruthy()
  return textarea
}

/** Set a controlled textarea value in a way React detects (see BranchPicker.test). */
function setTextareaValue(textarea: HTMLTextAreaElement, value: string) {
  act(() => {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
    setter?.call(textarea, value)
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function pressEnter(textarea: HTMLTextAreaElement, shiftKey = false): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { key: 'Enter', shiftKey, bubbles: true, cancelable: true })
  act(() => { textarea.dispatchEvent(event) })
  return event
}

describe('PlanApprovalPanel', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    vi.clearAllMocks()
    const r = render(<PlanApprovalPanel item={makeItem()} />)
    container = r.container
    root = r.root
  })

  function cleanup() {
    act(() => { root.unmount() })
  }

  it('Enter sends the feedback as request_changes (chat-input parity)', () => {
    try {
      const textarea = openFeedbackEditor(container)
      setTextareaValue(textarea, 'more tests please')

      const event = pressEnter(textarea)
      // The default (newline insertion) is suppressed so Enter acts as Send.
      expect(event.defaultPrevented).toBe(true)
      expect(emit).toHaveBeenCalledTimes(1)
      expect(emit).toHaveBeenCalledWith('plan_approval_response', {
        request_id: 'req-1',
        decision: 'request_changes',
        feedback: 'more tests please',
      })
      expect(updateMessage).toHaveBeenCalled()
    } finally {
      cleanup()
    }
  })

  it('Shift+Enter inserts a newline instead of sending', () => {
    try {
      const textarea = openFeedbackEditor(container)
      setTextareaValue(textarea, 'first line')

      const event = pressEnter(textarea, /* shiftKey */ true)
      // Not default-prevented: the browser performs the native newline insert.
      expect(event.defaultPrevented).toBe(false)
      expect(emit).not.toHaveBeenCalled()
      expect(updateMessage).not.toHaveBeenCalled()
    } finally {
      cleanup()
    }
  })

  it('Enter with empty feedback does not send (chat-input parity)', () => {
    try {
      const textarea = openFeedbackEditor(container)

      pressEnter(textarea)
      expect(emit).not.toHaveBeenCalled()
      expect(updateMessage).not.toHaveBeenCalled()
    } finally {
      cleanup()
    }
  })

  it('Send Feedback button still works alongside the keymap', () => {
    try {
      const textarea = openFeedbackEditor(container)
      setTextareaValue(textarea, 'button path')

      const sendBtn = Array.from(container.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').includes('Send Feedback'),
      )! as HTMLButtonElement
      act(() => { sendBtn.click() })
      expect(emit).toHaveBeenCalledWith('plan_approval_response', {
        request_id: 'req-1',
        decision: 'request_changes',
        feedback: 'button path',
      })
    } finally {
      cleanup()
    }
  })
})
