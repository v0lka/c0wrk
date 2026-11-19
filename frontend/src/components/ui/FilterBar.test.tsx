// @vitest-environment jsdom
//
// Tests for the shared FilterBar: the optional leftSlot renders BEFORE the
// input (the file tree's open-workspace / open-temp buttons live there) and
// the existing rightSlot contract is unchanged — the git history tab's
// consumer must stay byte-identical.

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { FilterBar } from './FilterBar'

describe('FilterBar slots', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function renderBar(props: Partial<Parameters<typeof FilterBar>[0]> = {}): void {
    act(() => {
      root.render(
        <FilterBar value="" onChange={() => {}} mode="glob" onToggleMode={() => {}} {...props} />,
      )
    })
  }

  it('renders the input and the mode toggle without slots', () => {
    renderBar()
    expect(container.querySelector('input')).not.toBeNull()
    expect(container.querySelector('button')).not.toBeNull()
  })

  it('renders leftSlot before the input', () => {
    renderBar({ leftSlot: <button data-testid="left-button">L</button> })
    const left = container.querySelector('[data-testid="left-button"]')
    const input = container.querySelector('input')
    expect(left).not.toBeNull()
    expect(input).not.toBeNull()
    expect(left!.compareDocumentPosition(input!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('renders rightSlot after the mode toggle', () => {
    renderBar({ rightSlot: <button data-testid="right-button">R</button> })
    const right = container.querySelector('[data-testid="right-button"]')
    expect(right).not.toBeNull()
    // The right slot is the LAST element in the bar.
    expect(right!.parentElement!.lastElementChild).toBe(right)
  })
})
