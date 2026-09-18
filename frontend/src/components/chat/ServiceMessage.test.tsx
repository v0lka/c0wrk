// @vitest-environment jsdom
// Regression: the variant icon sits in a width-capped flex row next to the
// notice text. For SVGs `min-width: auto` resolves to 0 (SVG has
// overflow:hidden), so without `flex-shrink: 0` a long silent-mode notice
// ("Silent mode: allowed …") squeezes the glyph from 14px down to a few
// pixels — the same root cause documented in ToolCard's StatusIcon.
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ServiceMessage } from './ServiceMessage'
import type { DisplayItem } from '@/types/messages'

type ServiceItem = Extract<DisplayItem, { kind: 'service' }>

const LONG_NOTICE =
  'Silent mode: allowed write_file (writes a workspace file) — ran unattended: ' +
  'no hard safety reason (the command_unbounded_analysis limitation on the flowsh ' +
  'judge is deliberately non-canonical)'

function makeItem(variant: ServiceItem['variant'], metadata?: Record<string, unknown>): ServiceItem {
  return { kind: 'service', id: 'svc-1', variant, content: LONG_NOTICE, metadata }
}

describe('ServiceMessage', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.replaceChildren()
  })

  const render = (item: ServiceItem) =>
    act(() => {
      root.render(<ServiceMessage item={item} />)
    })

  // SVG className is an SVGAnimatedString — read the class attribute instead.
  const iconClasses = () =>
    Array.from(container.querySelectorAll('svg')).map(el => el.getAttribute('class') ?? '')

  it.each(['status', 'routing', 'retry', 'step_retry'] as ServiceItem['variant'][])(
    'keeps the %s variant icon at its fixed size inside the narrow flex row',
    variant => {
      render(makeItem(variant))
      const icon = iconClasses()
      expect(icon).toHaveLength(1)
      expect(icon[0]).toContain('h-3.5')
      expect(icon[0]).toContain('w-3.5')
      // The actual regression: without shrink-0 the flex algorithm shrinks the
      // SVG below its 14px box when the notice text is long.
      expect(icon[0]).toContain('shrink-0')
    },
  )

  it('renders the notice text next to the icon', () => {
    render(makeItem('status'))
    expect(container.textContent).toContain('Silent mode: allowed write_file')
  })

  // ── Autonomy-decision icon tone: the verdict decides the glyph color ──

  it('paints the icon success green for an ALLOW-family autonomy verdict', () => {
    render(makeItem('status', {
      kind: 'tool_confirm', mode: 'silent', policy: 'allow', verdict: 'allow', tool: 'write_file',
    }))
    const cls = iconClasses()[0] ?? ''
    expect(cls).toContain('text-success')
    expect(cls).not.toContain('text-destructive')
  })

  it('paints the icon destructive red for a DENY autonomy verdict', () => {
    render(makeItem('status', {
      kind: 'tool_confirm', mode: 'silent', policy: 'judge', verdict: 'deny', tool: 'bash_exec',
    }))
    const cls = iconClasses()[0] ?? ''
    expect(cls).toContain('text-destructive')
    expect(cls).not.toContain('text-success')
  })

  it('treats step-limit allow_more as an ALLOW-family verdict', () => {
    render(makeItem('status', { kind: 'step_limit', mode: 'silent', verdict: 'allow_more' }))
    expect(iconClasses()[0] ?? '').toContain('text-success')
  })

  it('keeps the muted icon for non-autonomy rows and unknown verdicts', () => {
    render(makeItem('status'))
    const cls = iconClasses()[0] ?? ''
    expect(cls).not.toContain('text-success')
    expect(cls).not.toContain('text-destructive')

    render(makeItem('status', { kind: 'tool_confirm', verdict: 'unknown' }))
    const unknown = iconClasses()[0] ?? ''
    expect(unknown).not.toContain('text-success')
    expect(unknown).not.toContain('text-destructive')
  })

  it('does not throw on a malformed payload with a non-string verdict', () => {
    // A corrupted/foreign metadata payload with `kind`/`verdict` keys but a
    // non-string verdict previously reached `verdict.startsWith` and threw a
    // TypeError during render, crashing the memoized chat subtree. The boundary
    // guard must reject it so the row renders with the muted icon.
    render(makeItem('status', { kind: 'tool_confirm', verdict: 123 }))
    const cls = iconClasses()[0] ?? ''
    expect(cls).not.toContain('text-success')
    expect(cls).not.toContain('text-destructive')
  })
})
