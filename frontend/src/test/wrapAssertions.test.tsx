// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { assertTokenBreaks } from './wrapAssertions'

// Regression for the strengthened helper: it must assert EVERY span whose
// textContent equals the token, so a degraded duplicate (a plain span without
// separator <wbr>s) cannot hide behind a healthy PathWithBreaks copy.
describe('assertTokenBreaks', () => {
  let container: HTMLElement

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
  })

  afterEach(() => {
    document.body.replaceChildren()
  })

  it('passes when every matching span breaks at separators', () => {
    container.innerHTML =
      '<div><span>/<wbr>a/<wbr>b/<wbr>c</span><span>/<wbr>a/<wbr>b/<wbr>c</span></div>'
    expect(() => assertTokenBreaks('/a/b/c', container)).not.toThrow()
  })

  it('fails when any matching span lacks the separator breaks', () => {
    container.innerHTML = '<div><span>/<wbr>a/<wbr>b/<wbr>c</span><span>/a/b/c</span></div>'
    expect(() => assertTokenBreaks('/a/b/c', container)).toThrow()
  })
})
