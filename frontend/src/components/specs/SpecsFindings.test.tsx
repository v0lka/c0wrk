// @vitest-environment jsdom
// SpecsFindings — the finding list of a declared view section: row 1 the fix
// badge (a BUTTON only when the framework declared the findings-fix action,
// otherwise an INERT chip) + the file link, row 2 the detail with the
// hover-revealed copy button carrying the verbatim findingLine. The LABEL is
// the check's declared message (readable token as fallback), never a frontend
// vocabulary. Renders nothing when clean.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsFindings } from './SpecsFindings'
import { findingLine, REQUIREMENT_UNMET_REASON } from './specsActions'
import { clipboardSetText } from '@/api/runtime'
import { makeAction, makeFinding, makeView } from '@/test/sddFixture'
import type { SDDAction, SDDFinding, SDDView } from '@/types/models'

vi.mock('@/api/runtime', () => ({
  clipboardSetText: vi.fn().mockResolvedValue(true),
}))

let container: HTMLElement | null = null
let root: Root | null = null
let onOpenMock = vi.fn()
let onDispatchMock = vi.fn()

const SECTION: SDDView = makeView({ id: 'findings', label: 'Findings', kind: 'findings' })

async function render(findings: SDDFinding[], fixAction: SDDAction | null): Promise<void> {
  onOpenMock = vi.fn()
  onDispatchMock = vi.fn()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(
      <SpecsFindings
        section={SECTION}
        findings={findings}
        fixAction={fixAction}
        onOpen={onOpenMock}
        onDispatch={onDispatchMock}
      />,
    )
  })
}

function query(testId: string): HTMLElement | null {
  return container?.querySelector(`[data-testid="${testId}"]`) ?? null
}

const FIX_ACTION: SDDAction = makeAction({
  kind: 'fix-findings',
  label: 'Fix findings',
  skill: 'vibespec-update',
  prompt: 'Repair every finding, then re-check the tree.',
  requires: ['root', 'findings'],
})

const FINDINGS: SDDFinding[] = [
  makeFinding({
    kind: 'PATH_MISSING',
    file: 'specs/domains/testing.md',
    detail: 'core/x.go does not exist',
    message: 'a Key Files path must exist',
  }),
  makeFinding({ kind: 'INDEX_STALE', file: 'specs/domains/orphan.md', detail: 'not listed' }),
]

beforeEach(() => {
  vi.mocked(clipboardSetText).mockClear()
})

afterEach(() => {
  act(() => root?.unmount())
  root = null
  container = null
})

describe('SpecsFindings', () => {
  it('renders nothing when the tree is clean', async () => {
    await render([], FIX_ACTION)
    expect(container?.childElementCount).toBe(0)
  })

  it('renders the declared message as the label, the readable token as fallback', async () => {
    await render(FINDINGS, FIX_ACTION)
    const badges = container?.querySelectorAll('[data-testid="specs-finding-fix"]')
    expect(badges).toHaveLength(2)
    expect(badges?.[0]?.textContent).toContain('a Key Files path must exist')
    expect(badges?.[1]?.textContent).toContain('index stale')
    expect(container?.textContent).toContain('not listed')
  })

  it('dispatches the framework’s own findings-fix gesture from a finding card', async () => {
    await render([FINDINGS[0]!], FIX_ACTION)
    const badge = query('specs-finding-fix')
    expect(badge?.tagName).toBe('BUTTON')
    expect(badge?.getAttribute('data-skill')).toBe('vibespec-update')

    act(() => {
      badge?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(onDispatchMock).toHaveBeenCalledTimes(1)
    const [dispatch, newSession] = onDispatchMock.mock.calls[0] as [
      { skill: string; prompt: string; key: string },
      boolean,
    ]
    expect(newSession).toBe(false)
    expect(dispatch.skill).toBe('vibespec-update')
    // The declared instruction + the finding quoted verbatim (the line the
    // copy button hands out).
    expect(dispatch.prompt).toContain('Repair every finding')
    expect(dispatch.prompt).toContain(findingLine(FINDINGS[0]!))
  })

  it('passes the Shift modifier through (new session)', async () => {
    await render([FINDINGS[0]!], FIX_ACTION)
    act(() => {
      query('specs-finding-fix')?.dispatchEvent(
        new MouseEvent('click', { bubbles: true, shiftKey: true }),
      )
    })
    expect(onDispatchMock.mock.calls[0]?.[1]).toBe(true)
  })

  it('renders an INERT badge for a framework without the fix-findings action', async () => {
    await render([FINDINGS[0]!], null)
    const badge = query('specs-finding-fix')
    expect(badge?.tagName).toBe('SPAN')
    expect(badge?.getAttribute('data-inert')).toBe('true')

    act(() => {
      badge?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onDispatchMock).not.toHaveBeenCalled()
  })

  it('renders an inert chip naming the unmet requirement when the declared action is unavailable', async () => {
    await render([FINDINGS[0]!], { ...FIX_ACTION, available: false })
    const badge = query('specs-finding-fix')
    expect(badge?.tagName).toBe('SPAN')
    expect(badge?.getAttribute('title')).toBe(REQUIREMENT_UNMET_REASON)
  })

  it('names the missing skill — not the tree — when the declared action carries no skill', async () => {
    await render([FINDINGS[0]!], { ...FIX_ACTION, skill: '' })
    const badge = query('specs-finding-fix')
    expect(badge?.tagName).toBe('SPAN')
    expect(badge?.getAttribute('title')).toBe(
      'The framework declares this action with no skill to run',
    )
  })

  it('opens the finding file in the viewer', async () => {
    await render([FINDINGS[0]!], FIX_ACTION)
    act(() => {
      query('specs-finding-file')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onOpenMock).toHaveBeenCalledWith('specs/domains/testing.md')
  })

  it('copies the verbatim finding line', async () => {
    await render([FINDINGS[1]!], FIX_ACTION)
    // ASYNC act: the click starts the clipboard promise and the hook flashes
    // its verdict from the resolution, so the flush must be awaited — a sync
    // act() lets the state update land outside it (React act() warning).
    await act(async () => {
      query('specs-finding-copy')?.querySelector('button')?.dispatchEvent(
        new MouseEvent('click', { bubbles: true }),
      )
    })
    expect(clipboardSetText).toHaveBeenCalledWith(findingLine(FINDINGS[1]!))
  })

  it('shows the section’s own declared heading', async () => {
    await render(FINDINGS, FIX_ACTION)
    expect(query('specs-findings')?.textContent).toContain('Findings')
  })
})
