// @vitest-environment jsdom
// Unit tests for BaseSelectorRow's native-tooltip contract: the row button
// carries the commit subject when one exists, else a type-prefixed ref name;
// the detail span mirrors its own subject; no Radix tooltip is mounted (two
// native titles never stack — the innermost wins — unlike the former
// Radix-inside-titled-button shape that raised both popups at once).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { BaseSelectorRow } from './BaseSelectorRow'
import type { BranchBase } from '@/types/models'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

function renderRow(base: BranchBase, selected = false): HTMLButtonElement {
  act(() => {
    root.render(
      <BaseSelectorRow
        base={base}
        selected={selected}
        isCurrent={false}
        onSelect={() => {}}
      />,
    )
  })
  return container.querySelector('button')!
}

describe('BaseSelectorRow native tooltips', () => {
  it('titles the row with the commit subject when a detail exists', () => {
    const btn = renderRow({
      ref: 'a3f5c1d',
      label: 'a3f5c1d',
      type: 'commit',
      detail: 'fix: login bug',
    })

    expect(btn.title).toBe('fix: login bug')
  })

  it('falls back to the type-prefixed ref name when there is no detail', () => {
    expect(
      renderRow({ ref: 'develop', label: 'develop', type: 'local', detail: '' }).title,
    ).toBe('Branch develop')
    expect(
      renderRow({ ref: 'origin/main', label: 'origin/main', type: 'remote', detail: '' }).title,
    ).toBe('Remote origin/main')
    expect(
      renderRow({ ref: 'v1.0', label: 'v1.0', type: 'tag', detail: '' }).title,
    ).toBe('Tag v1.0')
    expect(
      renderRow({ ref: 'a3f5c1d', label: 'a3f5c1d', type: 'commit', detail: '' }).title,
    ).toBe('Commit a3f5c1d')
  })

  it('mirrors the subject onto the detail span and mounts no Radix trigger', () => {
    const btn = renderRow({
      ref: 'a3f5c1d',
      label: 'a3f5c1d',
      type: 'commit',
      detail: 'fix: login bug',
    })

    const detail = btn.querySelector('span[title="fix: login bug"]')
    expect(detail).not.toBeNull()
    expect(detail?.textContent).toBe('fix: login bug')
    // Radix TooltipTrigger would stamp data-slot="tooltip-trigger" via asChild.
    expect(container.querySelector('[data-slot="tooltip-trigger"]')).toBeNull()
  })

  it('omits the detail span entirely when there is no detail', () => {
    const btn = renderRow({ ref: 'develop', label: 'develop', type: 'local', detail: '' })
    expect(btn.querySelectorAll('span[title]')).toHaveLength(0)
  })

  it('calls onSelect with the ref on click', async () => {
    const onSelect = vi.fn()
    act(() => {
      root.render(
        <BaseSelectorRow
          base={{ ref: 'v1.0', label: 'v1.0', type: 'tag', detail: '' }}
          selected={false}
          isCurrent={false}
          onSelect={onSelect}
        />,
      )
    })
    await act(async () => {
      container
        .querySelector('button')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onSelect).toHaveBeenCalledWith('v1.0')
  })
})
