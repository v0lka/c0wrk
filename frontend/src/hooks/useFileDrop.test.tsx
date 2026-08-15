// Tests for useFileDrop — native OS drag-and-drop → attachment staging.
//
// useFileDrop is isolated by mocking its two collaborators:
//   - useStageAttachments: we spy on stageAttachmentPaths to assert the hook
//     forwards dropped paths (the staging pipeline itself has its own tests in
//     useAttachmentsInput.test.tsx).
//   - @/api/runtime.onGlobalEvent: we capture subscriptions in a registry so a
//     test can emit the `files:dropped` event.
//
// The drag-highlight (dragActive) is driven by document-level HTML5 drag
// events; we synthesize those with a Files-carrying dataTransfer.
//
// No @testing-library/react in this repo; createRoot + jsdom pattern (see
// ModelCombobox.test.tsx).

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Registry capturing onGlobalEvent subscriptions so tests can emit events.
const { registry, stageSpy } = vi.hoisted(() => ({
  registry: new Map<string, Array<(data: unknown) => void>>(),
  stageSpy: vi.fn<(sessionId: string | null, paths: string[]) => Promise<void>>(),
}))

vi.mock('@/api/runtime', () => ({
  onGlobalEvent: (event: string, cb: (data: unknown) => void) => {
    const list = registry.get(event) ?? []
    list.push(cb)
    registry.set(event, list)
    return () => {
      const arr = registry.get(event) ?? []
      registry.set(event, arr.filter((c) => c !== cb))
    }
  },
}))

vi.mock('@/hooks/useStageAttachments', () => ({
  useStageAttachments: () => ({ stageAttachmentPaths: stageSpy }),
}))

import { useFileDrop } from '@/hooks/useFileDrop'
import { useInputModeStore } from '@/stores/inputModeStore'

/** Emit a captured global event to all current subscribers. */
function emitGlobal(event: string, data: unknown): void {
  for (const cb of registry.get(event) ?? []) cb(data)
}

/** Synthesize a document drag event carrying files (jsdom has no real
 *  DataTransfer, so we attach a minimal stand-in the hook's hasFiles accepts). */
function dispatchDragEvent(type: 'dragenter' | 'dragover' | 'dragleave' | 'drop'): void {
  const ev = new Event(type, { bubbles: true })
  Object.defineProperty(ev, 'dataTransfer', {
    value: { files: [{}], types: ['Files'] },
    configurable: true,
  })
  document.dispatchEvent(ev)
}

/** A drag event carrying NO files (e.g. a text drag) — must be ignored. */
function dispatchTextDragEvent(type: 'dragenter' | 'dragover'): void {
  const ev = new Event(type, { bubbles: true })
  Object.defineProperty(ev, 'dataTransfer', {
    value: { files: [], types: ['text/plain'] },
    configurable: true,
  })
  document.dispatchEvent(ev)
}

// Harness: capture the latest dragActive so a test can assert it.
let capturedDragActive = false
function Harness({ activeSessionId }: { activeSessionId: string | null }) {
  const { dragActive } = useFileDrop(activeSessionId)
  capturedDragActive = dragActive
  return null
}

let container: HTMLDivElement
let root: Root | null = null

