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

function makeItem(variant: ServiceItem['variant']): ServiceItem {
  return { kind: 'service', id: 'svc-1', variant, content: LONG_NOTICE }
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
})
