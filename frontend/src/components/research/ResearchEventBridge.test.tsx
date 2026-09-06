// @vitest-environment jsdom
// ResearchEventBridge — the experimental-off purge of the research viewer
// tab. App mounts the bridge exactly while the experimental switch is on, so
// the bridge's UNMOUNT is the off-transition: an open research viewer tab
// must be closed there, regardless of whether the file viewer is currently
// mounted or collapsed (the viewer unmounts its content when collapsed, so
// the purge cannot live there).

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

describe('ResearchEventBridge — experimental-off purge', () => {
  beforeEach(() => {
    // No active project → the status hook's initial refresh() resets the
    // research store and issues no RPCs; the behavior under test is the
    // viewer purge alone.
    useProjectStore.setState({ activeProjectId: null })
  })

  it('closes an open research viewer tab on unmount (the off-transition)', async () => {
    useFileViewerStore.setState({
      openTabs: ['/ws/notes.md', RESEARCH_TAB_PATH],
      activeFile: RESEARCH_TAB_PATH,
      files: {},
    })

    const unmount = await mountBridge()
    expect(useFileViewerStore.getState().openTabs).toContain(RESEARCH_TAB_PATH)

    await unmount()

    const s = useFileViewerStore.getState()
    expect(s.openTabs).toEqual(['/ws/notes.md'])
    // The neighbor tab becomes active — the research tab is gone, not just hidden.
    expect(s.activeFile).toBe('/ws/notes.md')
  })

  it('leaves the viewer untouched on unmount when the research tab is not open', async () => {
    useFileViewerStore.setState({
      openTabs: ['/ws/a.md', '/ws/b.md'],
      activeFile: '/ws/b.md',
      files: {},
    })

    const unmount = await mountBridge()
    await unmount()

    const s = useFileViewerStore.getState()
    expect(s.openTabs).toEqual(['/ws/a.md', '/ws/b.md'])
    expect(s.activeFile).toBe('/ws/b.md')
  })
})
