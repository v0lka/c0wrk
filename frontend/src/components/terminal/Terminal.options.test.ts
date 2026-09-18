// @vitest-environment jsdom
// Pins @xterm/xterm v6's public options-setter contract.
//
// Background: since @xterm/xterm v6 the public `options` setter MERGES the
// assigned object's keys and validates each key against the constructor-only
// list (`cols`, `rows`, ...). Assigning a spread of the current options —
// `{ ...term.options, theme }` — drags the readonly keys through the setter
// and throws. Terminal.tsx used to do exactly that in its live-theme effect;
// because the effect runs on mount, the very first activation of the
// terminal pane crashed the whole input shell ("Input error" ErrorBoundary
// fallback), and the persisted `mode: 'terminal'` (zustand
// `c0wrk-input-mode`) reproduced the crash on every app start.
//
// Scope: this file pins the DEPENDENCY's merge/validate contract — the
// behaviour Terminal.tsx's `term.options.theme = palette` assignment relies
// on. It does NOT import or render Terminal.tsx: jsdom cannot host the
// renderer (no canvas), which is also why TerminalPanel.test.tsx mocks
// @xterm/xterm entirely for the component-wiring tests. A regression inside
// the component itself is therefore not caught here; what is caught is the
// dependency changing the contract underneath it (e.g. a partial `theme`
// assignment starting to throw or to reset sibling options).
//
// jsdom cannot provide a canvas 2D/WebGL context, so renderer-dependent
// calls (open() internals, fit()) are NOT exercised here — only the options
// setter, which is pure option bookkeeping and needs no renderer.

import { describe, it, expect, afterEach } from 'vitest'
import { Terminal as XTerm } from '@xterm/xterm'

let container: HTMLDivElement | null = null

afterEach(() => {
  container?.remove()
  container = null
})

function makeContainer(): HTMLDivElement {
  container = document.createElement('div')
  document.body.appendChild(container)
  return container
}

describe('xterm v6 public options setter', () => {
  it('accepts a partial assignment of mutable options (theme)', () => {
    const term = new XTerm({ scrollback: 100 })
    // Assigning ONLY the keys being changed must not throw — this is the
    // pattern Terminal.tsx relies on to apply palette changes live.
    expect(() => {
      term.options = { theme: { background: '#282c34', foreground: '#abb2bf' } }
    }).not.toThrow()
    expect(term.options.theme?.background).toBe('#282c34')
    term.dispose()
  })

  it('merges the assignment without resetting other options', () => {
    const term = new XTerm({ scrollback: 100, fontSize: 10 })
    term.options = { theme: { cursor: '#61afef' } }
    // Untouched keys keep their values: the setter merges, not replaces.
    expect(term.options.fontSize).toBe(10)
    expect(term.options.scrollback).toBe(100)
    term.dispose()
  })

  it('still rejects constructor-only options — proves the real validation is active', () => {
    const term = new XTerm({ scrollback: 100 })
    // Negative control: if @xterm/xterm ever stops validating readonly keys,
    // this file is testing a mock/stub and the guard above loses its teeth.
    // Matched generically (/constructor/i) on purpose: the exact error
    // wording is @xterm/xterm's own and may change across releases.
    // (cols is constructor-only in the typings — ITerminalInitOnlyOptions —
    // hence the cast; the runtime setter is what we are exercising.)
    expect(() => {
      term.options = { cols: 80 } as unknown as import('@xterm/xterm').ITerminalOptions
    }).toThrow(/constructor/i)
    term.dispose()
  })

  it('a terminal whose container is never opened still accepts theme assignments', () => {
    // Terminal.tsx assigns the theme in an effect that may run before/after
    // open() depending on render timing; the setter itself must be safe on a
    // not-yet-opened terminal (no renderer involved).
    const term = new XTerm({})
    expect(() => {
      term.options = { theme: { background: '#101010' } }
    }).not.toThrow()
    void makeContainer() // keep afterEach cleanup symmetric
    term.dispose()
  })
})
