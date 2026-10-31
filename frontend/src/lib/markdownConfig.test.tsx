// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
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

// The viewer store is mocked so local-file link clicks assert exactly which
// path reaches the viewer; the workspace-root resolver is mocked to control
// the No-Project (null root) vs rooted scenarios.
const fileViewerMock = vi.hoisted(() => ({
  openFile: vi.fn(),
  openFileAtLine: vi.fn(),
}))
const chatRootMock = vi.hoisted(() => ({
  resolveChatWorkspaceRoot: vi.fn(),
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: { getState: () => fileViewerMock },
}))
vi.mock('@/lib/chatWorkspaceRoot', () => ({
  resolveChatWorkspaceRoot: chatRootMock.resolveChatWorkspaceRoot,
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
    // Ids stay raw (NOT clobber-prefixed): the explicit `[jump](#sub-section)`
    // anchor asserted below resolves against exactly this value.
    expect(container.querySelector('h2')?.id).toBe('sub-section')
  })

  it('still renders explicit [#anchor](#…) links as real anchors', async () => {
    await render('[jump](#sub-section)')
    const a = container.querySelector('a')
    expect(a?.getAttribute('href')).toBe('#sub-section')
    expect(a?.textContent).toBe('jump')
  })
})

describe('Markdown local-file links', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    fileViewerMock.openFile.mockClear()
    fileViewerMock.openFileAtLine.mockClear()
    chatRootMock.resolveChatWorkspaceRoot.mockReset()
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

  function render(content: string, workspaceRoot: string | null = null) {
    return act(async () => {
      root.render(<Markdown content={content} workspaceRoot={workspaceRoot} />)
      // Flush the async root resolution + click handler chain.
      await Promise.resolve()
      await Promise.resolve()
    })
  }

  const link = () => container.querySelector('span[role="link"]') as HTMLElement | null

  it('renders a drive-prefix markdown href as an inert anchor (stripped upstream)', async () => {
    await render('[doc](C:\\Doc\\f.md)')
    // react-markdown's default urlTransform clears hrefs whose scheme is not
    // in its safe list (`c` is not) — the href never reaches MarkdownLink's
    // local-file classification at all. Pre-existing sanitize boundary: the
    // link stays an inert <a>, never a viewer open and never an external
    // dispatch.
    const a = container.querySelector('p a')
    expect(a).toBeTruthy()
    expect(a!.getAttribute('href')).toBeNull()
    expect(link()).toBeNull()
    expect(fileViewerMock.openFile).not.toHaveBeenCalled()
  })

  it('opens a root-less POSIX absolute link unchanged', async () => {
    await render('[x](/abs/dir/a.ts)')
    await act(async () => {
      link()!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/abs/dir/a.ts')
  })

  it('keeps a root-less relative link inert (nothing to join)', async () => {
    await render('[x](rel/dir/a.ts)')
    await act(async () => {
      link()!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fileViewerMock.openFile).not.toHaveBeenCalled()
  })

  it('joins a relative link with the workspace root when one exists', async () => {
    await render('[x](src/a.ts)', '/repo')
    await act(async () => {
      link()!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/repo/src/a.ts')
  })

  it('never joins an absolute link onto the workspace root', async () => {
    await render('[x](/abs/dir/a.ts)', '/repo')
    await act(async () => {
      link()!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/abs/dir/a.ts')
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

describe('Markdown remote image gating', () => {
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
    document.body.replaceChildren()
  })

  function render(content: string) {
    return act(async () => {
      root.render(<Markdown content={content} />)
      await Promise.resolve()
      await Promise.resolve()
    })
  }

  it('renders a remote http(s) image as a blocked placeholder, never an auto-fetched <img>', async () => {
    await render('![tracking](https://evil.example/pixel.png?t=1)')
    // The webview must not issue a GET for the model-authored URL: no <img>
    // exists until the user explicitly loads it.
    expect(container.querySelector('img')).toBeNull()
    const gate = container.querySelector('[data-testid="markdown-image-blocked"]')
    expect(gate).toBeTruthy()
    expect(gate!.textContent).toContain('https://evil.example/pixel.png?t=1')
    expect(gate!.querySelector('[data-testid="markdown-image-load"]')).toBeTruthy()
  })

  it('loads the remote image only after the explicit user click', async () => {
    await render('![photo](https://images.example/photo.png)')
    const load = container.querySelector('[data-testid="markdown-image-load"]') as HTMLElement
    await act(async () => {
      load.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })
    const img = container.querySelector('img')
    expect(img).toBeTruthy()
    expect(img!.getAttribute('src')).toBe('https://images.example/photo.png')
    // The gate is gone once consented.
    expect(container.querySelector('[data-testid="markdown-image-blocked"]')).toBeNull()
  })

  it('resets the gate when the src changes', async () => {
    await render('![a](https://images.example/a.png)')
    await act(async () => {
      ;(container.querySelector('[data-testid="markdown-image-load"]') as HTMLElement).dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      )
      await Promise.resolve()
    })
    expect(container.querySelector('img')).toBeTruthy()
    // A follow-up message with a different src cannot ride the earlier
    // approval — the gate is closed again.
    await render('![b](https://images.example/b.png)')
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('[data-testid="markdown-image-blocked"]')).toBeTruthy()
  })

  it('does not gate local (relative) image paths', async () => {
    await render('![pic](docs/img.png)')
    // Local paths resolve through the workspace RPC — no remote fetch, no gate.
    expect(container.querySelector('img')).toBeTruthy()
    expect(container.querySelector('[data-testid="markdown-image-blocked"]')).toBeNull()
  })

  it('blocks protocol-relative srcs — the webview resolves them against the page scheme', async () => {
    await render('![x](//evil.example/pixel.png?t=1)')
    // `//host` has no scheme for the sanitizer and misses a literal https://
    // prefix check, yet the browser fetches it cross-origin — it must be gated.
    expect(container.querySelector('img')).toBeNull()
    const gate = container.querySelector('[data-testid="markdown-image-blocked"]')
    expect(gate).toBeTruthy()
    expect(gate!.textContent).toContain('//evil.example/pixel.png?t=1')
  })

  it('blocks single-slash scheme spellings the URL parser normalizes to https://', async () => {
    await render('![x](https:/evil.example/pixel.png?t=1)')
    // `https:/host` (one slash) passes the sanitizer's protocol allow-list and
    // the webview normalizes it to https://host — it must be gated too.
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('[data-testid="markdown-image-blocked"]')).toBeTruthy()
  })
})

