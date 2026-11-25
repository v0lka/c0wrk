import { expect } from 'vitest'

/**
 * Long-token assertions for PathWithBreaks: the token renders as ONE span
 * whose textContent is char-identical to the source (a <wbr> is an element,
 * not a character), with exactly one <wbr> per `/`/`\` separator, each
 * sitting immediately after its separator, and no injected zero-width chars.
 *
 * Shared by the dialog tests that wire PathWithBreaks into their copy.
 * `scope` defaults to the whole document body (Radix portals dialog content
 * into body); pass a narrower ParentNode — e.g. the dialog element — when
 * several PathWithBreaks instances stay mounted at the same time.
 */
export function assertTokenBreaks(token: string, scope: ParentNode = document.body): void {
  const text = scope.textContent ?? ''
  expect(text).toContain(token)
  expect(text).not.toContain('\u200b')
  expect(text).not.toContain('\u200c')
  expect(text).not.toContain('\u200d')
  expect(text).not.toContain('\ufeff')
  const span = Array.from(scope.querySelectorAll('span')).find((s) => s.textContent === token)
  expect(span).not.toBeUndefined()
  const wbrs = span!.querySelectorAll('wbr')
  expect(wbrs.length).toBe((token.match(/[/\\]/g) ?? []).length)
  for (const wbr of wbrs) {
    const prev = (wbr.previousSibling?.textContent ?? '').slice(-1)
    expect(prev === '/' || prev === '\\').toBe(true)
  }
}
