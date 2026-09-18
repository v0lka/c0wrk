// paperHtmlSanitize — the security boundary for rendering a paper's
// paper.html. A hostile document must lose every active construct (script,
// iframe, style, event handlers, dangerous URL schemes) while a LaTeXML-shaped
// document must keep its structure: section ids, MathML with alttext/display,
// figures with local image srcs, tables, and both anchors kinds.

import { beforeEach, describe, expect, it } from 'vitest'
import type { Element, Root, RootContent } from 'hast'
import {
  PAPER_HTML_MAX_BYTES,
  PAPER_HTML_TREE_CACHE_MAX,
  paperHtmlByteLength,
  paperHtmlCacheStats,
  resetPaperHtmlCache,
  sanitizePaperHtml,
} from './paperHtmlSanitize'

// --- tree helpers -------------------------------------------------------------

function collectElements(root: Root): Element[] {
  const out: Element[] = []
  const visit = (nodes: readonly RootContent[]) => {
    for (const node of nodes) {
      if (node.type === 'element') {
        out.push(node)
        visit(node.children)
      }
    }
  }
  visit(root.children)
  return out
}

function findFirst(root: Root, tagName: string): Element | undefined {
  return collectElements(root).find((el) => el.tagName === tagName)
}

function allText(root: Root): string {
  let text = ''
  const visit = (nodes: readonly RootContent[]) => {
    for (const node of nodes) {
      if (node.type === 'text') text += node.value
      else if (node.type === 'element') visit(node.children)
    }
  }
  visit(root.children)
  return text
}

// --- fixtures -------------------------------------------------------------------

const HOSTILE_HTML = `<!DOCTYPE html>
<html>
  <head>
    <title>Hostile paper</title>
    <meta charset="utf-8">
    <link rel="stylesheet" href="https://evil.example/remote.css">
    <style>body { background: url('https://evil.example/track.png'); }</style>
    <script>window.parent && window.parent.postMessage('pwned', '*')</script>
  </head>
  <body onload="alert('body')">
    <h1 id="top" class="ltx_title">Hostile</h1>
    <p onclick="stealCookies()" style="background:url('https://evil.example/pixel')">Paragraph text</p>
    <iframe src="https://evil.example/frame" width="600" height="400"></iframe>
    <script>document.location = 'https://evil.example'</script>
    <a href="javascript:alert(1)">javascript link</a>
    <a href="vbscript:msgbox(1)">vbscript link</a>
    <a href="data:text/html,<b>booby</b>">data link</a>
    <a href="https://ok.example/paper">ok link</a>
    <img src="https://evil.example/x.png" onerror="alert(2)">
    <form action="https://evil.example"><input type="text"><button>go</button></form>
    <svg onload="alert(3)"><circle r="1"></circle></svg>
  </body>
</html>`

const LATEXML_HTML = `<!DOCTYPE html>
<html>
<head><title>Demo paper</title></head>
<body>
<section id="S1" class="ltx_section">
  <h2 class="ltx_title ltx_title_section">1 Introduction</h2>
  <p class="ltx_p">We study <math alttext="E=mc^2" display="inline" class="ltx_Math"><mi>E</mi><mo>=</mo><msup><mi>c</mi><mn>2</mn></msup></math> in depth.</p>
  <figure id="fig1" class="ltx_figure">
    <img src="assets/fig1.png" alt="System overview" width="420" height="240" class="ltx_graphics"/>
    <figcaption class="ltx_caption">Figure 1: The system.</figcaption>
  </figure>
  <div id="eq1" class="ltx_equation ltx_equation_group">
    <math alttext="x=y^2" display="block"><mrow><mi>x</mi><mo>=</mo><msup><mi>y</mi><mn>2</mn></msup></mrow></math>
  </div>
  <table class="ltx_tabular">
    <thead><tr><th class="ltx_th">Column</th></tr></thead>
    <tbody><tr><td colspan="2" class="ltx_td">cell</td></tr></tbody>
  </table>
  <p>See <a href="#S2-methods" class="ltx_ref">Section 2</a> and <a href="https://arxiv.org/abs/2401.00001" class="ltx_ref">arXiv</a>.</p>
  <section id="S2-methods"><h2 class="ltx_title">2 Methods</h2><p class="ltx_p">Details.</p></section>
</section>
</body>
</html>`

