// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import { ChatMessageRenderer } from './ChatMessageRenderer'

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

function message(id: string, type: ChatMessageUI['type'], content: string): ChatMessageUI {
  return {
    id,
    sessionId: 'session-1',
    type,
    content,
    metadata: {},
    timestamp: 0,
  }
}

const items: DisplayItem[] = [
  { kind: 'user', message: message('user-1', 'user', 'First question') },
  { kind: 'assistant', message: message('assistant-1', 'assistant', 'First answer') },
  { kind: 'user', message: message('user-2', 'user', 'Second question') },
  { kind: 'assistant', message: message('assistant-2', 'assistant', 'Second answer') },
]

function renderRenderer(): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(
      <ChatMessageRenderer
        items={items}
        stickyUserMessages
        trailingContent={<div data-testid="trailing">Streaming</div>}
      />,
    )
  })
  return container
}

describe('ChatMessageRenderer sticky user turns', () => {
  beforeEach(() => {
    document.body.replaceChildren()
  })

  afterEach(() => {
    act(() => root?.unmount())
    root = null
  })

  it('groups each user message with content up to the next user message', () => {
    const container = renderRenderer()
    const turns = Array.from(container.children)

    expect(turns).toHaveLength(2)
    expect(turns[0]?.querySelector('[data-message-id="user-1"]')).not.toBeNull()
    expect(turns[0]?.textContent).toContain('First answer')
    expect(turns[0]?.querySelector('[data-message-id="user-2"]')).toBeNull()
    expect(turns[1]?.querySelector('[data-message-id="user-2"]')).not.toBeNull()
    expect(turns[1]?.textContent).toContain('Second answer')
  })

  it('renders every history user exactly once with the sticky DOM contract', () => {
    const container = renderRenderer()

    for (const id of ['user-1', 'user-2']) {
      const matches = container.querySelectorAll(`[data-message-id="${id}"]`)

      expect(matches).toHaveLength(1)
      expect(matches[0]?.classList.contains('sticky')).toBe(true)
      expect(matches[0]?.classList.contains('top-0')).toBe(true)
    }

    expect(container.querySelectorAll('[data-message-id^="user-"]')).toHaveLength(2)
  })

  it('keeps trailing activity inside the last turn boundary', () => {
    const container = renderRenderer()
    const turns = Array.from(container.children)

    expect(turns).toHaveLength(2)
    expect(turns[0]?.querySelector('[data-testid="trailing"]')).toBeNull()
    expect(turns[1]?.querySelector('[data-testid="trailing"]')).not.toBeNull()
  })
})
