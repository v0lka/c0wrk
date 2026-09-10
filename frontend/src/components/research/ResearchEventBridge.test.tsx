// @vitest-environment jsdom
// ResearchEventBridge — pure store-sync bridge semantics.
//
// App mounts the bridge unconditionally (RESEARCH is always available), so an
// unmount is never an "experimental off" transition. The bridge must be a
// pure side-effect host: it renders no DOM and never mutates the file viewer
// — in particular it must NOT close an open research tab on unmount.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/api/runtime', () => ({
  subscribe: (_name: string, _cb: (...data: unknown[]) => void) => () => {},
}))
vi.mock('@/api/research', () => ({
  getResearchStatus: vi.fn(),
  getResearchGraph: vi.fn(),
  getResearchNextStep: vi.fn(),
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { ResearchEventBridge } from './ResearchEventBridge'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { RESEARCH_TAB_PATH } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'

async function mountBridge(): Promise<() => Promise<void>> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root: Root = createRoot(container)
  await act(async () => {
    root.render(<ResearchEventBridge />)
  })
  return async () => {
    await act(async () => {
      root.unmount()
    })
    container.remove()
  }
}

describe('ResearchEventBridge — pure store sync', () => {
  beforeEach(() => {
    // No active project → the status hook's initial refresh() resets the
    // research store and issues no RPCs; the behavior under test is the
    // bridge's own (non-)interaction with the viewer.
    useProjectStore.setState({ activeProjectId: null })
  })

  it('renders no DOM', async () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root: Root = createRoot(container)
    await act(async () => {
      root.render(<ResearchEventBridge />)
    })
    expect(container.innerHTML).toBe('')
    await act(async () => {
      root.unmount()
    })
    container.remove()
  })

  it('leaves an open research viewer tab in place on unmount', async () => {
    useFileViewerStore.setState({
      openTabs: ['/ws/notes.md', RESEARCH_TAB_PATH],
      activeFile: RESEARCH_TAB_PATH,
      files: {},
    })

    const unmount = await mountBridge()
    expect(useFileViewerStore.getState().openTabs).toContain(RESEARCH_TAB_PATH)

    await unmount()

    const s = useFileViewerStore.getState()
    expect(s.openTabs).toEqual(['/ws/notes.md', RESEARCH_TAB_PATH])
    expect(s.activeFile).toBe(RESEARCH_TAB_PATH)
  })
})
