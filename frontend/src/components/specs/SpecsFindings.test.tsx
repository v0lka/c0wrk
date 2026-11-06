// @vitest-environment jsdom
// SpecsFindings — the tier-1 findings list's CASCADE CARD layout (ADR-106
// D5): row 1 carries the fix badge (icon + kind chip as one button — the
// per-finding spec-update dispatch, ADR-109) + the file link, row 2 the
// detail text (flex-1) with the hover-revealed copy button at its trailing
// edge — always in the DOM, transparent until the card (the hover `group`)
// is hovered, copying the verbatim findingLine the dispatched fix prompt
// carries (ADR-109 D7). Renders NOTHING when clean — the header badge is
// the clean signal.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsFindings } from './SpecsFindings'
import { findingLine } from './specsActions'
import { clipboardSetText } from '@/api/runtime'
import type { SpecFinding } from '@/types/models'

// Copy must go through the native Wails runtime, NOT navigator.clipboard
// (mirrors CopyIconButton.test). The boundary module is mocked so no real
// window.runtime binding is touched.
vi.mock('@/api/runtime', () => ({
  clipboardSetText: vi.fn().mockResolvedValue(true),
}))

let container: HTMLElement | null = null
let root: Root | null = null
let onOpenMock = vi.fn()
let onFixMock = vi.fn()

async function render(findings: SpecFinding[]): Promise<void> {
  onOpenMock = vi.fn()
  onFixMock = vi.fn()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(
      <SpecsFindings
        findings={findings}
        onOpen={onOpenMock}
        onFixFinding={onFixMock}
      />,
    )
  })
}

function query(testId: string): HTMLElement | null {
  return container?.querySelector(`[data-testid="${testId}"]`) ?? null
}

const FIX_TITLE = 'Fix this finding in the chat — Shift = new session'

const FINDINGS: SpecFinding[] = [
  { kind: 'PATH_MISSING', file: 'domains/testing.md', detail: 'core/x.go does not exist' },
  { kind: 'INDEX_STALE', file: 'domains/orphan.md', detail: 'not listed in INDEX.md' },
]

describe('SpecsFindings — cascade card', () => {
  it('renders nothing when the tree is clean', async () => {
    await render([])
    expect(query('specs-findings')).toBeNull()
  })

  it('row 1 carries the fix badge and the file link, row 2 the detail with the hover copy at its trailing edge', async () => {
    await render(FINDINGS)

    const cards = document.querySelectorAll('[data-testid="specs-finding"]')
    expect(cards).toHaveLength(2)

    const card = cards[0] as HTMLElement
    // Two stacked rows: the controls row and the detail row.
    const rows = Array.from(card.children)
    expect(rows).toHaveLength(2)

    // Row 1: the fix badge (the kind label) + the clickable file (basename),
    // siblings in the controls row — untouched by the copy affordance.
    expect(rows[0]!.textContent).toContain('Missing path')
    const fix = rows[0]!.querySelector('[data-testid="specs-finding-fix"]')
    const link = rows[0]!.querySelector('[data-testid="specs-finding-file"]')!
    expect(fix).not.toBeNull()
    expect(link.textContent).toBe('testing.md')
    expect(link.getAttribute('data-file')).toBe('domains/testing.md')
    // The copy wrapper lives in row 2 ONLY — the controls row never grows it.
    expect(rows[0]!.querySelector('[data-testid="specs-finding-copy"]')).toBeNull()

    // Row 2: the detail on its OWN line (flex-1 — dominant width, never
    // squeezed into the controls row) with the hover-revealed copy button
    // pinned to the row's trailing edge.
    const detail = rows[1]!.querySelector('[data-testid="specs-finding-detail"]') ?? rows[1]!
    expect(detail.getAttribute('data-testid')).toBe('specs-finding-detail')
    expect(detail.textContent).toBe('core/x.go does not exist')
    expect(detail.className).toContain('flex-1')
    expect(detail.className).toContain('min-w-0')
    expect(rows[1]!.querySelector('[data-testid="specs-finding-copy"]')).not.toBeNull()
  })

  it('the fix badge is a warning-styled button with the exact tooltip', async () => {
    await render(FINDINGS)

    const fix = query('specs-finding-fix')!
    expect(fix.tagName).toBe('BUTTON')
    expect(fix.getAttribute('title')).toBe(FIX_TITLE)
    // The header badge's styling: warning chip tinting darker on hover.
    expect(fix.className).toContain('bg-warning/10')
    expect(fix.className).toContain('hover:bg-warning/20')
    expect(fix.className).toContain('text-warning')
  })

  it('clicking the fix badge requests the per-finding fix for THAT finding', async () => {
    await render(FINDINGS)

    const fixes = document.querySelectorAll<HTMLElement>('[data-testid="specs-finding-fix"]')
    act(() => {
      fixes[0]!.click()
    })
    expect(onFixMock).toHaveBeenCalledTimes(1)
    expect(onFixMock).toHaveBeenCalledWith(FINDINGS[0]!, false)

    act(() => {
      fixes[1]!.click()
    })
    expect(onFixMock).toHaveBeenCalledTimes(2)
    expect(onFixMock).toHaveBeenLastCalledWith(FINDINGS[1]!, false)
    // The click dispatches — it never opens the viewer.
    expect(onOpenMock).not.toHaveBeenCalled()
  })

  it('shift-clicking the fix badge requests a NEW session', async () => {
    await render(FINDINGS)

    const fix = query('specs-finding-fix')!
    await act(async () => {
      fix.dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
    })
    expect(onFixMock).toHaveBeenCalledTimes(1)
    expect(onFixMock).toHaveBeenCalledWith(FINDINGS[0]!, true)
  })

  it('the file link opens the finding file through onOpen', async () => {
    await render(FINDINGS)

    const links = document.querySelectorAll<HTMLButtonElement>(
      '[data-testid="specs-finding-file"]',
    )
    act(() => {
      links[1]!.click()
    })
    expect(onOpenMock).toHaveBeenCalledWith('domains/orphan.md')
    // Opening via the link never dispatches the fix.
    expect(onFixMock).not.toHaveBeenCalled()
  })
})

