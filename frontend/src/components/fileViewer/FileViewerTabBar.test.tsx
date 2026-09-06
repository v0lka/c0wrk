// @vitest-environment jsdom
// FileViewerTabBar — the research pseudo-path tab icon.
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

function renderBar(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<FileViewerTabBar onToggleCollapse={() => {}} />)
  })
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

  it('renders the flask icon for the research tab without any getFileIcon RPC for it', () => {
    renderBar()

    // The research tab shows the flask (success-tinted svg)…
    expect(document.querySelector('.text-success')).not.toBeNull()
    // …the icon RPC fires only for the real file tab — never for the
    // pseudo-path…
    expect(getFileIconMock).not.toHaveBeenCalledWith(RESEARCH_TAB_PATH)
    expect(getFileIconMock).toHaveBeenCalledWith('/ws/main.go')
    // …and nothing is persisted under the pseudo-path key.
    expect(useFileViewerStore.getState().fileIcons[RESEARCH_TAB_PATH]).toBeUndefined()
  })

  it('skips the icon RPC for any c0wrk:-prefixed pseudo-path (hook-level prefix guard)', () => {
    // A hypothetical future pseudo-tab: the tab bar special-cases only
    // RESEARCH_TAB_PATH at the call site, so this tab DOES run TabFileIcon
    // → useFileIcon. The hook's prefix-level guard must still prevent both
    // the getFileIcon RPC and the junk fileIcons cache entry.
    const futureTab = 'c0wrk:some-future-tab'
    useFileViewerStore.setState({
      openTabs: [futureTab, '/ws/main.go'],
      activeFile: futureTab,
    })
    renderBar()

    expect(getFileIconMock).toHaveBeenCalledTimes(1)
    expect(getFileIconMock).toHaveBeenCalledWith('/ws/main.go')
    expect(getFileIconMock).not.toHaveBeenCalledWith(futureTab)
    expect(useFileViewerStore.getState().fileIcons[futureTab]).toBeUndefined()
  })
})
