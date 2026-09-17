// @vitest-environment jsdom
//
// PaperHtmlView — the render layer over the sanitized paper.html tree. These
// tests cover the behaviors a static export cannot carry on its own: local
// image resolution to data URLs (with an honest placeholder on failure — and
// never a remote request for a missing local file), external links routed
// through openExternalURL, internal #fragment anchors scrolling in-document,
// the oversize cap, and the rendered DOM structure for a LaTeXML-shaped
// document (plus zero script/iframe survival end-to-end).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { readFileAsDataURLMock, openExternalURLMock } = vi.hoisted(() => ({
  readFileAsDataURLMock: vi.fn(),
  openExternalURLMock: vi.fn(),
}))

vi.mock('@/api/workspace', () => ({
  readFileAsDataURL: readFileAsDataURLMock,
}))

vi.mock('@/api/runtime', () => ({
  openExternalURL: openExternalURLMock,
}))

import { PaperHtmlView, type PaperHtmlViewProps } from './PaperHtmlView'
import { PAPER_HTML_MAX_BYTES, resetPaperHtmlCache } from '@/lib/paperHtmlSanitize'

// jsdom implements no scrollIntoView — polyfill with an observable mock.
const scrollIntoViewMock = vi.fn()

const PAPER_DIR = '/ws/.research/papers/demo'
const PAPER_HTML_PATH = `${PAPER_DIR}/paper.html`

const DOC_WITH_IMG = `<!DOCTYPE html><html><body>
<section id="S1"><h2>Intro</h2>
<figure id="fig1"><img src="assets/fig1.png" alt="Overview"/><figcaption>Figure 1</figcaption></figure>
</section></body></html>`

const DOC_WITH_LINKS = `<!DOCTYPE html><html><body>
<h2 id="sec-methods">Methods</h2>
<p>See <a href="https://arxiv.org/abs/2401.00001">the paper</a> and <a href="#sec-methods">the methods</a>.</p>
</body></html>`

const LATEXML_DOC = `<!DOCTYPE html><html><body>
<section id="S1"><h2>1 Introduction</h2>
<p>Inline <math alttext="E=mc^2" display="inline"><mi>E</mi><mo>=</mo><msup><mi>c</mi><mn>2</mn></msup></math>.</p>
<figure id="fig1"><img src="assets/fig1.png" alt="System overview"/><figcaption>Figure 1: The system.</figcaption></figure>
<div id="eq1"><math alttext="x=y" display="block"><mrow><mi>x</mi><mo>=</mo><mi>y</mi></mrow></math></div>
</section></body></html>`

const HOSTILE_DOC = `<!DOCTYPE html><html><body>
<p onclick="steal()">text</p>
<script>alert(1)</script><iframe src="https://evil.example"></iframe>
<style>body{background:url(https://evil.example/x.png)}</style>
<a href="javascript:alert(2)">bad</a>
</body></html>`

let root: Root | null = null
let container: HTMLDivElement | null = null

async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function mount(props: PaperHtmlViewProps): Promise<void> {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(<PaperHtmlView {...props} />)
  })
  await settle()
}

function click(node: Element): MouseEvent {
  const event = new MouseEvent('click', { bubbles: true, cancelable: true })
  act(() => {
    node.dispatchEvent(event)
  })
  return event
}

beforeEach(() => {
  readFileAsDataURLMock.mockReset()
  // Default: every candidate is missing — individual tests override this with
  // resolutions where the fixture image should be found.
  readFileAsDataURLMock.mockRejectedValue(new Error('no such file'))
  openExternalURLMock.mockReset()
  scrollIntoViewMock.mockReset()
  resetPaperHtmlCache()
  Element.prototype.scrollIntoView = scrollIntoViewMock as unknown as Element['scrollIntoView']
})

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  root = null
  container = null
})

