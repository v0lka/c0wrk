// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { EditorView } from '@codemirror/view'
import { EditorState } from '@codemirror/state'
import { startCompletion, closeCompletion, completionStatus } from '@codemirror/autocomplete'
import { TOOLTIP_DELAY_MS } from '@/components/ui/tooltip'
import type { FileEntry } from '@/types/models'

// The controller mounts its React pane from CodeMirror's measure/event paths —
// outside any act() — so this file disables the global act-environment flag
// and asserts against the committed DOM instead. Restored nowhere: vitest
// isolates globals per file and setup.ts re-arms the flag for every other one.
const g = globalThis as Record<string, unknown>
g.IS_REACT_ACT_ENVIRONMENT = false

const { listDirectoryMock, getSessionWorkspaceMock, getMCPMentionableServersMock } = vi.hoisted(() => ({
  listDirectoryMock: vi.fn(),
  getSessionWorkspaceMock: vi.fn(),
  getMCPMentionableServersMock: vi.fn(),
}))

vi.mock('@/api/workspace', () => ({
  listDirectory: (...args: unknown[]) => listDirectoryMock(...args),
  getSessionWorkspace: (...args: unknown[]) => getSessionWorkspaceMock(...args),
}))
vi.mock('@/api/skills', () => ({
  listSkills: vi.fn().mockResolvedValue([]),
}))
vi.mock('@/api/agents', () => ({
  listAgents: vi.fn().mockResolvedValue([]),
}))
vi.mock('@/api/mcp', () => ({
  getMCPMentionableServers: (...args: unknown[]) => getMCPMentionableServersMock(...args),
  isMentionableMCPMode: (mode: string) => mode === 'auto' || mode === 'manual',
}))
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn(() => () => {}),
}))

import { createChatAutocomplete } from './cmChatAutocomplete'
import { listAgents } from '@/api/agents'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useUiScaleStore } from '@/stores/uiScaleStore'
import { computeMentionTooltipPlacement } from './cmMentionTooltip'

const ENTRIES: FileEntry[] = [
  { name: 'alpha.txt', path: '/ws/alpha.txt', is_dir: false },
  { name: 'beta', path: '/ws/beta', is_dir: true },
]

// Bounded virtual scheduler drain for a positive completion condition. No real
// timeout is used as absence evidence; negative checks follow source completion.
async function until(cond: () => boolean, what: string, timeoutMs = 4000): Promise<void> {
  for (let elapsed = 0; elapsed <= timeoutMs; elapsed += 20) {
    if (cond()) return
    await vi.advanceTimersByTimeAsync(20)
  }
  throw new Error(`timeout waiting for: ${what}`)
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
  // Store transitions fire the completion caches' invalidation subscriptions,
  // so per-test project ids (set below) keep every test on fresh fixtures.
  useFileTreeStore.setState({ rootPath: '' })
})

function makeView(): { view: EditorView; host: HTMLElement } {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const view = new EditorView({
    state: EditorState.create({ doc: '', extensions: [createChatAutocomplete()] }),
    parent: host,
  })
  return { view, host }
}

function typeAndComplete(view: EditorView, text: string): void {
  view.dispatch(view.state.replaceSelection(text))
  startCompletion(view)
  expect(completionStatus(view.state)).toBe('pending')
}

const hostEl = (): HTMLElement => document.querySelector('.cm-mention-tooltip-host') as HTMLElement
const paneEl = (): HTMLElement | null => hostEl()?.querySelector('[role="tooltip"]') ?? null
const rows = (): HTMLElement[] =>
  Array.from(document.querySelectorAll('.cm-tooltip-autocomplete li[id]')) as HTMLElement[]