beforeEach(() => {
  resetPaperHtmlCache()
})

// --- hostile input -------------------------------------------------------------

describe('sanitizePaperHtml: hostile document', () => {
  it('keeps zero script/iframe/style/svg/form elements', () => {
    const root = sanitizePaperHtml(HOSTILE_HTML)
    const tags = new Set(collectElements(root).map((el) => el.tagName))
    const banned = [
      'script',
      'iframe',
      'style',
      'svg',
      'circle',
      'form',
      'input',
      'button',
      'link',
      'meta',
      'title',
      'object',
      'embed',
      'frameset',
    ]
    for (const tag of banned) {
      expect(tags.has(tag), `tag <${tag}> survived sanitization`).toBe(false)
    }
  })

  it('keeps zero event-handler and style attributes', () => {
    const root = sanitizePaperHtml(HOSTILE_HTML)
    for (const el of collectElements(root)) {
      for (const key of Object.keys(el.properties)) {
        expect(
          key.toLowerCase().startsWith('on'),
          `event handler "${key}" survived on <${el.tagName}>`,
        ).toBe(false)
        expect(key, `style attribute survived on <${el.tagName}>`).not.toBe('style')
      }
    }
  })

  it('strips javascript:/vbscript:/data: hrefs but keeps https and fragment hrefs', () => {
    const root = sanitizePaperHtml(HOSTILE_HTML)
    const hrefs = collectElements(root)
      .filter((el) => el.tagName === 'a')
      .map((el) => el.properties.href)
    expect(hrefs).toEqual([undefined, undefined, undefined, 'https://ok.example/paper'])
    // The link text survives — only the dangerous destination is dropped.
    expect(allText(root)).toContain('javascript link')
  })

  it('never leaks <style> bodies, script source, or head residue as visible text', () => {
    const root = sanitizePaperHtml(HOSTILE_HTML)
    const text = allText(root)
    expect(text).not.toContain('url(')
    expect(text).not.toContain('postMessage')
    expect(text).not.toContain('Hostile paper') // <title> text lives in <head>
    expect(text).toContain('Paragraph text') // …while body content is kept
  })

  it('drops every ltx_* class (no remote stylesheet hooks survive)', () => {
    const root = sanitizePaperHtml(LATEXML_HTML)
    for (const el of collectElements(root)) {
      const className = el.properties.className
      if (className === undefined) continue
      const names = Array.isArray(className) ? className : [className]
      for (const name of names) {
        expect(String(name)).not.toMatch(/^ltx_/)
      }
    }
  })
})

// --- LaTeXML-shaped input --------------------------------------------------------