describe('PaperHtmlView: local images', () => {
  it('resolves a relative src against the paper directory through readFileAsDataURL', async () => {
    readFileAsDataURLMock.mockImplementation(async (path: string) => {
      if (path === `${PAPER_DIR}/assets/fig1.png`) return 'data:image/png;base64,AAA'
      throw new Error(`not found: ${path}`)
    })
    await mount({ content: DOC_WITH_IMG, baseFilePath: PAPER_HTML_PATH })

    const img = document.querySelector('[data-testid="paper-html-image"]') as HTMLImageElement | null
    expect(img).not.toBeNull()
    expect(img?.getAttribute('src')).toBe('data:image/png;base64,AAA')
    expect(img?.getAttribute('alt')).toBe('Overview')
    expect(readFileAsDataURLMock).toHaveBeenCalledWith(`${PAPER_DIR}/assets/fig1.png`)
  })

  it('tries the workspace root next, then renders a placeholder — never a remote request', async () => {
    readFileAsDataURLMock.mockRejectedValue(new Error('missing'))
    await mount({
      content: DOC_WITH_IMG,
      baseFilePath: PAPER_HTML_PATH,
      workspaceRoot: '/ws',
    })

    // Both candidate bases were tried, in order (paper dir, then root).
    expect(readFileAsDataURLMock.mock.calls.map((call) => call[0])).toEqual([
      `${PAPER_DIR}/assets/fig1.png`,
      '/ws/assets/fig1.png',
    ])
    // A visible placeholder appears…
    expect(document.querySelector('[data-testid="paper-html-image-missing"]')).not.toBeNull()
    // …and the raw relative src is never handed to the webview as a URL.
    expect(document.querySelector('img[src="assets/fig1.png"]')).toBeNull()
  })

  it('renders the placeholder without any RPC when there is no base to resolve against', async () => {
    await mount({ content: DOC_WITH_IMG })
    expect(readFileAsDataURLMock).not.toHaveBeenCalled()
    expect(document.querySelector('[data-testid="paper-html-image-missing"]')).not.toBeNull()
  })
})

describe('PaperHtmlView: links', () => {
  it('routes external hrefs through openExternalURL and never navigates the webview', async () => {
    await mount({ content: DOC_WITH_LINKS })
    const external = document.querySelector('a[href="https://arxiv.org/abs/2401.00001"]')
    expect(external).not.toBeNull()

    const event = click(external!)
    expect(openExternalURLMock).toHaveBeenCalledWith('https://arxiv.org/abs/2401.00001')
    expect(event.defaultPrevented).toBe(true)
  })

  it('scrolls in-document for #fragment hrefs', async () => {
    await mount({ content: DOC_WITH_LINKS })
    const target = document.getElementById('sec-methods')
    expect(target).not.toBeNull()
    const internal = document.querySelector('a[href="#sec-methods"]')
    expect(internal).not.toBeNull()

    click(internal!)
    expect(scrollIntoViewMock).toHaveBeenCalledTimes(1)
    expect(scrollIntoViewMock.mock.instances[0]).toBe(target)
    expect(openExternalURLMock).not.toHaveBeenCalled()
  })
})

describe('PaperHtmlView: structure and safety', () => {
  it('renders a LaTeXML-shaped document: sections, MathML, figures, captions', async () => {
    await mount({ content: LATEXML_DOC, baseFilePath: PAPER_HTML_PATH })

    expect(document.querySelector('section#S1')).not.toBeNull()
    const maths = document.querySelectorAll('math')
    expect(maths).toHaveLength(2)
    expect(maths[0]?.getAttribute('alttext')).toBe('E=mc^2')
    expect(maths[0]?.getAttribute('display')).toBe('inline')
    expect(maths[1]?.getAttribute('display')).toBe('block')
    // Presentation tree depth survives: msup inside inline math.
    expect(maths[0]?.querySelectorAll('msup, mi, mo')).not.toBeNull()
    expect(document.querySelector('figure#fig1 figcaption')?.textContent).toContain(
      'Figure 1: The system.',
    )
  })

  it('renders MathML elements in the MathML namespace', async () => {
    await mount({ content: LATEXML_DOC })
    const math = document.querySelector('math')
    expect(math?.namespaceURI).toBe('http://www.w3.org/1998/Math/MathML')
  })

  it('leaves zero script/iframe/style elements and event handlers in the DOM', async () => {
    await mount({ content: HOSTILE_DOC })
    expect(document.querySelectorAll('script, iframe, style, svg')).toHaveLength(0)
    expect(document.querySelectorAll('[onclick]')).toHaveLength(0)
    const badLink = Array.from(document.querySelectorAll('a')).find((a) =>
      a.textContent?.includes('bad'),
    )
    expect(badLink?.hasAttribute('href')).toBe(false)
    // The surviving paragraph proves content around stripped nodes is kept.
    expect(document.querySelector('p')?.textContent).toContain('text')
  })
})

describe('PaperHtmlView: oversize cap', () => {
  it('renders an explicit message instead of the tree for oversized content', async () => {
    const oversized = `<p>${'x'.repeat(PAPER_HTML_MAX_BYTES)}</p>`
    await mount({ content: oversized })

    expect(document.querySelector('[data-testid="paper-html-oversize"]')).not.toBeNull()
    expect(document.querySelector('[data-testid="paper-html-view"]')).toBeNull()
    expect(document.body.textContent).toContain('too large')
  })

  it('renders normally just under the cap boundary', async () => {
    const justUnder = `<p>${'x'.repeat(PAPER_HTML_MAX_BYTES - 100)}</p>`
    await mount({ content: justUnder })
    expect(document.querySelector('[data-testid="paper-html-view"]')).not.toBeNull()
  })
})
