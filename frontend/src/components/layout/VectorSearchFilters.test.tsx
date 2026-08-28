// Readiness gating for VectorSearchFilters: while the vector index is not
// ready, searches would block on index readiness — the query/file-pattern
// inputs and the search button are disabled at the widget level. Uses the
// real useVectorIndexStore (its in-memory storage fallback is jsdom-safe) so
// the selector wiring is exercised, not mocked.

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { VectorSearchFilters } from '@/components/layout/VectorSearchFilters'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import type { VectorIndexStatus } from '@/types/models'

const statusWith = (state: VectorIndexStatus['state']): VectorIndexStatus => ({
  state,
  progress: 0,
  files_indexed: 0,
  total_files: 0,
})

let container: HTMLElement
let root: Root
let onSearch: ReturnType<typeof vi.fn>
let onClear: ReturnType<typeof vi.fn>
let onKeyDown: ReturnType<typeof vi.fn>

beforeEach(() => {
  act(() => {
    useVectorIndexStore.getState().reset()
  })
  onSearch = vi.fn()
  onClear = vi.fn()
  onKeyDown = vi.fn()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  document.body.replaceChildren()
})

function renderFilters(isSearchMode = false) {
  act(() => {
    root.render(
      <VectorSearchFilters
        isSearchMode={isSearchMode}
        onSearch={onSearch as () => void}
        onClear={onClear as () => void}
        onKeyDown={onKeyDown as (e: React.KeyboardEvent) => void}
      />,
    )
  })
}

/** DOM-order inputs: query, topK, file pattern. */
function inputs(): HTMLInputElement[] {
  return Array.from(container.querySelectorAll('input'))
}

/** The input at DOM position 0..2, failing loudly if not rendered. */
function inputAt(i: number): HTMLInputElement {
  const el = inputs()[i]
  if (!el) throw new Error(`input #${i} not rendered`)
  return el
}

/** The icon-only search button (lucide puts `lucide-search` on its svg). */
function searchButton(): HTMLButtonElement {
  const btn = container.querySelector('button svg.lucide-search')?.closest('button')
  if (!btn) throw new Error('search button not rendered')
  return btn as HTMLButtonElement
}

/** Set an input's value the way a real keystroke would (React-controlled). */
function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!
  act(() => {
    setter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('VectorSearchFilters readiness gating', () => {
  it('disables the query and file-pattern inputs plus the search button while indexing', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('indexing') })
    })
    renderFilters()
    expect(inputAt(0).disabled).toBe(true)
    expect(inputAt(2).disabled).toBe(true)
    expect(searchButton().disabled).toBe(true)
  })

  it('disables the widgets for every non-ready state', () => {
    const nonReady = ['idle', 'reindexing', 'unavailable'] as const
    for (const state of nonReady) {
      act(() => {
        useVectorIndexStore.setState({ status: statusWith(state) })
      })
      renderFilters()
      expect(inputAt(0).disabled, state).toBe(true)
      expect(inputAt(2).disabled, state).toBe(true)
      expect(searchButton().disabled, state).toBe(true)
    }
  })

  it('keeps the widgets enabled when the index is ready', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready') })
    })
    renderFilters()
    expect(inputAt(0).disabled).toBe(false)
    expect(inputAt(2).disabled).toBe(false)
    expect(searchButton().disabled).toBe(false)
  })

  it('typing in the query input updates the store when ready (behavior unchanged)', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready') })
    })
    renderFilters()
    setInputValue(inputAt(0), 'handler')
    expect(useVectorIndexStore.getState().query).toBe('handler')
  })

  it('Enter in the query input is forwarded to the parent handler when ready', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready') })
    })
    renderFilters()
    inputAt(0).dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onKeyDown).toHaveBeenCalledTimes(1)
    expect(onKeyDown.mock.calls[0]?.[0]).toMatchObject({ key: 'Enter' })
  })

  it('clicking the search button triggers onSearch when ready', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready') })
    })
    renderFilters()
    act(() => {
      searchButton().click()
    })
    expect(onSearch).toHaveBeenCalledTimes(1)
  })

  it('keeps the search button disabled while a search is loading even when ready', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready'), isLoading: true })
    })
    renderFilters()
    expect(searchButton().disabled).toBe(true)
  })
})
