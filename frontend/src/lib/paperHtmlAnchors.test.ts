// Tests for lib/paperHtmlAnchors.ts — E1 anchor resolution against the
// rendered paper.html. The point of these is the same DEGRADATION contract as
// paperAnchors: an anchor that cannot be located confidently must resolve to
// null rather than jumping to a plausible-looking wrong element, and the
// resolver must be PURE — hast trees and element descriptors, never DOM nodes.
//
// The fixtures mirror real LaTeXML/ar5iv exports: sections `S3`, subsections
// `S3.SS2`, figures `S3.F2`/`fig3`, tables `S5.T1`, equations `S4.E5`, and
// captions inside `<figcaption>` starting with "Figure 2: ".

import { describe, it, expect, beforeEach } from 'vitest'
import type { Element, RootContent } from 'hast'
import type { PaperAnchor } from '@/api/papers'
import { MIN_TEXT_NEEDLE } from './paperAnchors'
import { PAPER_HTML_MAX_BYTES, resetPaperHtmlCache, sanitizePaperHtml } from './paperHtmlSanitize'
import { resolveHtmlAnchor } from './paperHtmlAnchors'

/** A LaTeXML-shaped document: one id-less figure (exercises the caption-text
 *  fallback), an id'd figure, a section-scoped table id, and a numbered
 *  display equation. The body mention of Figure 2 PRECEDES the caption —
 *  caption-start priority must still win. */
const DOC = `<!doctype html>
<html>
<head><title>Demo Paper</title></head>
<body>
<h1>Demo Paper</h1>
<section id="S1"><h2>1 Introduction</h2><p>We study injection attacks on agents.</p></section>
<p>Figure 2 shows the overall mechanism end to end.</p>
<section id="S3"><h2>3 Method</h2>
  <section id="S3.SS1"><h3>3.1 Setup</h3><p>The setup is careful and explicit.</p></section>
  <section id="S3.SS2"><h3>3.2 Attention</h3><p>Attention is all we need here.</p></section>
</section>
<figure><figcaption><b>Figure 2: </b>The attention mechanism.</figcaption></figure>
<figure id="fig3"><figcaption><b>Figure 3: </b>Ablation over heads.</figcaption></figure>
<figure id="S5.T1"><figcaption><b>Table 1: </b>Results overview.</figcaption><table><tbody><tr><td>42</td></tr></tbody></table></figure>
<table id="S4.E5"><tbody><tr><td><math display="block" alttext="x=y"><mi>x</mi></math></td><td>(5)</td></tr></tbody></table>
<p>We follow prompt 7 in all runs.</p>
<p>Prompt 7: the adversarial recipe.</p>
<p>The quick brown fox jumps over the lazy dog.</p>
</body>
</html>`

/** A document with NO ids at all — every resolution must go through the
 *  conservative caption/heading text fallbacks. */
const NO_ID_DOC = `<!doctype html>
<html><body>
<h2>3 Method</h2>
<p>As reported in Table 4 the accuracy drops.</p>
<table><caption>Table 4: Accuracy under attack.</caption><tbody><tr><td>17%</td></tr></tbody></table>
</body></html>`

function anchor(ref: string, label = ''): PaperAnchor {
  return { label, ref, note: '' }
}

/** Resolve `hit.path` against the sanitized tree and return the target
 *  element — proving the descriptor contract points at a real element. */
function elementAt(html: string, path: number[]): Element {
  expect(path.length).toBeGreaterThan(0)
  const root = sanitizePaperHtml(html)
  let nodes: readonly RootContent[] = root.children
  let el: Element | undefined
  for (const step of path) {
    const elements = nodes.filter((n): n is Element => n.type === 'element')
    el = elements[step]!
    expect(el).toBeDefined()
    nodes = el.children
  }
  return el!
}

describe('paperHtmlAnchors: LaTeXML id resolution', () => {
  beforeEach(() => resetPaperHtmlCache())

  it('resolves §3 to the S3 section element', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('§3'))
    expect(hit).not.toBeNull()
    expect(hit!.id).toBe('S3')
    expect(hit!.kind).toBe('section')
    expect(hit!.needle).toBe('§3')
    expect(elementAt(DOC, hit!.path).tagName).toBe('section')
  })

  it('resolves §3.2 to the S3.SS2 subsection (ar5iv convention)', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('§3.2'))
    expect(hit!.id).toBe('S3.SS2')
    expect(hit!.kind).toBe('section')
  })

  it('resolves §3.1 to the S3.SS1 subsection', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('§3.1'))
    expect(hit!.id).toBe('S3.SS1')
  })

  it('resolves Fig. 2 through the caption element (caption-start priority over an earlier body mention)', () => {
    // The figure carries NO id, so resolution must fall back to caption text —
    // and the caption must win over the earlier paragraph that also starts
    // with "Figure 2".
    const hit = resolveHtmlAnchor(DOC, anchor('Fig. 2'))
    expect(hit).not.toBeNull()
    expect(hit!.id).toBeNull()
    expect(hit!.kind).toBe('figure')
    expect(elementAt(DOC, hit!.path).tagName).toBe('figcaption')
  })

  it('resolves Fig. 3 to the exact fig3 id', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('Figure 3'))
    expect(hit!.id).toBe('fig3')
    expect(hit!.kind).toBe('figure')
  })

  it('resolves Table 1 to the section-scoped S5.T1 id', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('Table 1'))
    expect(hit!.id).toBe('S5.T1')
    expect(hit!.kind).toBe('table')
  })

  it('resolves Eq. (5) to the section-scoped S4.E5 equation id', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('Eq. (5)'))
    expect(hit!.id).toBe('S4.E5')
    expect(hit!.kind).toBe('equation')
  })

  it('resolves a subsection heading by text when no id matches', () => {
    const hit = resolveHtmlAnchor(NO_ID_DOC, anchor('§3'))
    expect(hit).not.toBeNull()
    expect(hit!.id).toBeNull()
    expect(elementAt(NO_ID_DOC, hit!.path).tagName).toBe('h2')
  })

  it('resolves a table by caption text in an id-less document (caption priority over an earlier body mention)', () => {
    // The paragraph "As reported in Table 4…" precedes the caption in
    // document order — the caption element must still be the target.
    const hit = resolveHtmlAnchor(NO_ID_DOC, anchor('Table 4'))
    expect(hit).not.toBeNull()
    expect(hit!.kind).toBe('table')
    expect(elementAt(NO_ID_DOC, hit!.path).tagName).toBe('caption')
  })
})

