// @vitest-environment jsdom
//
// Live cascade guard for the chat autocomplete tooltip.
//
// The tooltip's look is a three-way war: @codemirror/autocomplete's
// baseTheme styles the same elements with editor-scoped selectors
// (`.ͼbase .cm-tooltip.cm-tooltip-autocomplete > ul`, specificity (0,3,1))
// that beat any global index.css rule. The intended design therefore lives
// in createChatEditorTheme (cmChatTheme.ts), whose entries carry the chat
// editor's own scoping class — CodeMirror mirrors that class onto the
// tooltip's body-level wrapper — tie baseTheme on specificity and win on
// mount order. This test resolves the actual winner of that war by mounting
// a real editor, opening the tooltip, and computing (specificity, document
// order) over every rule in the document that matches the live elements.
// It fails if CM bumps its baseTheme specificity, if the theme entries are
// removed, or if the class-mirroring mechanism that carries them disappears.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { EditorView } from '@codemirror/view'
import { EditorState } from '@codemirror/state'
import { autocompletion, startCompletion } from '@codemirror/autocomplete'
import { createChatEditorTheme } from '@/lib/cmChatTheme'

interface Candidate {
  selector: string
  value: string
  spec: [number, number, number]
  order: number
}

/**
 * Specificity triple of ONE simple selector (ids, class-level, element-level).
 * Unicode-aware: CodeMirror's scoping classes are `.ͼN` (non-ASCII letters),
 * which ASCII `\w` would miss and mis-classify as elements.
 */
function specificityOfSelector(selector: string): [number, number, number] {
  let ids = 0
  let classes = 0
  let elements = 0
  const re =
    /#[\p{L}\p{N}_-]+|::[\p{L}\p{N}_-]+|:[\p{L}\p{N}_-]+(?:\([^)]*\))?|\[[^\]]*\]|\.[-\p{L}\p{N}_]+|[\p{L}\p{N}_]+/gu
  for (const m of selector.matchAll(re)) {
    const token = m[0]
    if (token.startsWith('#')) ids++
    else if (token.startsWith('::')) elements++
    else if (token.startsWith(':')) classes++
    else if (token.startsWith('[')) classes++
    else if (token.startsWith('.')) classes++
    else elements++
  }
  return [ids, classes, elements]
}

/** Specificity of a selector list as it applies to `el`: the max over the
 * list entries that actually match (CSS counts only the matching selector). */
function specificity(selector: string, el: Element): [number, number, number] {
  let best: [number, number, number] = [0, 0, 0]
  for (const part of selector.split(',')) {
    const trimmed = part.trim()
    if (!trimmed) continue
    try {
      if (!el.matches(trimmed)) continue
    } catch {
      continue
    }
    const spec = specificityOfSelector(trimmed)
    if (cmpSpec(spec, best) > 0) best = spec
  }
  return best
}

function cmpSpec(a: [number, number, number], b: [number, number, number]): number {
  return a[0] - b[0] || a[1] - b[1] || a[2] - b[2]
}

/**
 * Every declaration of `prop` in the document whose rule matches `el`,
 * as (specificity, document order) candidates.
 */
function declarationsFor(el: Element, prop: string): Candidate[] {
  const found: Candidate[] = []
  let sheetIndex = 0
  for (const style of Array.from(document.querySelectorAll('style'))) {
    const sheet = (style as HTMLStyleElement).sheet
    const rules = Array.from(sheet?.cssRules ?? [])
    rules.forEach((rule, ruleIndex) => {
      const styleRule = rule as CSSStyleRule
      if (typeof styleRule.selectorText !== 'string' || !styleRule.style) return
      let matches = false
      try {
        matches = el.matches(styleRule.selectorText)
      } catch {
        return // selector jsdom cannot evaluate — not ours to judge
      }
      if (!matches) return
      const value = styleRule.style.getPropertyValue(prop)
      if (!value) return
      found.push({
        selector: styleRule.selectorText,
        value,
        spec: specificity(styleRule.selectorText, el),
        order: sheetIndex * 10000 + ruleIndex,
      })
    })
    sheetIndex++
  }
  return found
}

function winner(candidates: Candidate[]): Candidate {
  expect(candidates.length).toBeGreaterThan(0)
  return candidates.reduce((best, c) =>
    cmpSpec(c.spec, best.spec) > 0 || (cmpSpec(c.spec, best.spec) === 0 && c.order > best.order) ? c : best,
  )
}

/** Bounded fake-timer poll for a positive condition (project timing rules: no sleeps). */
async function until<T>(cond: () => T | null, what: string): Promise<T> {
  for (let elapsed = 0; elapsed <= 4000; elapsed += 20) {
    const value = cond()
    if (value !== null) return value
    await vi.advanceTimersByTimeAsync(20)
  }
  throw new Error(`timeout waiting for: ${what}`)
}

describe('chat autocomplete tooltip — live cascade winner', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
    document.body.replaceChildren()
  })

  async function openTooltip(): Promise<HTMLElement> {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const view = new EditorView({
      state: EditorState.create({
        doc: 'hello',
        extensions: [
          createChatEditorTheme(true),
          autocompletion({
            override: [
              (ctx) =>
                Promise.resolve({
                  from: ctx.pos,
                  options: [{ label: 'alpha-skill', type: 'keyword' }],
                  filter: false,
                }),
            ],
          }),
        ],
      }),
      parent: host,
    })
    view.dispatch({ selection: { anchor: 5 } })
    ;(startCompletion as (v: EditorView) => boolean)(view)

    const tooltip = await until(() => document.body.querySelector<HTMLElement>('.cm-tooltip-autocomplete'), 'tooltip to open')
    return tooltip
  }

  it('renders the tooltip list in the UI font chain, not CM base monospace', async () => {
    const tooltip = await openTooltip()

    // Mechanism sanity: the tooltip is body-level and its wrapper carries the
    // editor's style classes — the channel the theme entries ride. If CodeMirror
    // stops mirroring those classes, this assertion flags the breakage even
    // before the cascade outcome changes.
    const wrapper = tooltip.parentElement
    expect(wrapper).not.toBeNull()
    expect(wrapper!.className).toContain('cm-editor')

    const ul = tooltip.querySelector('ul')
    expect(ul).not.toBeNull()

    const fonts = declarationsFor(ul!, 'font-family')
    // The loser must be present — otherwise this test no longer guards the
    // real conflict and has gone blind to a baseTheme change.
    expect(fonts.some((c) => c.value.includes('monospace'))).toBe(true)
    expect(winner(fonts).value).toContain('var(--font-sans)')
  })

  it('keeps the intended geometry, row colors, and popover skin over baseTheme', async () => {
    const tooltip = await openTooltip()
    const ul = tooltip.querySelector('ul')
    expect(ul).not.toBeNull()

    expect(winner(declarationsFor(ul!, 'max-height')).value).toBe('480px')

    const li = ul!.querySelector('li')
    expect(li).not.toBeNull()
    expect(winner(declarationsFor(li!, 'padding')).value).toContain('0.5rem')

    const selected = ul!.querySelector('li[aria-selected]')
    expect(selected).not.toBeNull()
    expect(winner(declarationsFor(selected!, 'background-color')).value).toContain('var(--color-muted)')

    expect(winner(declarationsFor(tooltip, 'background-color')).value).toContain('var(--color-popover)')
  })
})
