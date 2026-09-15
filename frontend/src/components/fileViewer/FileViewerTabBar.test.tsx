// @vitest-environment jsdom
// FileViewerTabBar — the research pseudo-path tab icon + middle-click close.
//
// The c0wrk:research tab renders its FlaskConical icon DIRECTLY at the call
// site: TabFileIcon (which runs useFileIcon) must never execute for the
// pseudo-path — it would fire a getFileIcon RPC for a non-file path and
// persist a junk fileIcons entry keyed by it.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { getFileIconMock } = vi.hoisted(() => ({
  getFileIconMock: vi.fn<(path: string) => Promise<{ icon: string; icon_color: string }>>(),
}))
vi.mock('@/api/workspace', () => ({ getFileIcon: getFileIconMock }))

import { FileViewerTabBar } from './FileViewerTabBar'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { RESEARCH_TAB_PATH } from '@/stores/researchStore'

let root: Root | null = null
let container: HTMLDivElement | null = null

// jsdom in this setup exposes no global `CSS`, but the component's
// scrollToTab relies on CSS.escape (baseline in every real browser/webview
// since 2016). Polyfill a minimal escaping version for the test environment.
{
  const g = globalThis as { CSS?: { escape?: (v: string) => string } }
  if (!g.CSS?.escape) {
    g.CSS = { ...g.CSS, escape: (v: string) => v.replace(/["\\\]]/g, '\\$&') }
  }
  // jsdom implements no scrolling — scrollToTab calls scrollIntoView on
  // activation, which jsdom elements lack. A no-op keeps the click path
  // testable without asserting scroll behaviour (jsdom has no layout anyway).
  const proto = Element.prototype as Element & { scrollIntoView?: () => void }
  if (!proto.scrollIntoView) {
    proto.scrollIntoView = () => {}
  }
}

function renderBar(): Promise<void> {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  return act(async () => {
    root!.render(<FileViewerTabBar onToggleCollapse={() => {}} />)
    // Flush the getFileIcon promise for real-file tabs: its .then updates
    // the store + local state, and those updates must land inside act(...)
    // or React logs "not wrapped in act(...)" warnings to stderr.
    await Promise.resolve()
  })
}

/** Dispatch a MouseEvent on `el` inside act(...) — jsdom's dispatchEvent is
 *  synchronous, but React state updates it triggers must still be flushed. */
function fireMouse(el: Element, type: 'click' | 'auxclick', button: number): void {
  act(() => {
    el.dispatchEvent(
      new MouseEvent(type, { bubbles: true, cancelable: true, button, composed: true }),
    )
  })
}

function tabButton(path: string): HTMLButtonElement {
  // Attribute-iteration instead of CSS.escape: jsdom in this setup exposes
  // no global `CSS`, so escaping the path for a querySelector is not
  // possible. Comparing the raw attribute value is exact and escape-free.
  const el = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-file-path]')).find(
    (b) => b.dataset.filePath === path,
  )
  if (!el) throw new Error(`tab not found: ${path}`)
  return el
}

describe('FileViewerTabBar — research pseudo-path icon', () => {
  beforeEach(() => {
    getFileIconMock.mockReset().mockResolvedValue({ icon: 'go', icon_color: '#00ADD8' })
    useFileViewerStore.setState({
      openTabs: [RESEARCH_TAB_PATH, '/ws/main.go'],
      activeFile: RESEARCH_TAB_PATH,
      files: {},
      fileIcons: {},
    })
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    container?.remove()
    root = null
    container = null
  })

  it('renders the flask icon for the research tab without any getFileIcon RPC for it', async () => {
    await renderBar()

    // The research tab shows the flask (success-tinted svg)…
    expect(document.querySelector('.text-success')).not.toBeNull()
    // …the icon RPC fires only for the real file tab — never for the
    // pseudo-path…
    expect(getFileIconMock).not.toHaveBeenCalledWith(RESEARCH_TAB_PATH)
    expect(getFileIconMock).toHaveBeenCalledWith('/ws/main.go')
    // …and nothing is persisted under the pseudo-path key.
    expect(useFileViewerStore.getState().fileIcons[RESEARCH_TAB_PATH]).toBeUndefined()
  })

  it('skips the icon RPC for any c0wrk:-prefixed pseudo-path (hook-level prefix guard)', async () => {
    // A hypothetical future pseudo-tab: the tab bar special-cases only
    // RESEARCH_TAB_PATH at the call site, so this tab DOES run TabFileIcon
    // → useFileIcon. The hook's prefix-level guard must still prevent both
    // the getFileIcon RPC and the junk fileIcons cache entry.
    const futureTab = 'c0wrk:some-future-tab'
    useFileViewerStore.setState({
      openTabs: [futureTab, '/ws/main.go'],
      activeFile: futureTab,
    })
    await renderBar()

    expect(getFileIconMock).toHaveBeenCalledTimes(1)
    expect(getFileIconMock).toHaveBeenCalledWith('/ws/main.go')
    expect(getFileIconMock).not.toHaveBeenCalledWith(futureTab)
    expect(useFileViewerStore.getState().fileIcons[futureTab]).toBeUndefined()
  })
})

describe('FileViewerTabBar — middle-click closes the tab', () => {
  beforeEach(() => {
    getFileIconMock.mockReset().mockResolvedValue({ icon: 'go', icon_color: '#00ADD8' })
    useFileViewerStore.setState({
      openTabs: ['/ws/active.go', '/ws/inactive.go', '/ws/other.go'],
      activeFile: '/ws/active.go',
      files: {},
      fileIcons: {},
    })
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    container?.remove()
    root = null
    container = null
  })

  it('middle-click on an inactive tab closes it without changing the active tab', async () => {
    await renderBar()

    const tab = tabButton('/ws/inactive.go')
    fireMouse(tab, 'auxclick', 1)

    const s = useFileViewerStore.getState()
    expect(s.openTabs).toEqual(['/ws/active.go', '/ws/other.go'])
    // The active tab is untouched — middle-click must not activate the tab.
    expect(s.activeFile).toBe('/ws/active.go')
  })

  it('middle-click on the active tab closes it and activates the neighbour', async () => {
    await renderBar()

    const tab = tabButton('/ws/active.go')
    fireMouse(tab, 'auxclick', 1)

    const s = useFileViewerStore.getState()
    expect(s.openTabs).toEqual(['/ws/inactive.go', '/ws/other.go'])
    // closeFile's neighbour semantics: closing the active tab activates
    // the tab that took its index.
    expect(s.activeFile).toBe('/ws/inactive.go')
  })

  it('middle-click suppresses the default middle-button behaviour (preventDefault)', async () => {
    await renderBar()

    const tab = tabButton('/ws/inactive.go')
    const ev = new MouseEvent('auxclick', { bubbles: true, cancelable: true, button: 1, composed: true })
    act(() => {
      const notDefaultPrevented = tab.dispatchEvent(ev)
      // cancelable + preventDefault in the handler → dispatchEvent reports
      // that the default was prevented.
      expect(notDefaultPrevented).toBe(false)
    })

    expect(useFileViewerStore.getState().openTabs).toEqual(['/ws/active.go', '/ws/other.go'])
  })

  it('left click still activates the tab (no regression in the click path)', async () => {
    await renderBar()

    const tab = tabButton('/ws/other.go')
    fireMouse(tab, 'click', 0)

    expect(useFileViewerStore.getState().activeFile).toBe('/ws/other.go')
  })

  it('auxclick with a non-middle button does not close the tab', async () => {
    await renderBar()

    const tab = tabButton('/ws/inactive.go')
    // button 2 = right button; auxclick fires, but the handler must ignore it
    // (right-click keeps its dedicated context-menu path).
    fireMouse(tab, 'auxclick', 2)

    expect(useFileViewerStore.getState().openTabs).toHaveLength(3)
  })
})
