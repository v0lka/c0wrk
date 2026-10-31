// Reading-position memory for the file viewer (CodeMirrorFileViewer).
//
// The floating viewer auto-collapses when focus/pointer lands outside it
// (AppLayout's outside handler), which UNMOUNTS the content — the CodeMirror
// view dies and its scroll offset with it. These tests pin the fix: the
// position is saved into fileViewerStore.scrollPositions while the view is
// still alive (unmount / tab re-target) and restored on the next mount /
// activation, mirroring ChatScrollManager + chatStore for the chat area.
//
// jsdom environment: mounts a real CodeMirror EditorView (Range geometry
// polyfills come from src/test/setup.ts); scroll geometry is pinned at the
// Element-prototype level like ChatScrollManager.test.tsx does. The real
// fileViewerStore is used — the store contract is half of the feature.

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { EditorView } from '@codemirror/view'

import { CodeMirrorFileViewer } from './CodeMirrorFileViewer'
import { useFileViewerStore } from '@/stores/fileViewerStore'

vi.mock('@/hooks/useWorkspacePath', () => ({
  useWorkspacePath: () => '/ws',
}))

vi.mock('@/lib/cmLanguages', () => ({
  loadLanguageByName: async () => null,
}))

vi.mock('@/components/fileViewer/FileViewerContextMenu', () => ({
  FileViewerContextMenu: () => null,
}))

// The Markdown preview stub keeps the test light: the preview pane binds its
// scroll listener to Markdown's rendered ROOT (first element child of the
// wrapper), so any single-root element reproduces the contract.
vi.mock('@/lib/markdownConfig', () => ({
  Markdown: ({ content, className }: { content: string; className?: string }) => (
    <div data-testid="markdown-root" className={className}>{content}</div>
  ),
}))

const LONG_TEXT = Array.from({ length: 200 }, (_, i) => `line ${i + 1}`).join('\n')

let activeRoot: Root | null = null

// jsdom computes no layout, so the scroll geometry is pinned at the prototype
// level (restored by afterEach's vi.restoreAllMocks). Tests re-pin between the
// save and the restore phase to exercise the proportional-scale path.
const pinGetter = (prop: 'scrollHeight' | 'clientHeight') =>
  vi.spyOn(window.Element.prototype, prop, 'get').mockReturnValue(0)
let scrollHeightSpy: ReturnType<typeof pinGetter>
let clientHeightSpy: ReturnType<typeof pinGetter>

function pinGeometry(scrollHeight: number, clientHeight: number) {
  scrollHeightSpy.mockReturnValue(scrollHeight)
  clientHeightSpy.mockReturnValue(clientHeight)
}

async function renderViewer(props: {
  content?: string
  language?: string
  diff?: string
  highlightLine?: number | null
} = {}): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(
      <CodeMirrorFileViewer
        content={props.content ?? LONG_TEXT}
        language={props.language ?? 'typescript'}
        diff={props.diff}
        highlightLine={props.highlightLine ?? null}
      />,
    )
  })
  return container
}

function viewOf(container: HTMLElement): EditorView {
  const view = EditorView.findFromDOM(container)
  if (!view) throw new Error('EditorView not found in container')
  return view
}

function primeStore(path: string): void {
  useFileViewerStore.setState({
    openTabs: [path],
    activeFile: path,
    files: { [path]: { content: LONG_TEXT, loading: false } },
    scrollPositions: {},
  })
}

async function unmountViewer(): Promise<void> {
  await act(async () => {
    activeRoot!.unmount()
  })
  activeRoot = null
}

beforeEach(() => {
  useFileViewerStore.setState({
    files: {},
    openTabs: [],
    activeFile: null,
    scrollPositions: {},
  })
  scrollHeightSpy = pinGetter('scrollHeight').mockReturnValue(2000)
  clientHeightSpy = pinGetter('clientHeight').mockReturnValue(600)
})

