import { expect } from 'vitest'

/**
 * Long-token assertions for PathWithBreaks: the token renders as ONE span
 * whose textContent is char-identical to the source (a <wbr> is an element,
 * not a character), with exactly one <wbr> per `/`/`\` separator, each
 * sitting immediately after its separator, and no injected zero-width chars.
 *
 * Asserts EVERY span whose textContent equals the token — not just the first
 * match — so a degraded copy cannot hide behind a healthy duplicate (a dialog
 * may render the same token more than once). Shared by the dialog tests that
 * wire PathWithBreaks into their copy. `scope` defaults to the whole document
 * body (Radix portals dialog content into body); pass a narrower ParentNode —
 * e.g. the dialog element — when several PathWithBreaks instances stay
 * mounted at the same time.
 */
export function assertTokenBreaks(token: string, scope: ParentNode = document.body): void {
  const text = scope.textContent ?? ''
  expect(text).toContain(token)
  expect(text).not.toContain('\u200b')
  expect(text).not.toContain('\u200c')
  expect(text).not.toContain('\u200d')
  expect(text).not.toContain('\ufeff')
  const spans = Array.from(scope.querySelectorAll('span')).filter((s) => s.textContent === token)
  expect(spans.length).toBeGreaterThan(0)
  const separatorCount = (token.match(/[/\\]/g) ?? []).length
  for (const span of spans) {
    const wbrs = span.querySelectorAll('wbr')
    expect(wbrs.length).toBe(separatorCount)
    for (const wbr of wbrs) {
      const prev = (wbr.previousSibling?.textContent ?? '').slice(-1)
      expect(prev === '/' || prev === '\\').toBe(true)
    }
  }
}

/**
 * Stronger copy-safety assertion for one rendered span: walks its DOM in
 * order, replaying the character stream a user would select/copy. Every text
 * node must match the source at its offset; every `<wbr>` must sit
 * immediately after a `/` or `\` — which keeps `textContent` char-identical
 * to the input path, since a `<wbr>` contributes no characters at all
 * (implying no injected zero-width characters either). Used by the unit tests
 * of PathWithBreaks and by dialog/menu tests that render it.
 */
export function assertCopySafe(path: string, span: HTMLElement): void {
  expect(span.textContent).toBe(path)
  let offset = 0
  let wbrCount = 0
  for (const node of Array.from(span.childNodes)) {
    if (node.nodeType === Node.TEXT_NODE) {
      const text = node.textContent ?? ''
      expect(path.startsWith(text, offset)).toBe(true)
      offset += text.length
    } else {
      expect(node.nodeType).toBe(Node.ELEMENT_NODE)
      expect((node as Element).tagName).toBe('WBR')
      wbrCount++
      expect(offset).toBeGreaterThan(0)
      expect(path[offset - 1]).toMatch(/[/\\]/)
    }
  }
  expect(offset).toBe(path.length)
  const separators = path.match(/[/\\]/g) ?? []
  expect(wbrCount).toBe(separators.length)
  expect(span.querySelectorAll('wbr')).toHaveLength(wbrCount)
}