describe('mention markdown pane', () => {
  const views: { view: EditorView; host: HTMLElement }[] = []
  afterEach(() => {
    for (const { view, host } of views) {
      view.destroy()
      host.remove()
    }
    views.length = 0
    vi.clearAllMocks()
  })

  async function openSlash(): Promise<EditorView> {
    getMCPMentionableServersMock.mockResolvedValue([])
    const { view, host } = makeView()
    views.push({ view, host })
    typeAndComplete(view, '/')
    await until(() => completionStatus(view.state) !== 'pending', 'completion source to settle')
    await until(() => rows().length > 0, 'rendered rows')
    return view
  }

  async function hoverRow(index: number): Promise<void> {
    rows()[index]!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))
  }

  it('opens for the hovered row after the standard delay and renders sanitized markdown in the chat tooltip chrome', async () => {
    const description = '## Alpha agent first\n\nHandles **reviews** with plain text'
    vi.mocked(listAgents).mockResolvedValue([{ name: 'reviewer', description }])
    await openSlash()

    await hoverRow(0)
    // Hover-only activation: the row the popup pre-selected does NOT open the
    // pane on its own — only the pointer, after TOOLTIP_DELAY_MS (the same
    // single source the chat's TooltipProvider gates its tooltips with).
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS - 1)
    expect(hostEl().style.visibility).not.toBe('visible')
    await vi.advanceTimersByTimeAsync(1)
    await until(() => hostEl().style.visibility === 'visible', 'hover pane visible')

    // VISIBLE markdown (heading + strong), with hostile markup from the
    // workspace-authored description kept inert.
    expect(hostEl().querySelector('h2')?.textContent).toBe('Alpha agent first')
    expect(hostEl().querySelector('strong')?.textContent).toBe('reviews')
    expect(hostEl().querySelector('script')).toBeNull()
    expect(hostEl().querySelector('img')).toBeNull()
    expect(hostEl().innerHTML).not.toContain('onerror')

    // The chrome mirrors the chat's TooltipContent (ui/tooltip.tsx): same box,
    // border, shadow, entry animation — plus its diamond arrow pointing at
    // the hovered row, clamped to a concrete X once the pane is measured.
    const pane = paneEl()
    expect(pane).not.toBeNull()
    for (const cls of ['rounded-md', 'border-border', 'bg-background', 'shadow-[0_4px_12px_var(--color-shadow)]', 'animate-in']) {
      expect(pane!.classList.contains(cls)).toBe(true)
    }
    const arrow = pane!.querySelector('span[aria-hidden]')
    expect(arrow).not.toBeNull()
    expect(arrow!.classList.contains('rotate-45')).toBe(true)
    expect(arrow!.classList.contains('bg-background')).toBe(true)
    expect(arrow!.classList.contains('size-2.5')).toBe(true)
    expect(arrow!.classList.contains('rounded-[2px]')).toBe(true)
    expect(arrow!.classList.contains('border')).toBe(false)
    expect((arrow as HTMLElement).style.transform).toContain('rotate(45deg)')
    expect((arrow as HTMLElement).style.left).toMatch(/px$/)
  })

  it('never opens for the keyboard-selected row — only hover shows it', async () => {
    vi.mocked(listAgents).mockResolvedValue([
      { name: 'alpha', description: 'Alpha agent first description' },
      { name: 'beta', description: 'Beta agent second description with more words' },
    ])
    await openSlash()

    // selectOnOpen selects row 0: well past the standard delay the pane is
    // still hidden — selection without a pointer never arms the show timer.
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 200)
    expect(hostEl().style.visibility).not.toBe('visible')

    await hoverRow(1)
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
    await until(
      () => (hostEl()?.textContent ?? '').includes('Beta agent second'),
      'hover pane for row 1',
    )
  })

  it('resolves the hovered row by its id index, not its DOM position (virtualized list)', async () => {
    vi.mocked(listAgents).mockResolvedValue([
      { name: 'alpha', description: 'Alpha agent description' },
      { name: 'beta', description: 'Beta agent description' },
    ])
    // The completion caches are module-level and refreshed only on a rootPath
    // change; bump it so this test's agents mock is fetched (not a previous
    // test's cached list).
    useFileTreeStore.setState({ rootPath: '/mention-virtualized' })
    await openSlash()

    // CodeMirror virtualizes the completion list: each row's id ends in its
    // ABSOLUTE option index (`<listId>-<i>`), which stops matching the row's
    // DOM position once the rendered window starts at `range.from > 0` (a list
    // longer than maxRenderedOptions, 100). Model that mismatch by giving the
    // first rendered row the id of a later option: the pane must show THAT
    // completion's description, not the one at DOM position 0 (which is what
    // counting `li` siblings would wrongly resolve).
    rows()[0]!.id = 'virtualized-1'
    expect(rows()[0]!.id).toMatch(/-1$/)

    await hoverRow(0)
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
    await until(
      () => (hostEl()?.textContent ?? '').includes('Beta agent description'),
      'pane resolved via the row id index',
    )
    expect(hostEl()?.textContent ?? '').not.toContain('Alpha agent description')
  })

  it('hides immediately when the pointer leaves the row, with nothing to fall back to', async () => {
    vi.mocked(listAgents).mockResolvedValue([{ name: 'alpha', description: 'Alpha agent first description' }])
    await openSlash()

    await hoverRow(0)
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
    await until(() => hostEl().style.visibility === 'visible', 'hover pane visible')

    rows()[0]!.dispatchEvent(new MouseEvent('mouseout', { bubbles: true, relatedTarget: document.body }))
    // The chat tooltip closes without dwell time on pointer leave — so does
    // the pane; no keyboard selection remains for it to "revert" to.
    expect(hostEl().style.visibility).not.toBe('visible')
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 100)
    expect(hostEl().style.visibility).not.toBe('visible')
  })

  it('stays hidden for rows without a description (file items)', async () => {
    useFileTreeStore.setState({ rootPath: '/ws' })
    listDirectoryMock.mockResolvedValue(ENTRIES)
    const { view, host } = makeView()
    views.push({ view, host })
    typeAndComplete(view, '@')
    await until(() => completionStatus(view.state) !== 'pending', 'completion source to settle')
    await until(() => rows().length > 0, 'file rows')

    rows()[1]!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
    expect(hostEl().style.visibility).not.toBe('visible')
  })

  it('hides the pane on popup close, and destroying the editor never leaves a visible pane', async () => {
    vi.mocked(listAgents).mockResolvedValue([{ name: 'reviewer', description: 'Alpha agent first description' }])
    const view = await openSlash()
    await hoverRow(0)
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
    await until(() => hostEl().style.visibility === 'visible', 'pane visible')

    closeCompletion(view)
    await until(() => hostEl().style.visibility !== 'visible', 'pane hidden on close')

    view.destroy()
    expect(hostEl().style.visibility).not.toBe('visible')
    // The host is an app-lifetime shared layer (no React-root churn on editor
    // remounts) — destroying an editor only ever hides the pane.
    expect(document.querySelectorAll('.cm-mention-tooltip-host')).toHaveLength(1)
  })

  it('anchors to the pointer point — the pane opens over the cursor, not the row rect', async () => {
    vi.mocked(listAgents).mockResolvedValue([{ name: 'reviewer', description: 'Alpha agent first description' }])
    await openSlash()

    // Pointer at (222, 111): the fixed host is placed in layout px from that
    // point (zoom 1 in jsdom) — above the pointer with the 20px gap (two
    // diamond heights). jsdom has no layout, so the pane measures 0×0 and
    // left = x, top = y-20 — exact values a row-rect anchor (all-zero rects
    // in jsdom) could never produce.
    rows()[0]!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, clientX: 222, clientY: 111 }))
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
    await until(() => hostEl().style.visibility === 'visible', 'pane visible')
    expect(hostEl().style.left).toBe('222px')
    expect(hostEl().style.top).toBe('91px')
    // Diamond on the bottom edge — the pane hangs above the pointer.
    await until(
      () => paneEl()?.querySelector('span[aria-hidden]')?.classList.contains('bottom-[-5px]') === true,
      'diamond on the bottom edge',
    )
  })

  it('uses the real measured pane size — bottom edge two diamond heights above the cursor (0×0-measure regression)', async () => {
    vi.mocked(listAgents).mockResolvedValue([{ name: 'reviewer', description: 'Alpha agent first description' }])
    await openSlash()

    // jsdom has no layout, so lend every element the pane's real size: the
    // controller must measure the pane through the hidden host. A display:none
    // host measures 0×0 in a real browser — and the 0×0 placement pins the
    // pane's top-left corner to the pointer, hanging the whole box down-side
    // of it (the reported bug). The mocked getters below bypass the layout
    // engine, so the fix's DOM contract is pinned explicitly instead: the
    // host toggles visibility (a visibility-hidden subtree KEEPS its layout,
    // so the first measure is real) and never display.
    expect(hostEl().style.visibility).toBe('hidden')
    expect(hostEl().style.display).toBe('')
    const width = vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(448)
    const height = vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(190)
    try {
      rows()[0]!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, clientX: 300, clientY: 500 }))
      await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
      await until(() => hostEl().style.visibility === 'visible', 'pane visible')
      expect(hostEl().style.display).toBe('')
      // top = 500 - 20 - 190 = 290 → the pane's bottom edge at 480 = cursor
      // - 20; left = 300 - 448/2 = 76. A 0×0 measure would give 500/300.
      expect(hostEl().style.top).toBe('290px')
      expect(hostEl().style.left).toBe('76px')
      // The diamond tracks the cursor through the measured geometry: 224px
      // from the pane's left edge (= cursor x), inside the clamped span.
      await until(
        () => (paneEl()?.querySelector('span[aria-hidden]') as HTMLElement | null)?.style.left === '224px',
        'diamond X computed from the real size',
      )
      expect(paneEl()?.querySelector('span[aria-hidden]')?.classList.contains('bottom-[-5px]')).toBe(true)
    } finally {
      width.mockRestore()
      height.mockRestore()
    }
  })

  it('divides the pointer anchor by the live UI zoom — the layout-px host placement', async () => {
    vi.mocked(listAgents).mockResolvedValue([{ name: 'reviewer', description: 'Alpha agent first description' }])
    await openSlash()

    // At UI scale 150% clientX/clientY are VISUAL px: 300/1.5=200, 150/1.5=100
    // (top 100-20=80). Writing visual px straight into the fixed host would
    // displace the pane by coordinate × (zoom − 1) — the model-picker bug.
    useUiScaleStore.setState({ scale: 150 })
    try {
      rows()[0]!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, clientX: 300, clientY: 150 }))
      await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
      await until(() => hostEl().style.visibility === 'visible', 'pane visible')
      expect(hostEl().style.left).toBe('200px')
      expect(hostEl().style.top).toBe('80px')
    } finally {
      useUiScaleStore.setState({ scale: 100 })
    }
  })

  it('moves the diamond to the pane top edge when the pane flips below the pointer', async () => {
    vi.mocked(listAgents).mockResolvedValue([{ name: 'reviewer', description: 'Alpha agent first description' }])
    await openSlash()

    // y 5: no room above (5-16 < 0) → the pane flips below the pointer and
    // the diamond moves to its top edge, so it still points at the object.
    rows()[0]!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, clientX: 200, clientY: 5 }))
    await vi.advanceTimersByTimeAsync(TOOLTIP_DELAY_MS + 50)
    await until(() => hostEl().style.visibility === 'visible', 'pane visible')
    // Flipped: the pane's top edge opens 20px (two diamond heights) BELOW the
    // cursor — the only side left when the viewport has no room above.
    expect(hostEl().style.top).toBe('25px')
    await until(
      () => paneEl()?.querySelector('span[aria-hidden]')?.classList.contains('top-[-5px]') === true,
      'diamond moved to the top edge',
    )
    expect(paneEl()!.querySelector('span[aria-hidden]')!.classList.contains('bottom-[-5px]')).toBe(false)
  })
})