describe('SpecsFindings — hover copy button (ADR-109 D7)', () => {
  // The wrapper must render the exact reveal classes the MessageFooter
  // hover pattern uses; pinned here so a styling refactor cannot silently
  // break the hover reveal (or turn it into a layout-shifting animation).
  const expectHoverReveal = (wrapper: Element): void => {
    expect(wrapper.className).toContain('opacity-0')
    expect(wrapper.className).toContain('transition-opacity')
    expect(wrapper.className).toContain('duration-150')
    expect(wrapper.className).toContain('group-hover:opacity-100')
    expect(wrapper.className).toContain('focus-within:opacity-100')
    // Trailing-edge anchor, never squeezed by the detail text.
    expect(wrapper.className).toContain('ml-auto')
    expect(wrapper.className).toContain('shrink-0')
    // Opacity-only reveal: no height animation, no display toggling —
    // hovering must not move a single pixel.
    expect(wrapper.className).not.toContain('max-h')
    expect(wrapper.className).not.toContain('hidden')
  }

  const copyButton = (): HTMLButtonElement =>
    document.querySelector('[data-testid="specs-finding-copy"] button') as HTMLButtonElement

  async function renderForCopy(findings: SpecFinding[]): Promise<void> {
    onOpenMock = vi.fn()
    onFixMock = vi.fn()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    await act(async () => {
      root!.render(
        <SpecsFindings findings={findings} onOpen={onOpenMock} onFixFinding={onFixMock} />,
      )
    })
  }

  async function clickCopy(): Promise<HTMLButtonElement> {
    const button = copyButton()
    await act(async () => {
      button.click()
      await vi.advanceTimersByTimeAsync(0)
    })
    return button
  }

  beforeEach(() => {
    vi.useFakeTimers()
    // Pin the success verdict explicitly: the failure test below overrides it.
    vi.mocked(clipboardSetText).mockResolvedValue(true)
    // Prove the native runtime path is taken, never navigator.clipboard.
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: undefined,
    })
  })

  afterEach(() => {
    // Unmount BEFORE restoring real timers: the revert timer is cleared by
    // the component's unmount cleanup, so nothing fires into a dead tree.
    act(() => root!.unmount())
    vi.clearAllMocks()
    vi.useRealTimers()
  })

  it('the copy button is always in the DOM, transparent until the card is hovered (opacity-only reveal)', async () => {
    await renderForCopy(FINDINGS)

    const cards = document.querySelectorAll('[data-testid="specs-finding"]')
    expect(cards).toHaveLength(2)
    for (const card of cards) {
      // The card is the hover group the reveal keys off.
      expect(card.className).toContain('group')
      // One copy affordance per card, always mounted (not hover-conditional).
      const wrappers = card.querySelectorAll('[data-testid="specs-finding-copy"]')
      expect(wrappers).toHaveLength(1)
      expectHoverReveal(wrappers[0]!)
      expect(wrappers[0]!.querySelector('button')).not.toBeNull()
    }
  })

  it('clicking the copy button copies the verbatim findingLine — the same bullet the dispatched fix prompt carries', async () => {
    await renderForCopy([FINDINGS[0]!])

    await clickCopy()

    expect(clipboardSetText).toHaveBeenCalledTimes(1)
    // Exactly the dispatch-prompt bullet format: '- [KIND] file: detail'.
    expect(vi.mocked(clipboardSetText).mock.calls[0]![0]).toBe(
      '- [PATH_MISSING] domains/testing.md: core/x.go does not exist',
    )
    // Single source: byte-identical to the header badge's prompt line.
    expect(vi.mocked(clipboardSetText).mock.calls[0]![0]).toBe(findingLine(FINDINGS[0]!))
  })

  it('the copy click stays local — it never dispatches the fix nor opens the viewer', async () => {
    await renderForCopy(FINDINGS)

    await clickCopy()

    expect(onFixMock).not.toHaveBeenCalled()
    expect(onOpenMock).not.toHaveBeenCalled()
  })

  it('success flashes Copied (Check, text-success) and reverts to the idle label after 2 s', async () => {
    await renderForCopy([FINDINGS[0]!])

    const button = await clickCopy()
    expect(button.getAttribute('aria-label')).toBe('Copied')
    expect(button.querySelector('svg.lucide-check')).not.toBeNull()
    expect(button.querySelector('svg')!.getAttribute('class')).toContain('text-success')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(button.getAttribute('aria-label')).toBe('Copy finding line')
    expect(button.querySelector('svg.lucide-copy')).not.toBeNull()
    expect(button.querySelector('svg.lucide-check')).toBeNull()
  })

  it('a failed clipboard write flashes Failed (X, text-destructive) — never Copied', async () => {
    vi.mocked(clipboardSetText).mockResolvedValue(false)
    await renderForCopy([FINDINGS[0]!])

    const button = await clickCopy()
    expect(button.getAttribute('aria-label')).toBe('Failed')
    expect(button.querySelector('svg.lucide-check')).toBeNull()
    const icon = button.querySelector('svg')!
    expect(icon.getAttribute('class')).toContain('text-destructive')
    expect(icon.getAttribute('class')).not.toContain('text-success')

    // The failure flash is bounded like the success flash.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(button.getAttribute('aria-label')).toBe('Copy finding line')
    expect(button.querySelector('svg.lucide-x')).toBeNull()
  })
})
