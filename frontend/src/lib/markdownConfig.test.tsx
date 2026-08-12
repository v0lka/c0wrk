// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  ;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true
})

// Avoid persisted localStorage stores during Markdown rendering.
vi.mock('@/stores/themeStore', () => ({
  useThemeStore: (selector: (s: { theme: 'dark' | 'light' }) => unknown) =>
    selector({ theme: 'dark' }),
}))

// Mermaid is code-split in MermaidBlock — mock it so the wiring test stays
// focused on block routing, not diagram rendering.
vi.mock('mermaid', () => ({
  default: {
    initialize: vi.fn(),
    render: vi.fn().mockResolvedValue({ svg: '<svg id="d" width="10" height="10"/>' }),
  },
}))

import { Markdown } from './markdownConfig'

describe('Markdown mermaid block wiring', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    Object.defineProperty(container, 'clientWidth', { configurable: true, value: 500 })
  })

  function render(content: string) {
    return act(async () => {
      root.render(<Markdown content={content} />)
      // flush the async mermaid render promise
      await Promise.resolve()
      await Promise.resolve()
    })
  }

  it('renders a fenced ```mermaid block as an interactive MermaidBlock', async () => {
    await render('```mermaid\ngraph TD\nA-->B\n```')
    const canvas = container.querySelector('.mermaid-canvas')
    expect(canvas).toBeTruthy()
    expect(canvas?.querySelector('svg')).toBeTruthy()
  })

  it('leaves normal fenced code blocks as plain <pre><code>', async () => {
    await render('```go\nfmt.Println("hi")\n```')
    const pre = container.querySelector('pre')
    expect(pre).toBeTruthy()
    // No mermaid canvas should be rendered for non-mermaid blocks.
    expect(container.querySelector('.mermaid-canvas')).toBeNull()
    expect(pre?.querySelector('code')?.className).toContain('language-go')
  })
})
