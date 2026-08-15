// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ActiveGoal } from '@/stores/goalStore'

// Mock the stores with minimal state the component selects.
const { updateMessage, clearPendingProposal, confirmGoal, cancelGoal, useActiveGoal } = vi.hoisted(() => ({
  updateMessage: vi.fn(),
  clearPendingProposal: vi.fn(),
  confirmGoal: vi.fn(async () => {}),
  cancelGoal: vi.fn(async () => {}),
  // Typed so mockReturnValue accepts an ActiveGoal | undefined (the real hook's
  // return type); an untyped vi.fn() would infer a () => undefined signature and
  // reject a goal snapshot argument.
  useActiveGoal: vi.fn<(sessionId: string | null) => ActiveGoal | undefined>(() => undefined),
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
  useActiveGoal,
}))

// Mock the api so no real Wails binding is touched. The component imports
// { goal } from '@/api' (the index re-export), so mock that module.
vi.mock('@/api', () => ({
  goal: { confirmGoal, cancelGoal },
}))
vi.mock('@/api/runtime', () => ({ getApp: () => ({}) }))
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

import { GoalProposalPanel } from './GoalProposalPanel'
import type { DisplayItem } from '@/types/messages'

type GoalProposalItem = Extract<DisplayItem, { kind: 'goal_proposal' }>

function makeItem(opts: { condition?: string; verify?: string; requestId?: string; verificationMode?: string }): GoalProposalItem {
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
    verification_mode: opts.verificationMode ?? 'executable',
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
      // confirmGoal now carries the (default) verification mode as the 5th arg.
      expect(confirmGoal).toHaveBeenCalledWith('sess-1', 'req-1', 'Make tests pass', 'go test ./...', 'executable')
      expect(updateMessage).toHaveBeenCalled()
    } finally {
      cleanup()
    }
  })

  it('renders the chosen verification mode in the panel', () => {
    // The default proposal mode ('executable') shows the "Executable check"
    // option as the selected (aria-checked) radio.
    try {
      const radios = Array.from(container.querySelectorAll('button[role="radio"]'))
      const selected = radios.find((b) => b.getAttribute('aria-checked') === 'true')
      expect(selected).toBeTruthy()
      expect(selected!.textContent).toContain('Executable check')
    } finally {
      cleanup()
    }
  })

  it('approve sends the user-edited verification mode', async () => {
    // Switch the segmented toggle to "Re-run verification", then approve: the
    // edited mode ('re_derivation') must reach the backend via confirmGoal.
    try {
      const reRunBtn = Array.from(container.querySelectorAll('button[role="radio"]')).find(
        (b) => (b.textContent ?? '').includes('Re-run verification'),
      )! as HTMLButtonElement
      await act(async () => { reRunBtn.click() })

      const approveBtn = Array.from(container.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').includes('Approve'),
      )! as HTMLButtonElement
      await act(async () => { approveBtn.click() })
      expect(confirmGoal).toHaveBeenCalledWith('sess-1', 'req-1', 'Make tests pass', 'go test ./...', 're_derivation')
    } finally {
      cleanup()
    }
  })

  it('falls back to executable for an empty/unknown proposed mode', () => {
    act(() => { root.unmount() })
    const r = render(<GoalProposalPanel item={makeItem({ verificationMode: '' })} />)
    container = r.container
    root = r.root
    try {
      const selected = Array.from(container.querySelectorAll('button[role="radio"]')).find(
        (b) => b.getAttribute('aria-checked') === 'true',
      )
      expect(selected).toBeTruthy()
      expect(selected!.textContent).toContain('Executable check')
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

  it('renders graceful "Dismissed" card for a legacy decision=clarify', () => {
    // A persisted proposal from the removed needs_clarification mode carries
    // decision='clarify', which no longer matches the 'approve'/'cancel'
    // settled branches. With resolved=true it must fall through to the generic
    // isResolved → 'Dismissed' card (graceful render of old data).
    act(() => { root.unmount() })
    const item = makeItem({})
    item.message.metadata = { request_id: 'req-1', resolved: true, decision: 'clarify' }
    const r = render(<GoalProposalPanel item={item} />)
    try {
      expect(r.container.textContent).toContain('Dismissed')
      expect(r.container.textContent).not.toContain('Clarification sent')
    } finally {
      act(() => { r.root.unmount() })
    }
  })

  it('settled approve card shows "Goal approved" when no verdict yet', () => {
    act(() => { root.unmount() })
    // No active goal / no verdict → plain placeholder.
    useActiveGoal.mockReturnValue(undefined)
    const item = makeItem({})
    item.message.metadata = { request_id: 'req-1', resolved: true, decision: 'approve' }
    const r = render(<GoalProposalPanel item={item} />)
    try {
      expect(r.container.textContent).toContain('Goal approved')
    } finally {
      useActiveGoal.mockReturnValue(undefined)
      act(() => { r.root.unmount() })
    }
  })

  it('settled approve card surfaces the verdict badge, reason, and clickable file evidence', () => {
    act(() => { root.unmount() })
    useActiveGoal.mockReturnValue({
      condition: 'ship it',
      status: 'met',
      turn: 3,
      verdict: 'met',
      reason: 'all green',
      evidence: [
        { type: 'file', ref: 'core/main.go', summary: 'the fix' },
        { type: 'command', ref: 'go test ./...', summary: 'all pass' },
      ],
    })
    const item = makeItem({})
    item.message.metadata = { request_id: 'req-1', resolved: true, decision: 'approve' }
    const r = render(<GoalProposalPanel item={item} />)
    try {
      const text = r.container.textContent ?? ''
      // Verdict badge + reason + evidence summary are surfaced.
      expect(text).toContain('met')
      expect(text).toContain('all green')
      expect(text).toContain('the fix')
      // The plain placeholder is replaced by the verdict body.
      expect(text).not.toContain('Goal approved')
      // File-typed evidence renders as a clickable FileLink (role=button, full
      // path in the title hint), and the non-file evidence ref shows inline.
      const fileLink = r.container.querySelector('[role="button"][title="core/main.go"]')
      expect(fileLink).toBeTruthy()
      expect(fileLink!.textContent).toContain('main.go')
      expect(text).toContain('go test ./...')
    } finally {
      useActiveGoal.mockReturnValue(undefined)
      act(() => { r.root.unmount() })
    }
  })
})
