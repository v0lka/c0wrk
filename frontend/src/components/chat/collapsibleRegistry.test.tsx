// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { CollapsibleBlock } from './CollapsibleBlock'
import { collapsibleRegistry } from './collapsibleRegistry'

/** A controlled caller in the shape of PlanStepBlock / SubAgentBlock. */
function ControlledBlock({ revealId, onOpenChangeSeen }: {
  revealId: string
  onOpenChangeSeen?: (open: boolean) => void
}) {
  const [open, setOpen] = useState(true)
  return (
    <CollapsibleBlock
      label="controlled"
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        onOpenChangeSeen?.(next)
      }}
      revealId={revealId}
    >
      body
    </CollapsibleBlock>
  )
}

describe('collapsibleRegistry', () => {
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

  // Radix keeps CollapsibleContent mounted and toggles `data-state`.
  const contentState = () =>
    container.querySelector('[data-slot="collapsible-content"]')?.getAttribute('data-state')

  const renderUncontrolled = (revealId: string) =>
    act(() => {
      root.render(
        <CollapsibleBlock label="uncontrolled" revealId={revealId}>
          body
        </CollapsibleBlock>,
      )
    })

  it('registers on mount and unregisters on unmount (symmetric, no leaks)', () => {
    renderUncontrolled('reg-1')
    expect(collapsibleRegistry.get('reg-1')).toBeTypeOf('function')

    act(() => {
      root.unmount()
    })
    expect(collapsibleRegistry.get('reg-1')).toBeUndefined()
  })

  it('unregisters on revealId change and registers under the new id', () => {
    renderUncontrolled('reg-old')
    act(() => {
      root.render(
        <CollapsibleBlock label="uncontrolled" revealId="reg-new">
          body
        </CollapsibleBlock>,
      )
    })
    expect(collapsibleRegistry.get('reg-old')).toBeUndefined()
    expect(collapsibleRegistry.get('reg-new')).toBeTypeOf('function')
  })

  it('collapses an uncontrolled block (ToolCard/Thought style) via setOpen(false)', () => {
    renderUncontrolled('reg-tool')
    // Open it through the registry first to prove the callback is live both ways.
    act(() => {
      collapsibleRegistry.get('reg-tool')?.(true)
    })
    expect(contentState()).toBe('open')

    act(() => {
      collapsibleRegistry.get('reg-tool')?.(false)
    })
    expect(contentState()).toBe('closed')
  })

  it('collapses a controlled block (PlanStepBlock/SubAgentBlock style) via setOpen(false)', () => {
    const seen: boolean[] = []
    act(() => {
      root.render(
        <ControlledBlock revealId="reg-plan" onOpenChangeSeen={(o) => seen.push(o)} />,
      )
    })
    expect(contentState()).toBe('open')

    act(() => {
      collapsibleRegistry.get('reg-plan')?.(false)
    })
    expect(contentState()).toBe('closed')
    expect(seen).toEqual([false])
  })

  it('re-register with the same id overwrites: the newest registration wins', () => {
    renderUncontrolled('reg-dup')
    const second = document.createElement('div')
    document.body.appendChild(second)
    const secondRoot = createRoot(second)
    act(() => {
      secondRoot.render(
        <CollapsibleBlock label="second" revealId="reg-dup">
          body
        </CollapsibleBlock>,
      )
    })

    // The second registration must have replaced the first: a write reaches
    // the second block (collapse it) and, after the first block unmounts,
    // the registration must still resolve (it belongs to the second block).
    act(() => {
      collapsibleRegistry.get('reg-dup')?.(false)
    })
    const secondContent = second.querySelector('[data-slot="collapsible-content"]')
    expect(secondContent?.getAttribute('data-state')).toBe('closed')

    act(() => {
      root.unmount()
    })
    expect(collapsibleRegistry.get('reg-dup')).toBeTypeOf('function')

    // And once the second block unmounts too, the id is free again.
    act(() => {
      secondRoot.unmount()
    })
    second.remove()
    expect(collapsibleRegistry.get('reg-dup')).toBeUndefined()
  })
})
