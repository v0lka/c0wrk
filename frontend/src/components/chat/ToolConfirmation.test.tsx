// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

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

function makeItem(
  metadata: Record<string, unknown>,
): Extract<DisplayItem, { kind: 'tool_confirm' }> {
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
        ...metadata,
      },
      timestamp: 1,
    },
  }
}

const reasonItem = (reasoning: string | undefined) =>
  makeItem(reasoning !== undefined ? { reasoning } : {})

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
    render(reasonItem('This tool creates or overwrites a file.'))
    const text = container.textContent ?? ''
    expect(text).toContain('Why approval is needed')
    expect(text).toContain('This tool creates or overwrites a file.')
    // The tool/input display and action buttons are still present.
    expect(text).toContain('write_file')
    expect(text).toContain('Allow Once')
  })

  it('renders no reason block when metadata.reasoning is empty', () => {
    render(reasonItem(''))
    const text = container.textContent ?? ''
    expect(text).not.toContain('Why approval is needed')
    // Card still renders the tool and actions.
    expect(text).toContain('write_file')
    expect(text).toContain('Allow Once')
  })

  it('renders the confirmation reason with the matched blacklist pattern', () => {
    // A shell command that matched a security.groups.execute blacklist pattern
    // is forced to confirmation; the reason names the matched pattern so the
    // user can see WHICH rule fired.
    render(
      reasonItem(
        'command "rm -rf /" matched blacklist pattern "rm\\s+-rf\\s+/" — confirmation required',
      ),
    )
    const text = container.textContent ?? ''
    expect(text).toContain('Why approval is needed')
    expect(text).toContain('matched blacklist pattern')
    expect(text).toContain('rm -rf /')
  })

  it('keeps the tool input scrollbar inside an unclipped container', () => {
    render(makeItem({ args: JSON.stringify({ command: 'x'.repeat(400) }) }))

    const inputLabel = Array.from(container.querySelectorAll('p')).find(
      (element) => element.textContent === 'Input:',
    )
    const inputContainer = inputLabel?.parentElement
    const input = inputContainer?.querySelector('pre')

    expect(inputContainer).not.toBeNull()
    expect(inputContainer?.classList.contains('overflow-hidden')).toBe(false)
    expect(input?.classList.contains('w-full')).toBe(true)
    expect(input?.classList.contains('max-h-64')).toBe(true)
    // Vertical-only scrolling: long values wrap instead of a horizontal bar.
    expect(input?.classList.contains('overflow-y-auto')).toBe(true)
    expect(input?.classList.contains('overflow-auto')).toBe(false)
    expect(input?.classList.contains('overflow-x-auto')).toBe(false)
    expect(input?.classList.contains('custom-scrollbar')).toBe(true)
  })

  it('wraps long argument values with a hanging indent instead of clipping', () => {
    render(makeItem({ args: JSON.stringify({ command: 'echo ' + 'x'.repeat(400) }) }))

    const lines = Array.from(container.querySelectorAll<HTMLSpanElement>('pre span'))
    // Pretty-printed JSON: "{", the command line, "}".
    expect(lines.length).toBeGreaterThanOrEqual(3)

    const commandLine = lines.find((element) => element.textContent?.includes('command'))
    expect(commandLine).toBeDefined()
    // Soft wrap enabled, and continuation lines indent to the value column
    // ('  "command": ' is 13 columns) rather than starting at column 0.
    expect(commandLine?.classList.contains('whitespace-pre-wrap')).toBe(true)
    expect(commandLine?.classList.contains('block')).toBe(true)
    expect(commandLine?.style.paddingLeft).toBe('13ch')
    expect(commandLine?.style.textIndent).toBe('-13ch')
    expect(commandLine?.style.overflowWrap).toBe('anywhere')

    // Structural lines keep their own indentation as the wrap column.
    const closing = lines.find((element) => element.textContent === '}')
    expect(closing?.style.paddingLeft).toBe('0ch')
    expect(closing?.style.textIndent).toBe('0ch')
  })

  it('adds action spacing without reducing the input height limit', () => {
    render(makeItem({ args: JSON.stringify({ command: 'x'.repeat(400) }) }))

    const allowButton = Array.from(container.querySelectorAll('button')).find(
      (element) => element.textContent === 'Allow Once',
    )
    const actions = allowButton?.parentElement
    const input = container.querySelector('pre')

    expect(actions?.classList.contains('pt-3')).toBe(true)
    expect(input?.classList.contains('max-h-64')).toBe(true)
  })

  it('hides the Ask Agent action when disable_judge is set', () => {
    render(makeItem({ reasoning: 'Judge already evaluated this call', disable_judge: true }))
    const text = container.textContent ?? ''
    expect(text).not.toContain('Ask Agent')
    // The manual decision actions remain available.
    expect(text).toContain('Allow Once')
    expect(text).toContain('Deny')
  })

  it('shows the Ask Agent action when disable_judge is not set', () => {
    render(reasonItem('This tool can modify your system and requires your approval.'))
    expect(container.textContent ?? '').toContain('Ask Agent')
  })
})
