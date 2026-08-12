// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  ;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true
})

// --- Mock themeStore (avoids persisted localStorage in tests) ---
vi.mock('@/stores/themeStore', () => ({
  useThemeStore: (selector: (s: { theme: 'dark' | 'light' }) => unknown) =>
    selector({ theme: 'dark' }),
}))

// --- Mock the dynamic `import('mermaid')` in MermaidBlock ---
const renderMock = vi.fn()
const initializeMock = vi.fn()
vi.mock('mermaid', () => ({
  default: {
    initialize: initializeMock,
    render: renderMock,
  },
}))

import { MermaidBlock } from './MermaidBlock'

const SVG = '<svg id="d" xmlns="http://www.w3.org/2000/svg" width="100" height="60"><rect width="100" height="60"/></svg>'

describe('MermaidBlock', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    renderMock.mockReset()
    initializeMock.mockReset()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    // jsdom has no layout — give the canvas a clientWidth so fit() is meaningful.
    Object.defineProperty(container, 'clientWidth', { configurable: true, value: 500 })
  })

  function render(code: string) {
    return act(async () => {
      root.render(<MermaidBlock code={code} />)
      // flush the async mermaid render promise
      await Promise.resolve()
      await Promise.resolve()
    })
  }

  it('renders the diagram SVG inside the canvas', async () => {
    renderMock.mockResolvedValue({ svg: SVG })
    await render('graph TD\nA-->B')
    const canvas = container.querySelector('.mermaid-canvas')!
    expect(canvas).toBeTruthy()
    expect(canvas.querySelector('svg')).toBeTruthy()
  })

  it('shows zoom controls and a percentage label', async () => {
    renderMock.mockResolvedValue({ svg: SVG })
    await render('graph TD\nA-->B')
    const buttons = container.querySelectorAll('button[title]')
    const titles = Array.from(buttons).map((b) => b.getAttribute('title'))
    expect(titles).toEqual(expect.arrayContaining(['Zoom out', 'Zoom in', 'Reset view']))
  })

  it('zooms in and out via the toolbar buttons', async () => {
    renderMock.mockResolvedValue({ svg: SVG })
    await render('graph TD\nA-->B')
    // Select the percentage label by its stable aria-live contract, not by
    // incidental DOM order relative to the canvas.
    const label = () => container.querySelector('[aria-live="polite"]')

    const zoomIn = container.querySelector('button[title="Zoom in"]') as HTMLButtonElement
    const zoomOut = container.querySelector('button[title="Zoom out"]') as HTMLButtonElement
    expect(zoomIn && zoomOut).toBeTruthy()

    const before = label()?.textContent
    await act(async () => {
      zoomIn.click()
    })
    const after = label()?.textContent
    expect(before).toBe('100%')
    expect(after).not.toBe('100%')

    // Zoom back out — percentage should change again.
    await act(async () => {
      zoomOut.click()
    })
    expect(label()?.textContent).not.toBe(after)
  })

  it('initializes mermaid with securityLevel: "strict" (XSS guard)', async () => {
    renderMock.mockResolvedValue({ svg: SVG })
    await render('graph TD\nA-->B')
    expect(initializeMock).toHaveBeenCalledTimes(1)
    const call = initializeMock.mock.calls[0]
    const config = (call?.[0] ?? {}) as { securityLevel?: string }
    // The SVG output is injected via dangerouslySetInnerHTML from
    // attacker-controllable input; securityLevel must stay 'strict' so
    // mermaid strips scripts. A regression to 'loose' is a security bug.
    expect(config.securityLevel).toBe('strict')
  })

  it('disables flowchart htmlLabels so labels survive DOMPurify', async () => {
    renderMock.mockResolvedValue({ svg: SVG })
    await render('graph TD\nA-->B')
    const call = initializeMock.mock.calls[0]
    const config = (call?.[0] ?? {}) as { flowchart?: { htmlLabels?: boolean } }
    // Labels must render as SVG <text>, not HTML inside <foreignObject>: the
    // DOMPurify svg-only sink strips <foreignObject> + its HTML children, so
    // the default htmlLabels:true would delete every label (text invisible).
    expect(config.flowchart?.htmlLabels).toBe(false)
  })

  it('sanitizes the mermaid SVG output before injecting it', async () => {
    // A payload that would be dangerous if injected unsanitized. mermaid's
    // strict mode would strip it, but we assert the DOMPurify defense layer
    // also removes it, guarding against upstream regressions.
    const payload = '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><rect/></svg>'
    renderMock.mockResolvedValue({ svg: payload })
    await render('graph TD\nA-->B')
    // No <script> element should survive into the rendered DOM.
    expect(container.querySelector('script')).toBeNull()
    // The sanitized SVG body should still be present.
    expect(container.querySelector('.mermaid-canvas svg')).toBeTruthy()
  })

  it('renders an error block when mermaid.render fails', async () => {
    renderMock.mockRejectedValue(new Error('syntax error'))
    await render('not valid mermaid')
    expect(container.textContent).toContain('error')
    expect(container.querySelector('.mermaid-canvas')).toBeNull()
    // The raw source is surfaced so the user can see what failed.
    expect(container.textContent).toContain('not valid mermaid')
  })
})
