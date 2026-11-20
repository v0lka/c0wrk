// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { AskUserPanel } from './AskUserPanel'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'

type AskUserItem = Extract<DisplayItem, { kind: 'ask_user' }>

function makeItem(metadata: Record<string, unknown>): AskUserItem {
  const message: ChatMessageUI = {
    id: 'au-1',
    sessionId: 'sess-1',
    type: 'ask_user',
    content: '',
    metadata,
    timestamp: 0,
  }
  return { kind: 'ask_user', message }
}

describe('AskUserPanel option hints', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.replaceChildren()
  })

  const render = (item: AskUserItem) =>
    act(() => {
      root.render(<AskUserPanel item={item} />)
    })

  const spanWithText = (text: string) =>
    Array.from(container.querySelectorAll('span')).find(el => el.textContent === text)

  it('renders the option description as a muted hint under the label', () => {
    render(makeItem({
      request_id: 'req-1',
      questions: [{
        id: 'q1',
        question: 'REST or GraphQL?',
        options: [
          { label: 'REST', value: 'rest', description: 'Simpler tooling, wide adoption' },
          { label: 'GraphQL', value: 'graphql' },
        ],
      }],
    }))

    const hint = spanWithText('Simpler tooling, wide adoption')
    expect(hint).toBeDefined()
    // Muted tone, visually distinct from the option label above it.
    expect(hint!.className).toContain('text-muted-foreground')
    expect(hint!.className).not.toContain('text-foreground')
    // The hint lives INSIDE the option's label span, under the label text.
    const labelSpan = Array.from(container.querySelectorAll('button > span'))
      .find(el => el.textContent!.startsWith('REST'))
    expect(labelSpan).toBeDefined()
    expect(labelSpan!.contains(hint!)).toBe(true)
    // Options without a description render no hint element at all.
    expect(spanWithText('GraphQL')).toBeDefined()
  })

  it('renders no hint spans when no option carries a description', () => {
    render(makeItem({
      request_id: 'req-2',
      questions: [{
        id: 'q1',
        question: 'Proceed?',
        options: [
          { label: 'Yes', value: 'yes' },
          { label: 'No', value: 'no' },
        ],
      }],
    }))

    // The only span hierarchy inside the option buttons is the label itself.
    expect(container.querySelectorAll('button span span')).toHaveLength(0)
  })

  it('drops a non-string description at the parse boundary instead of rendering junk', () => {
    // A foreign/corrupted payload must not reach the DOM as "[object Object]".
    render(makeItem({
      request_id: 'req-3',
      questions: [{
        id: 'q1',
        question: 'Pick one',
        options: [
          { label: 'A', value: 'a', description: 123 },
          { label: 'B', value: 'b' },
        ],
      }],
    }))

    expect(container.textContent).not.toContain('123')
    expect(container.querySelectorAll('button span span')).toHaveLength(0)
    expect(spanWithText('A')).toBeDefined()
  })

  it('keeps the recommended star and selection flow intact alongside hints', () => {
    render(makeItem({
      request_id: 'req-4',
      questions: [{
        id: 'q1',
        question: 'REST or GraphQL?',
        options: [
          { label: 'REST', value: 'rest', description: 'hint' },
          { label: 'GraphQL', value: 'graphql' },
        ],
        recommended: ['rest'],
      }],
    }))

    expect(container.querySelector('svg.lucide-star')).not.toBeNull()
    // Clicking the hinted option still selects it (checked state paints info bg).
    const hinted = Array.from(container.querySelectorAll('button'))
      .find(b => b.textContent!.includes('REST'))
    act(() => {
      hinted!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(hinted!.className).toContain('bg-info/10')
  })
})