describe('paperHtmlAnchors: verbatim quotes and the label fallback', () => {
  beforeEach(() => resetPaperHtmlCache())

  it('resolves a verbatim quote to the containing paragraph', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('the quick brown fox jumps over the lazy dog'))
    expect(hit).not.toBeNull()
    expect(hit!.kind).toBe('text')
    expect(hit!.id).toBeNull()
    const el = elementAt(DOC, hit!.path)
    expect(el.tagName).toBe('p')
  })

  it('resolves a quote that starts a block via the starts-with pass', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('We study injection attacks'))
    expect(hit).not.toBeNull()
    expect(elementAt(DOC, hit!.path).tagName).toBe('p')
  })

  it('refuses a quote shorter than MIN_TEXT_NEEDLE', () => {
    expect(MIN_TEXT_NEEDLE).toBeGreaterThan(1)
    expect(resolveHtmlAnchor(DOC, anchor('fox'))).toBeNull()
  })

  it('label fallback hits a structural target (a block starting with the label + separator)', () => {
    const hit = resolveHtmlAnchor(DOC, anchor('', 'prompt 7'))
    expect(hit).not.toBeNull()
    expect(hit!.kind).toBe('text')
    expect(elementAt(DOC, hit!.path).tagName).toBe('p')
  })

  it('label fallback never accepts a bare contains-match in body prose', () => {
    // "prompt 8" only occurs mid-sentence ("We follow prompt 8…") — the label
    // fallback must refuse it (no structural target).
    const doc = DOC.replace('We follow prompt 7 in all runs.', 'We follow prompt 8 in all runs.')
    expect(resolveHtmlAnchor(doc, anchor('', 'prompt 8'))).toBeNull()
  })
})

describe('paperHtmlAnchors: conservatism and degradation', () => {
  beforeEach(() => resetPaperHtmlCache())

  it('refuses a bare number', () => {
    expect(resolveHtmlAnchor(DOC, anchor('42'))).toBeNull()
  })

  it('refuses a page pointer', () => {
    expect(resolveHtmlAnchor(DOC, anchor('p. 7'))).toBeNull()
    expect(resolveHtmlAnchor(DOC, anchor('page 12'))).toBeNull()
  })

  it('refuses a structural keyword without a number', () => {
    expect(resolveHtmlAnchor(DOC, anchor('Figure'))).toBeNull()
  })

  it('returns null for an anchor no document part matches', () => {
    expect(resolveHtmlAnchor(DOC, anchor('§9'))).toBeNull()
    expect(resolveHtmlAnchor(DOC, anchor('Fig. 9'))).toBeNull()
  })

  it('returns null for an empty or blank document', () => {
    expect(resolveHtmlAnchor('', anchor('§3'))).toBeNull()
    expect(resolveHtmlAnchor('   \n  ', anchor('§3'))).toBeNull()
  })

  it('returns null for an over-cap document (unrenderable, so unresolved)', () => {
    const oversize = `<html><body><section id="S3"><h2>3 Method</h2>${'x'.repeat(
      PAPER_HTML_MAX_BYTES,
    )}</section></body></html>`
    expect(resolveHtmlAnchor(oversize, anchor('§3'))).toBeNull()
  })

  it('resolves hostile markup as its sanitized self (script content never matches)', () => {
    const hostile = `<html><body><script>§3 Figure 2 Table 1</script><section id="S3"><h2>3 Method</h2></section></body></html>`
    const hit = resolveHtmlAnchor(hostile, anchor('§3'))
    expect(hit!.id).toBe('S3')
    expect(resolveHtmlAnchor(hostile, anchor('Figure 2'))).toBeNull()
  })

  it('tries ref candidates in order (verbatim first, then the stripped form)', () => {
    // The verbatim "§3 (method)" classifies through its § prefix, but the
    // parenthetical-stripped candidate also resolves; both must hit S3.
    expect(resolveHtmlAnchor(DOC, anchor('§3 (method)'))!.id).toBe('S3')
  })
})