describe('computeMentionTooltipPlacement', () => {
  // Defaults: gap 20 (two heights of the size-2.5 diamond), margin 16 (the
  // collision padding EllipsisHint's tooltips keep from the viewport edges).
  const base = {
    anchor: { x: 100, y: 200 },
    size: { width: 120, height: 60 },
    viewport: { width: 500, height: 400 },
  }

  it('sits above the pointer with the gap, centered on it, diamond on the pointer', () => {
    expect(computeMentionTooltipPlacement(base)).toEqual({
      left: 40,
      top: 120,
      arrowX: 60,
      side: 'above',
    })
  })

  it('prefers above when both sides fit — the tooltip identity', () => {
    // 400-16-200=184 of room below also fits; above still wins.
    expect(computeMentionTooltipPlacement(base).side).toBe('above')
  })

  it('flips below the pointer, diamond on the top edge, when there is no room above', () => {
    // Above: 30-16=14 < 60 → below: 30+20.
    expect(computeMentionTooltipPlacement({ ...base, anchor: { x: 100, y: 30 } })).toEqual({
      left: 40,
      top: 50,
      arrowX: 60,
      side: 'below',
    })
  })

  it('takes the roomier side when neither fits, above on a tie', () => {
    const args = { ...base, viewport: { width: 500, height: 140 } }
    // y 70: 54 above vs 54 below → tie → above. y 65: 49 vs 59 → below.
    expect(computeMentionTooltipPlacement({ ...args, anchor: { x: 100, y: 70 } }).side).toBe('above')
    expect(computeMentionTooltipPlacement({ ...args, anchor: { x: 100, y: 65 } }).side).toBe('below')
  })

  it('clamps the pane inside the viewport and pins the diamond to the pane edge', () => {
    const edge = computeMentionTooltipPlacement({ ...base, anchor: { x: 440, y: 200 } })
    // Raw left 440-60=380 > 500-16-120=364 → clamped; the diamond cannot
    // follow the pointer past the pane's straight edge (120-12).
    expect(edge.left).toBe(364)
    expect(edge.arrowX).toBe(76)
  })

  it('clamps the top inside the viewport when the chosen side still overflows', () => {
    // y 78: room above (62) fits → above; raw top 78-20-60=-2 < margin → 16.
    const low = computeMentionTooltipPlacement({
      ...base,
      anchor: { x: 100, y: 78 },
      viewport: { width: 500, height: 160 },
    })
    expect(low.top).toBe(16)
    expect(low.side).toBe('above')
  })
})
