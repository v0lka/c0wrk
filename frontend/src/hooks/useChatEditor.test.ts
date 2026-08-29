// Unit tests for the chat editor hook:
//  - shouldFastPathPaste — the fast-path heuristic that decides whether the
//    chat editor should let CodeMirror insert a paste natively (pure
//    text/html) or route it through the Go backend (image / copied files).
//    Pure function over a DataTransfer.
//  - onContentChange — the hook must report the FULL editor text (not a
//    boolean) on every document change, so the controller can persist it as
//    the active session's draft. Needs a real CodeMirror view in the DOM.
//
// jsdom environment: the onContentChange suite mounts a real CodeMirror
// EditorView (needs a DOM), and useThemeStore (imported via the hook)
// persists through window.localStorage.
// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { createElement } from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The editor's extension bundle pulls in the @-/#-autocomplete source, which
// imports the Wails api wrappers. Mock them so nothing reaches window.go.
vi.mock('@/api/workspace', () => ({ listDirectory: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/skills', () => ({ listSkills: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/agents', () => ({ listAgents: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/runtime', () => ({ subscribe: vi.fn(() => () => {}) }))

import { shouldFastPathPaste, useChatEditor, type ChatEditorAPI } from '@/hooks/useChatEditor'

/** Build a minimal DataTransfer-like object from a list of {kind, type} items. */
function fakeDataTransfer(items: Array<{ kind: 'string' | 'file'; type: string }>): DataTransfer {
  return {
    items: items as unknown as DataTransferItemList,
  } as DataTransfer
}

describe('shouldFastPathPaste', () => {
  it('fast-paths plain text (string item, text/plain)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([{ kind: 'string', type: 'text/plain' }]))).toBe(true)
  })

  it('fast-paths rich text (string items, text/plain + text/html, no file)', () => {
    const data = fakeDataTransfer([
      { kind: 'string', type: 'text/plain' },
      { kind: 'string', type: 'text/html' },
    ])
    expect(shouldFastPathPaste(data)).toBe(true)
  })

  it('fast-paths text/html only (rich text from a browser/AI chat)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([{ kind: 'string', type: 'text/html' }]))).toBe(true)
  })

  it('does NOT fast-path an image paste (file item, image/png)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([{ kind: 'file', type: 'image/png' }]))).toBe(false)
  })

  it('does NOT fast-path a screenshot that also carries a text/html shadow', () => {
    // Browsers often expose a screenshot as BOTH an image file and a text/html
    // representation; the file item must force the backend route.
    const data = fakeDataTransfer([
      { kind: 'string', type: 'text/html' },
      { kind: 'file', type: 'image/png' },
    ])
    expect(shouldFastPathPaste(data)).toBe(false)
  })

  it('does NOT fast-path a copied file (file item, generic/empty type)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([{ kind: 'file', type: '' }]))).toBe(false)
  })

  it('treats an empty clipboard as a fast path (nothing to route)', () => {
    expect(shouldFastPathPaste(fakeDataTransfer([]))).toBe(true)
  })
})

describe('useChatEditor onContentChange', () => {
  let container: HTMLDivElement
  let root: Root
  let captured: ChatEditorAPI | null = null
  let changes: string[]

  function Harness() {
    const editor = useChatEditor({
      disabled: false,
      placeholder: 'type…',
      onSend: () => {},
      onContentChange: (text: string) => {
        changes.push(text)
      },
    })
    captured = editor
    return createElement('div', { ref: editor.containerRef })
  }

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.innerHTML = ''
    captured = null
    changes = []
    vi.clearAllMocks()
  })

  function render() {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => {
      root.render(createElement(Harness))
    })
  }

  it('reports the FULL text (not a boolean) on every document change', () => {
    changes = []
    render()
    const editor = captured!
    // Mounting with an empty doc must not fire a content change.
    expect(changes).toEqual([])

    // Programmatic setText → full text.
    act(() => {
      editor.setText('hello')
    })
    expect(changes).toEqual(['hello'])
    expect(editor.getText()).toBe('hello')

    // insertAtCursor appends (unfocused → end of doc).
    act(() => {
      editor.insertAtCursor(' world')
    })
    expect(changes).toEqual(['hello', 'hello world'])

    // clear → empty string.
    act(() => {
      editor.clear()
    })
    expect(changes).toEqual(['hello', 'hello world', ''])
    expect(editor.getText()).toBe('')
  })

  it('does not fire onContentChange for non-document updates (focus)', () => {
    changes = []
    render()
    const editor = captured!
    editor.focus()
    expect(changes).toEqual([])
    expect(editor.getText()).toBe('')
  })
})