beforeEach(() => {
  // Unmount any component left mounted by a previous test. Failing to do so
  // leaks live subscribers to the global mode store: a mode change in this
  // test would re-trigger their effects and accumulate subscriptions.
  act(() => { root?.unmount() })

  act(() => { useInputModeStore.setState({ mode: 'chat' }) })
  registry.clear()
  stageSpy.mockReset()
  capturedDragActive = false

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

/** Render the harness for the given session in chat mode. */
function mount(activeSessionId: string | null): void {
  act(() => {
    root?.render(<Harness activeSessionId={activeSessionId} />)
  })
}

describe('useFileDrop', () => {
  it('stages dropped file paths via stageAttachmentPaths (chat mode)', async () => {
    stageSpy.mockResolvedValue(undefined)
    mount('sess-1')

    await act(async () => {
      emitGlobal('files:dropped', { paths: ['/x/a.png', '/y/b.md'], x: 10, y: 20 })
    })

    expect(stageSpy).toHaveBeenCalledOnce()
    expect(stageSpy).toHaveBeenCalledWith('sess-1', ['/x/a.png', '/y/b.md'])
  })

  it('forwards a null session id (pipeline creates one on demand)', async () => {
    stageSpy.mockResolvedValue(undefined)
    mount(null)

    await act(async () => {
      emitGlobal('files:dropped', { paths: ['/x/a.md'], x: 0, y: 0 })
    })

    expect(stageSpy).toHaveBeenCalledWith(null, ['/x/a.md'])
  })

  it('ignores a malformed payload that fails the guard', async () => {
    mount('sess-1')

    await act(async () => {
      // Missing paths array — isFilesDroppedData rejects.
      emitGlobal('files:dropped', { x: 1, y: 2 })
    })

    expect(stageSpy).not.toHaveBeenCalled()
  })

  it('ignores an event with no paths', async () => {
    mount('sess-1')

    await act(async () => {
      emitGlobal('files:dropped', { paths: [], x: 5, y: 5 })
    })

    expect(stageSpy).not.toHaveBeenCalled()
  })

  it('is a no-op in terminal mode (never stages)', async () => {
    act(() => { useInputModeStore.setState({ mode: 'terminal' }) })
    mount('sess-1')

    await act(async () => {
      emitGlobal('files:dropped', { paths: ['/x/a.md'], x: 0, y: 0 })
    })

    expect(stageSpy).not.toHaveBeenCalled()
    // dragActive stays false in terminal mode even on dragenter.
    dispatchDragEvent('dragenter')
    expect(capturedDragActive).toBe(false)
  })

  it('re-subscribes and stages again after switching back to chat mode', async () => {
    stageSpy.mockResolvedValue(undefined)
    mount('sess-1')

    // Switch to terminal — the effect re-runs via the store subscription.
    act(() => { useInputModeStore.setState({ mode: 'terminal' }) })
    await act(async () => {
      emitGlobal('files:dropped', { paths: ['/x/a.md'], x: 0, y: 0 })
    })
    expect(stageSpy).not.toHaveBeenCalled()

    // Switch back to chat.
    act(() => { useInputModeStore.setState({ mode: 'chat' }) })
    await act(async () => {
      emitGlobal('files:dropped', { paths: ['/x/a.md'], x: 0, y: 0 })
    })
    expect(stageSpy).toHaveBeenCalledOnce()
    expect(stageSpy).toHaveBeenCalledWith('sess-1', ['/x/a.md'])
  })

  it('lights up dragActive on dragenter and hides on drop', () => {
    mount('sess-1')
    expect(capturedDragActive).toBe(false)

    act(() => { dispatchDragEvent('dragenter') })
    expect(capturedDragActive).toBe(true)

    act(() => { dispatchDragEvent('drop') })
    expect(capturedDragActive).toBe(false)
  })

  it('ref-counts nested dragenter/dragleave so it stays lit until leaving', () => {
    mount('sess-1')

    // Two enters (e.g. crossing into a child element) then one leave: still lit.
    act(() => { dispatchDragEvent('dragenter') })
    act(() => { dispatchDragEvent('dragenter') })
    act(() => { dispatchDragEvent('dragleave') })
    expect(capturedDragActive).toBe(true)

    // Second leave (cursor truly out): hides.
    act(() => { dispatchDragEvent('dragleave') })
    expect(capturedDragActive).toBe(false)
  })

  it('ignores drag events that carry no files', () => {
    mount('sess-1')
    act(() => { dispatchTextDragEvent('dragenter') })
    expect(capturedDragActive).toBe(false)
    // The webview default (file navigation) is still suppressed via
    // preventDefault only for file drags; non-file drags are ignored entirely.
  })

  it('unsubscribes on unmount (no staging after teardown)', async () => {
    mount('sess-1')
    act(() => { root?.unmount() })

    await act(async () => {
      emitGlobal('files:dropped', { paths: ['/x/a.md'], x: 0, y: 0 })
    })
    expect(stageSpy).not.toHaveBeenCalled()
  })
})
