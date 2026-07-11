// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  g.IS_REACT_ACT_ENVIRONMENT = true
})

// Mock the chat store so the component renders without a real Zustand store.
// The component selects updateMessage and setActivityStatus individually, so
// the mock must invoke the selector with state containing both.
vi.mock('@/stores/chatStore', () => ({
  useChatStore: (selector: (s: { updateMessage: () => void; setActivityStatus: () => void }) => unknown) =>
    selector({ updateMessage: vi.fn(), setActivityStatus: vi.fn() }),
}))

// Mock the on-demand judge-events hook (it subscribes to runtime events that
// are not relevant to rendering the backend-provided reason).
vi.mock('@/hooks/events/useToolJudgeEvents', () => ({
  useToolJudgeEvents: vi.fn(),
}))

// Mock the runtime so no real window.runtime binding is touched.
vi.mock('@/api/runtime', () => ({
  emit: vi.fn(),
  onSessionEvent: vi.fn(() => () => {}),
  reportDroppedEvent: vi.fn(),
}))

import { ToolConfirmation } from './ToolConfirmation'
import type { DisplayItem } from '@/types/messages'

function makeItem(reasoning: string | undefined): Extract<DisplayItem, { kind: 'tool_confirm' }> {
  return {
    kind: 'tool_confirm',
    message: {
      id: 'tool-confirm-c1',
      sessionId: 'sess-1',
      type: 'tool_confirm',
      content: 'Confirm: write_file',
      metadata: {
        confirm_id: 'c1',
        tool: 'write_file',
        args: '{"path":"/x/f.txt","content":"hi"}',
        ...(reasoning !== undefined ? { reasoning } : {}),
      },
      timestamp: 1,
    },
  }
}

describe('ToolConfirmation — confirmation reason', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  const render = (item: Extract<DisplayItem, { kind: 'tool_confirm' }>) =>
    act(() => {
      root.render(<ToolConfirmation item={item} />)
    })

  it('renders the human-readable reason when metadata.reasoning is present', () => {
    render(makeItem('This tool creates or overwrites a file.'))
    const text = container.textContent ?? ''
    expect(text).toContain('Why approval is needed')
    expect(text).toContain('This tool creates or overwrites a file.')
    // The tool/input display and action buttons are still present.
    expect(text).toContain('write_file')
    expect(text).toContain('Allow Once')
  })

  it('renders no reason block when metadata.reasoning is empty', () => {
    render(makeItem(''))
    const text = container.textContent ?? ''
    expect(text).not.toContain('Why approval is needed')
    // Card still renders the tool and actions.
    expect(text).toContain('write_file')
    expect(text).toContain('Allow Once')
  })
})
