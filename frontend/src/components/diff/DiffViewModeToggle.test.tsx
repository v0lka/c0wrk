// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { DiffViewModeToggle } from './DiffViewModeToggle'
import { useReviewStore } from '@/stores/reviewStore'

let container: HTMLDivElement
let root: Root

function renderToggle(className?: string) {
  act(() => {
    root.render(<DiffViewModeToggle className={className} />)
  })
}

beforeEach(() => {
  // reviewStore persists via localStorage — pin a clean state for each test.
  localStorage.clear()
  useReviewStore.setState({ diffViewMode: 'unified' })
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
  localStorage.clear()
})

describe('DiffViewModeToggle', () => {
  it('renders the shared segmented control with stable test ids', () => {
    renderToggle()
    const unified = container.querySelector('[data-testid="diff-view-unified"]') as HTMLButtonElement
    const split = container.querySelector('[data-testid="diff-view-split"]') as HTMLButtonElement
    expect(unified).toBeTruthy()
    expect(split).toBeTruthy()
    const group = container.querySelector('[aria-label="Diff view mode"]')
    expect(group).toBeTruthy()
    expect(group?.getAttribute('role')).toBe('tablist')
  })

  it('reflects the reviewStore mode as the active segment', () => {
    useReviewStore.setState({ diffViewMode: 'split' })
    renderToggle()
    expect(
      (container.querySelector('[data-testid="diff-view-split"]') as HTMLButtonElement).getAttribute('aria-selected'),
    ).toBe('true')
    expect(
      (container.querySelector('[data-testid="diff-view-unified"]') as HTMLButtonElement).getAttribute('aria-selected'),
    ).toBe('false')
  })

  it('clicking a segment writes the mode back to the shared reviewStore', () => {
    renderToggle()
    const split = container.querySelector('[data-testid="diff-view-split"]') as HTMLButtonElement
    act(() => {
      split.click()
    })
    expect(useReviewStore.getState().diffViewMode).toBe('split')
    act(() => {
      ;(container.querySelector('[data-testid="diff-view-unified"]') as HTMLButtonElement).click()
    })
    expect(useReviewStore.getState().diffViewMode).toBe('unified')
  })

  it('applies the className to the control track', () => {
    renderToggle('ml-auto')
    expect(container.querySelector('[aria-label="Diff view mode"]')?.className).toContain('ml-auto')
  })

  it('is shared state: a switch here is visible to review pages (same store field)', () => {
    renderToggle()
    act(() => {
      ;(container.querySelector('[data-testid="diff-view-split"]') as HTMLButtonElement).click()
    })
    // The store is a module singleton — ReviewPage's HunkReviewBlock reads
    // the exact same field, so no extra wiring is possible or needed.
    expect(useReviewStore.getState().diffViewMode).toBe('split')
  })
})
