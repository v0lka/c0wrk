// @vitest-environment jsdom
// Regression guard for the xterm v6 readonly-options crash.
//
// Background: since @xterm/xterm v6 the public `options` setter MERGES the
// assigned object's keys and validates each key against the constructor-only
// list (`cols`, `rows`, ...). Assigning a spread of the current options —
// `{ ...term.options, theme }` — drags the readonly keys through the setter
// and throws `Option "cols" can only be set in the constructor`. Terminal.tsx
// did exactly that in its live-theme effect; because the effect runs on
// mount, the very first activation of the terminal pane crashed the whole
// input shell ("Input error" ErrorBoundary fallback), and the persisted
// `mode: 'terminal'` (zustand `c0wrk-input-mode`) reproduced the crash on
// every app start.
//
// The other terminal tests (TerminalPanel.test.tsx) mock @xterm/xterm
// entirely, which is why this crash class was invisible in CI. This file
// imports the REAL @xterm/xterm — no mock — so the options-setter contract
// is exercised against the shipped package.
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
    // pattern Terminal.tsx uses to apply palette changes live.
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
    // (cols is constructor-only in the typings — ITerminalInitOnlyOptions —
    // hence the cast; the runtime setter is what we are exercising.)
    expect(() => {
      term.options = { cols: 80 } as unknown as import('@xterm/xterm').ITerminalOptions
    }).toThrow(/can only be set in the constructor/)
    term.dispose()
  })

  it('rejects spreading the current options back (the original crash)', () => {
    const term = new XTerm({ scrollback: 100 })
    // This is the EXACT pattern that crashed the input shell: the getter
    // exposes readonly keys (cols/rows), and feeding them back through the
    // setter throws. Kept as documentation; Terminal.tsx must never do this.
    expect(() => {
      term.options = { ...term.options, theme: { background: '#000000' } }
    }).toThrow(/can only be set in the constructor/)
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
