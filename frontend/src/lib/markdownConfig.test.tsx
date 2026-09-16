// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Avoid persisted localStorage stores during Markdown rendering.
vi.mock('@/stores/themeStore', () => ({
  useThemeStore: (selector: (s: { themeId: string; customThemes: { id: string; name: string; type: string }[] }) => unknown) =>
    selector({ themeId: 'default-dark', customThemes: [] }),
  selectActiveThemeType: (s: { themeId: string; customThemes: { id: string; name: string; type: string }[] }) =>
    s.themeId === 'default-light' ? 'light' : 'dark',
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
import { markdownCacheStats, resetMarkdownCache } from './markdownCache'

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

describe('Markdown heading anchors', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  function render(content: string) {
    return act(async () => {
      root.render(<Markdown content={content} />)
    })
  }

  it('does not wrap headings in anchor links (dead self-referencing affordance)', async () => {
    await render('# Title\n\n## Sub Section\n')
    const h1 = container.querySelector('h1')
    const h2 = container.querySelector('h2')
    expect(h1?.textContent).toBe('Title')
    expect(h2?.textContent).toBe('Sub Section')
    // No autolink wrapper: a heading anchor points at the heading itself,
    // so the pointer cursor / underline promised a click that did nothing.
    expect(h1?.querySelector('a')).toBeNull()
    expect(h2?.querySelector('a')).toBeNull()
  })

  it('keeps rehype-slug ids so explicit #anchor links have targets', async () => {
    await render('## Sub Section\n')
    // rehype-sanitize's defaultSchema prefixes ids with 'user-content-' to
    // prevent DOM clobbering — assert the exact rendered value.
    expect(container.querySelector('h2')?.id).toBe('user-content-sub-section')
  })

  it('still renders explicit [#anchor](#…) links as real anchors', async () => {
    await render('[jump](#sub-section)')
    const a = container.querySelector('a')
    expect(a?.getAttribute('href')).toBe('#sub-section')
    expect(a?.textContent).toBe('jump')
  })
})

describe('Markdown parse cache', () => {
  beforeEach(() => {
    document.body.replaceChildren()
    resetMarkdownCache()
  })

  // Mount a fresh Markdown (a fresh mount cannot be short-circuited by memo, so
  // it exercises the cache path exactly as a re-render of an existing message
  // would).
  function renderOnce(content: string): { container: HTMLElement; unmount: () => void } {
    const div = document.createElement('div')
    document.body.appendChild(div)
    const root = createRoot(div)
    act(() => {
      root.render(<Markdown content={content} />)
    })
    return { container: div, unmount: () => act(() => root.unmount()) }
  }

  it('parses each distinct content once and serves repeats from the cache', () => {
    const first = renderOnce('# Cache me')
    // The cached node still resolves to correct DOM.
    expect(first.container.querySelector('h1')?.textContent).toBe('Cache me')
    first.unmount()
    expect(markdownCacheStats()).toMatchObject({ misses: 1, hits: 0 })

    const second = renderOnce('# Cache me')
    expect(second.container.querySelector('h1')?.textContent).toBe('Cache me')
    second.unmount()
    // The repeat re-used the parsed node — no second parse.
    expect(markdownCacheStats()).toMatchObject({ misses: 1, hits: 1 })

    const third = renderOnce('# Different')
    third.unmount()
    expect(markdownCacheStats().misses).toBe(2)
  })

  it('bounds the cache with LRU eviction', () => {
    for (let i = 0; i < 205; i++) {
      renderOnce(`# doc ${i}`).unmount()
    }
    const stats = markdownCacheStats()
    expect(stats.misses).toBe(205)
    expect(stats.size).toBeLessThanOrEqual(200)
  })
})

