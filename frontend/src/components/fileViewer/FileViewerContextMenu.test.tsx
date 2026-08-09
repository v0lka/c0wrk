// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  g.IS_REACT_ACT_ENVIRONMENT = true
})

// Mock the Zustand stores so the component's handlers reach into controlled
// fakes instead of the real persisted stores (which crash under jsdom when
// `window.localStorage` is undefined). Only the surface the component uses is
// surfaced; the hooks-based `useInputModeStore` is mocked below.
const vectorMock = vi.hoisted(() => ({
  setQuery: vi.fn(),
}))
const uiMock = vi.hoisted(() => ({
  setWorkspaceTab: vi.fn(),
}))
const inputMock = vi.hoisted(() => ({
  insertTextIntoInput: vi.fn(),
}))

vi.mock('@/stores/vectorIndexStore', () => ({
  useVectorIndexStore: { getState: () => vectorMock },
}))
vi.mock('@/stores/uiStore', () => ({
  useUIStore: { getState: () => uiMock },
}))
vi.mock('@/stores/inputModeStore', () => ({
  useInputModeStore: (selector: (s: { insertTextIntoInput: typeof inputMock.insertTextIntoInput }) => unknown) =>
    selector(inputMock),
}))

import { FileViewerContextMenu } from './FileViewerContextMenu'

describe('FileViewerContextMenu', () => {
  let container: HTMLElement
  let root: Root
  let onClose: ReturnType<typeof vi.fn>

  beforeEach(() => {
    onClose = vi.fn()
    vectorMock.setQuery.mockClear()
    uiMock.setWorkspaceTab.mockClear()
    inputMock.insertTextIntoInput.mockClear()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function renderMenu(
    reference: string,
    selectedText: string,
    position: { x: number; y: number } | null = { x: 10, y: 10 },
  ) {
    act(() => {
      root.render(
        <FileViewerContextMenu
          reference={reference}
          selectedText={selectedText}
          position={position}
          onClose={onClose as () => void}
        />,
      )
    })
  }

  function menuItem(label: string): HTMLButtonElement {
    const items = Array.from(container.querySelectorAll('[role="menuitem"]'))
    const match = items.find((b) => b.textContent?.trim() === label)
    if (!match) throw new Error(`menu item "${label}" not rendered`)
    return match as HTMLButtonElement
  }

  it('renders nothing when position is null', () => {
    renderMenu('@foo.go#L1', 'code', null)
    expect(container.querySelector('[role="menu"]')).toBeNull()
  })

  it('renders both Add to chat and Find similar items', () => {
    renderMenu('@foo.go#L1', 'code')
    expect(() => menuItem('Add to chat')).not.toThrow()
    expect(() => menuItem('Find similar')).not.toThrow()
  })

  it('Add to chat inserts the reference into the input', async () => {
    renderMenu('@foo.go#L1-5', 'code')
    await act(async () => {
      menuItem('Add to chat').click()
      await Promise.resolve()
    })
    expect(inputMock.insertTextIntoInput).toHaveBeenCalledTimes(1)
    expect(inputMock.insertTextIntoInput).toHaveBeenCalledWith('@foo.go#L1-5')
    expect(onClose).toHaveBeenCalled()
  })

  it('Find similar seeds the vector query and switches to the semantics tab', async () => {
    renderMenu('@foo.go#L1', '  func foo() {}  ')
    await act(async () => {
      menuItem('Find similar').click()
      await Promise.resolve()
    })
    // The selection is trimmed before seeding the query.
    expect(vectorMock.setQuery).toHaveBeenCalledTimes(1)
    expect(vectorMock.setQuery).toHaveBeenCalledWith('func foo() {}')
    expect(uiMock.setWorkspaceTab).toHaveBeenCalledWith('semantics')
    expect(onClose).toHaveBeenCalled()
  })

  it('Find similar is a no-op when the selection is blank', async () => {
    renderMenu('@foo.go#L1', '   ')
    await act(async () => {
      menuItem('Find similar').click()
      await Promise.resolve()
    })
    expect(vectorMock.setQuery).not.toHaveBeenCalled()
    expect(uiMock.setWorkspaceTab).not.toHaveBeenCalled()
    // Still closes the menu.
    expect(onClose).toHaveBeenCalled()
  })
})
