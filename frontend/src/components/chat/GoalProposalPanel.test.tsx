// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  g.IS_REACT_ACT_ENVIRONMENT = true
})

// Mock the stores with minimal state the component selects.
const { updateMessage, clearPendingProposal, confirmGoal, cancelGoal, clarifyGoal } = vi.hoisted(() => ({
  updateMessage: vi.fn(),
  clearPendingProposal: vi.fn(),
  confirmGoal: vi.fn(async () => {}),
  cancelGoal: vi.fn(async () => {}),
  clarifyGoal: vi.fn(async () => {}),
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
vi.mock('@/stores/goalStore', () => ({
  useGoalStore: {
    getState: () => ({ clearPendingProposal }),
  },
}))

// Mock the api so no real Wails binding is touched. The component imports
// { goal } from '@/api' (the index re-export), so mock that module.
vi.mock('@/api', () => ({
  goal: { confirmGoal, cancelGoal, clarifyGoal },
}))
vi.mock('@/api/runtime', () => ({ getApp: () => ({}) }))
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

import { GoalProposalPanel } from './GoalProposalPanel'
import type { DisplayItem } from '@/types/messages'

type GoalProposalItem = Extract<DisplayItem, { kind: 'goal_proposal' }>

function makeItem(opts: { needsClarification?: boolean; condition?: string; verify?: string; clarification?: string; requestId?: string }): GoalProposalItem {
  return {
    kind: 'goal_proposal',
    message: {
      id: 'msg-1',
      sessionId: 'sess-1',
      type: 'goal_proposal',
      content: '',
      metadata: { request_id: opts.requestId ?? 'req-1' },
      timestamp: 1,
    },
    condition: opts.condition ?? 'Make tests pass',
    verify: opts.verify ?? 'go test ./...',
    clarification: opts.clarification,
    needs_clarification: opts.needsClarification ?? false,
  }
}

function render(el: React.ReactElement): { container: HTMLElement; root: Root } {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  act(() => { root.render(el) })
  return { container, root }
}

describe('GoalProposalPanel', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    vi.clearAllMocks()
    const r = render(<GoalProposalPanel item={makeItem({})} />)
    container = r.container
    root = r.root
  })

  function cleanup() {
    act(() => { root.unmount() })
  }

  it('renders Approve + Cancel by default', () => {
    try {
      const buttons = container.querySelectorAll('button')
      const labels = Array.from(buttons).map((b) => b.textContent ?? '')
      expect(labels.some((l) => l.includes('Approve'))).toBe(true)
      expect(labels.some((l) => l.includes('Cancel'))).toBe(true)
      expect(labels.some((l) => l.includes('Send'))).toBe(false)
    } finally {
      cleanup()
    }
  })

  it('approve calls confirmGoal and marks resolved', async () => {
    try {
      const approveBtn = Array.from(container.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').includes('Approve'),
      )! as HTMLButtonElement
      await act(async () => { approveBtn.click() })
      expect(confirmGoal).toHaveBeenCalledWith('sess-1', 'req-1', 'Make tests pass', 'go test ./...')
      expect(updateMessage).toHaveBeenCalled()
    } finally {
      cleanup()
    }
  })

  it('cancel calls cancelGoal and marks resolved', async () => {
    try {
      const cancelBtn = Array.from(container.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').includes('Cancel'),
      )! as HTMLButtonElement
      await act(async () => { cancelBtn.click() })
      expect(cancelGoal).toHaveBeenCalledWith('sess-1', 'req-1')
    } finally {
      cleanup()
    }
  })

  it('needs_clarification mode renders Send (clarify) instead of Approve', () => {
    // Re-render with a needs_clarification item.
    act(() => { root.unmount() })
    const r = render(<GoalProposalPanel item={makeItem({ needsClarification: true, clarification: 'which scope?' })} />)
    container = r.container
    root = r.root
    try {
      const labels = Array.from(container.querySelectorAll('button')).map((b) => b.textContent ?? '')
      expect(labels.some((l) => l.includes('Send'))).toBe(true)
      expect(labels.some((l) => l.includes('Approve'))).toBe(false)
    } finally {
      cleanup()
    }
  })

  it('Send button calls clarifyGoal with the refined verify text', async () => {
    act(() => { root.unmount() })
    const r = render(<GoalProposalPanel item={makeItem({ needsClarification: true, clarification: 'which scope?' })} />)
    container = r.container
    root = r.root
    try {
      // The Send button is disabled while verify is empty (its initial state in
      // needs_clarification mode). Simulate the user typing a refinement into
      // the verify textarea using React 19's native setter so the controlled
      // input updates state and enables the button.
      const verifyTextarea = container.querySelector('textarea#goal-verify-msg-1') as HTMLTextAreaElement
      expect(verifyTextarea).toBeTruthy()
      const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value')!.set!
      await act(async () => {
        nativeSetter.call(verifyTextarea, 'verify via tests')
        verifyTextarea.dispatchEvent(new Event('input', { bubbles: true }))
      })

      const sendBtn = Array.from(container.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').includes('Send'),
      )! as HTMLButtonElement
      expect(sendBtn.disabled).toBe(false)
      await act(async () => { sendBtn.click() })
      // clarifyGoal(sessionId, requestId, clarification) — the clarification
      // carries the user's refinement typed into the verify field.
      expect(clarifyGoal).toHaveBeenCalled()
      expect(clarifyGoal).toHaveBeenCalledWith('sess-1', 'req-1', 'verify via tests')
    } finally {
      cleanup()
    }
  })

  it('renders settled "Clarification sent" card for decision=clarify', () => {
    act(() => { root.unmount() })
    const item = makeItem({})
    item.message.metadata = { request_id: 'req-1', resolved: true, decision: 'clarify' }
    const r = render(<GoalProposalPanel item={item} />)
    try {
      expect(r.container.textContent).toContain('Clarification sent')
    } finally {
      act(() => { r.root.unmount() })
    }
  })
})