afterEach(async () => {
  if (activeRoot) {
    await unmountViewer()
  }
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

describe('CodeMirrorFileViewer — reading positions', () => {
  it('saves the position on unmount and restores it on remount (the collapse/resume scenario)', async () => {
    primeStore('/f.ts')
    const container = await renderViewer()
    const view = viewOf(container)
    expect(view.scrollDOM.scrollTop).toBe(0)

    // The user scrolled mid-file, then the floating viewer collapsed — the
    // content unmounts and the EditorView dies with it.
    act(() => {
      view.scrollDOM.scrollTop = 500
      view.scrollDOM.dispatchEvent(new Event('scroll'))
    })
    // Like the chat's reading positions, nothing is written until the loss
    // point (unmount).
    expect(useFileViewerStore.getState().scrollPositions['/f.ts']).toBeUndefined()

    await unmountViewer()
    expect(useFileViewerStore.getState().scrollPositions['/f.ts'])
      .toEqual({ scrollTop: 500, scrollHeight: 2000 })

    // Re-expanding mounts a fresh view: it must resume where reading stopped,
    // not jump back to the top.
    const remounted = await renderViewer()
    expect(viewOf(remounted).scrollDOM.scrollTop).toBe(500)
  })

  it('keeps one position per path across re-targets of the reused view', async () => {
    primeStore('/a.ts')
    const container = await renderViewer()
    const view = viewOf(container)

    act(() => {
      view.scrollDOM.scrollTop = 300
      view.scrollDOM.dispatchEvent(new Event('scroll'))
    })

    // Switch to /b.ts: the single EditorView is re-targeted (no remount), so
    // the outgoing path's position must be saved first.
    await act(async () => {
      useFileViewerStore.setState({
        activeFile: '/b.ts',
        openTabs: ['/a.ts', '/b.ts'],
        files: {
          '/a.ts': { content: LONG_TEXT, loading: false },
          '/b.ts': { content: LONG_TEXT, loading: false },
        },
      })
    })
    expect(useFileViewerStore.getState().scrollPositions['/a.ts'])
      .toEqual({ scrollTop: 300, scrollHeight: 2000 })

    // Read further down on /b.ts, then switch back: /a's offset must come
    // back in the live view, and /b's must survive the final unmount.
    act(() => {
      view.scrollDOM.scrollTop = 900
      view.scrollDOM.dispatchEvent(new Event('scroll'))
    })
    await act(async () => {
      useFileViewerStore.setState({ activeFile: '/a.ts' })
    })
    expect(view.scrollDOM.scrollTop).toBe(300)

    await unmountViewer()
    expect(useFileViewerStore.getState().scrollPositions['/b.ts'])
      .toEqual({ scrollTop: 900, scrollHeight: 2000 })
    expect(useFileViewerStore.getState().scrollPositions['/a.ts'])
      .toEqual({ scrollTop: 300, scrollHeight: 2000 })
  })

  it('scales the restored offset when the content height changed between save and restore', async () => {
    primeStore('/f.ts')
    const container = await renderViewer()
    pinGeometry(1000, 600)
    act(() => {
      const view = viewOf(container)
      view.scrollDOM.scrollTop = 250
      view.scrollDOM.dispatchEvent(new Event('scroll'))
    })
    await unmountViewer()
    expect(useFileViewerStore.getState().scrollPositions['/f.ts'])
      .toEqual({ scrollTop: 250, scrollHeight: 1000 })

    // The file grew while the viewer was collapsed: a quarter of the old
    // height stays a quarter of the new one.
    pinGeometry(2000, 600)
    const remounted = await renderViewer()
    expect(viewOf(remounted).scrollDOM.scrollTop).toBe(500)
  })

  it('applies the restore before the highlightLine scroll (open-at-line still wins)', async () => {
    primeStore('/f.ts')
    useFileViewerStore.getState().saveScrollPosition('/f.ts', { scrollTop: 500, scrollHeight: 2000 })

    // Effects run in definition order: the mount effect restores first, the
    // highlight effect (defined later) may re-scroll after — an explicit
    // open-at-line must never land BEFORE the restore.
    const container = await renderViewer({ highlightLine: 10 })
    expect(viewOf(container).scrollDOM.scrollTop).toBe(500)
  })

  it('saves and restores the markdown preview scroll across unmount/remount', async () => {
    primeStore('/f.md')
    const container = await renderViewer({ language: 'Markdown' })
    const preview = container.querySelector('[data-testid="markdown-root"]')
    expect(preview).not.toBeNull()

    pinGeometry(1500, 600)
    act(() => {
      preview!.scrollTop = 700
      preview!.dispatchEvent(new Event('scroll'))
    })
    expect(useFileViewerStore.getState().scrollPositions['/f.md']).toBeUndefined()

    await unmountViewer()
    expect(useFileViewerStore.getState().scrollPositions['/f.md'])
      .toEqual({ scrollTop: 700, scrollHeight: 1500 })

    const remounted = await renderViewer({ language: 'Markdown' })
    const preview2 = remounted.querySelector('[data-testid="markdown-root"]')
    expect(preview2!.scrollTop).toBe(700)
  })
})
