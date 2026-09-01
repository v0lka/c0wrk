// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ToolCard } from './ToolCard'
import type { DisplayItem } from '@/types/messages'
import { TooltipProvider } from '@/components/ui/tooltip'

type ToolItem = Extract<DisplayItem, { kind: 'tool' }>

function makeItem(overrides: Partial<ToolItem> & Pick<ToolItem, 'toolName' | 'args'>): ToolItem {
  return {
    kind: 'tool',
    id: 'tool-1',
    status: 'success',
    ...overrides,
  }
}

describe('ToolCard', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    // Radix tooltip positioning uses ResizeObserver, which jsdom lacks.
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    // Radix schedules post-open updates via rAF; run them synchronously.
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    })
    vi.stubGlobal('cancelAnimationFrame', () => {})
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    vi.unstubAllGlobals()
    container.remove()
    document.body.replaceChildren()
  })

  const render = (item: ToolItem) =>
    act(() => {
      root.render(<TooltipProvider><ToolCard item={item} /></TooltipProvider>)
    })

  const badges = () => Array.from(container.querySelectorAll('span'))
    .filter(el => ['cached', 'batched', 'MCP'].includes(el.textContent ?? ''))

  it('shows the real file name (not the tool name) for batched sub-calls', () => {
    render(makeItem({
      toolName: 'read_file (batched)',
      args: '{"path":"/repo/frontend/src/components/chat/toolCards/ToolCard.tsx"}',
      parsedArgs: { path: '/repo/frontend/src/components/chat/toolCards/ToolCard.tsx' },
      result: 'file content',
    }))
    expect(container.textContent).toContain('Read: ')
    expect(container.textContent).toContain('ToolCard.tsx')
    // The bare tool name must no longer stand in for the file name.
    expect(container.textContent).not.toContain('Read: read_file')
    // The batched banner is rendered and non-shrinkable so it stays visible
    // next to a truncated title.
    const badge = badges().find(el => el.textContent === 'batched')
    expect(badge).toBeDefined()
    expect(badge!.className).toContain('shrink-0')
    expect(badge!.className).toContain('whitespace-nowrap')
  })

  it('shows the real file name and fragment window for cached reads with merged args', () => {
    render(makeItem({
      toolName: 'read_file (cached)',
      args: '{"path":"/repo/core/orchestrator.go","hash":"ab12","start_line":501,"num_lines":100}',
      parsedArgs: { path: '/repo/core/orchestrator.go', hash: 'ab12', start_line: 501, num_lines: 100 },
      result: '[Lines 501-600 of 2000 from cached read_file result | hash: ab12]\npackage main',
    }))
    // Title carries the file name + the fragment's start line, not the tool name.
    expect(container.textContent).toContain('orchestrator.go L501')
    expect(container.textContent).not.toContain('Read: read_file')
    // Cached banner + fragment range note, both non-shrinkable.
    const badge = badges().find(el => el.textContent === 'cached')
    expect(badge).toBeDefined()
    expect(badge!.className).toContain('shrink-0')
    const range = Array.from(container.querySelectorAll('span'))
      .find(el => (el.textContent ?? '').startsWith('fragment: lines'))
    expect(range).toBeDefined()
    expect(range!.className).toContain('shrink-0')
    expect(range!.className).toContain('whitespace-nowrap')
    expect(range!.textContent).toContain('lines 501–600 of 2000')
  })

  it('shows the URL for a cached web_fetch', () => {
    render(makeItem({
      toolName: 'web_fetch (cached)',
      args: '{"url":"https://example.com/docs/api"}',
      parsedArgs: { url: 'https://example.com/docs/api' },
      result: '<html>docs</html>',
    }))
    expect(container.textContent).toContain('Fetched: ')
    expect(container.textContent).toContain('https://example.com/docs/api')
    expect(container.textContent).not.toContain('Fetched: web_fetch')
    expect(badges().find(el => el.textContent === 'cached')).toBeDefined()
  })

  it('renders a file link for cached file tools when the merged args carry a path', () => {
    render(makeItem({
      toolName: 'read_file (cached)',
      args: '{"path":"/repo/main.go","hash":"cd34","start_line":10,"num_lines":5}',
      parsedArgs: { path: '/repo/main.go', hash: 'cd34', start_line: 10, num_lines: 5 },
      result: '[Lines 10-14 of 40 from cached read_file result | hash: cd34]\nfunc main() {}',
    }))
    const link = container.querySelector('span[role="button"]')
    expect(link).not.toBeNull()
    expect(link!.textContent).toContain('main.go L10')
  })

  it('degrades to the generic placeholder for pre-rewrite cached calls (hash-only args)', () => {
    render(makeItem({
      toolName: 'read_file (cached)',
      args: '{"hash":"ab12"}',
      parsedArgs: { hash: 'ab12' },
      result: '[Lines 1-5 of 10 from cached read_file result | hash: ab12]\ncontent',
    }))
    // Old sessions recorded before the backend rewrite keep hash-only args;
    // the card falls back to the extractor placeholder, still with its badge.
    expect(container.textContent).toContain('Read: file')
    expect(badges().find(el => el.textContent === 'cached')).toBeDefined()
    // No file link is possible without a path.
    expect(container.querySelector('span[role="button"]')).toBeNull()
  })

  it('keeps plain (unsuffixed) cards unchanged', () => {
    render(makeItem({
      toolName: 'read_file',
      args: '{"path":"/repo/plain.ts"}',
      parsedArgs: { path: '/repo/plain.ts' },
      result: 'content',
    }))
    expect(container.textContent).toContain('plain.ts')
    expect(badges()).toHaveLength(0)
  })
})