describe('sanitizePaperHtml: LaTeXML-shaped document', () => {
  it('keeps sections and heading ids un-prefixed (internal anchors must resolve)', () => {
    const root = sanitizePaperHtml(LATEXML_HTML)
    const section = findFirst(root, 'section')
    expect(section?.properties.id).toBe('S1')
    const nested = collectElements(root).filter((el) => el.tagName === 'section')
    expect(nested.map((el) => el.properties.id)).toContain('S2-methods')
    const h2 = findFirst(root, 'h2')
    expect(h2?.children[0]).toMatchObject({ type: 'text', value: '1 Introduction' })
  })

  it('keeps MathML with alttext and display attributes and its token tree', () => {
    const root = sanitizePaperHtml(LATEXML_HTML)
    const maths = collectElements(root).filter((el) => el.tagName === 'math')
    expect(maths).toHaveLength(2)
    const [inline, block] = maths
    expect(inline?.properties).toMatchObject({ alttext: 'E=mc^2', display: 'inline' })
    expect(block?.properties).toMatchObject({ alttext: 'x=y^2', display: 'block' })
    // Presentation tree survives: mi/mo/msup → mi/mn.
    expect(inline?.children.map((c) => (c.type === 'element' ? c.tagName : c.type))).toEqual([
      'mi',
      'mo',
      'msup',
    ])
    const msup = collectElements(root).find((el) => el.tagName === 'msup')
    expect(msup?.children.map((c) => (c.type === 'element' ? c.tagName : c.type))).toEqual([
      'mi',
      'mn',
    ])
  })

  it('keeps figure > img with the relative local src, alt text and dimensions', () => {
    const root = sanitizePaperHtml(LATEXML_HTML)
    const figure = findFirst(root, 'figure')
    expect(figure?.properties.id).toBe('fig1')
    const img = findFirst(root, 'img')
    expect(img?.properties).toMatchObject({
      src: 'assets/fig1.png',
      alt: 'System overview',
      width: 420,
      height: 240,
    })
    const figcaption = findFirst(root, 'figcaption')
    expect(allText({ type: 'root', children: figcaption?.children ?? [] })).toContain(
      'Figure 1: The system.',
    )
  })

  it('keeps table semantics (thead/th/td with colSpan)', () => {
    const root = sanitizePaperHtml(LATEXML_HTML)
    const th = findFirst(root, 'th')
    expect(allText({ type: 'root', children: th?.children ?? [] })).toBe('Column')
    const td = findFirst(root, 'td')
    // Type-agnostic on purpose: property-information's html schema decides
    // whether colSpan arrives as the raw string '2' or the coerced number 2,
    // and that detail shifted across its 7.x releases. What matters is that
    // the sanitizer preserves the colspan attribute at all.
    expect(String(td?.properties.colSpan)).toBe('2')
    // Ancestors rule: tr still lives inside table structure.
    const table = findFirst(root, 'table')
    expect(table).toBeDefined()
  })

  it('keeps internal #fragment and external https anchors', () => {
    const root = sanitizePaperHtml(LATEXML_HTML)
    const hrefs = collectElements(root)
      .filter((el) => el.tagName === 'a')
      .map((el) => el.properties.href)
    expect(hrefs).toEqual(['#S2-methods', 'https://arxiv.org/abs/2401.00001'])
  })

  it('handles body-less fragment input', () => {
    const root = sanitizePaperHtml('<p>just a fragment</p>')
    expect(allText(root)).toBe('just a fragment')
    expect(findFirst(root, 'p')).toBeDefined()
  })
})

// --- cache and size cap -----------------------------------------------------------

describe('paperHtmlSanitize: cache and size accounting', () => {
  it('caches the sanitized tree by content', () => {
    sanitizePaperHtml(LATEXML_HTML)
    sanitizePaperHtml(LATEXML_HTML)
    const stats = paperHtmlCacheStats()
    expect(stats).toMatchObject({ hits: 1, misses: 1, size: 1 })
  })

  it('evicts least-recently-used trees past the cap', () => {
    for (let i = 0; i <= PAPER_HTML_TREE_CACHE_MAX; i++) {
      sanitizePaperHtml(`<p>doc ${i}</p>`)
    }
    expect(paperHtmlCacheStats().size).toBeLessThanOrEqual(PAPER_HTML_TREE_CACHE_MAX)
  })

  it('counts UTF-8 bytes, not UTF-16 code units', () => {
    expect(paperHtmlByteLength('')).toBe(0)
    expect(paperHtmlByteLength('abc')).toBe(3)
    expect(paperHtmlByteLength('ü')).toBe(2)
    expect(paperHtmlByteLength('𝕏')).toBe(4)
    expect(PAPER_HTML_MAX_BYTES).toBe(8 * 1024 * 1024)
  })
})
