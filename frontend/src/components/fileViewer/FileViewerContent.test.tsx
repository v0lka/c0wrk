// @vitest-environment jsdom
// FileViewerContent — the research viewer tab is always available.
//
// The research pseudo-path (c0wrk:research) renders the ResearchWorkspace
// unconditionally — RESEARCH is not gated on the experimental-features
// switch (which now controls only the Small-LLM profile), so the workspace
// must render even while that switch is off.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/hooks/useFileViewerData', () => ({ useFileViewerData: vi.fn() }))
vi.mock('@/components/research/ResearchWorkspace', () => ({
  ResearchWorkspace: () => <div data-testid="research-workspace" />,
}))

import { FileViewerContent } from './FileViewerContent'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { RESEARCH_TAB_PATH } from '@/stores/researchStore'
import { useExperimentalStore } from '@/stores/experimentalStore'

let root: Root | null = null
let container: HTMLDivElement | null = null

function renderContent(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<FileViewerContent />)
  })
}

describe('FileViewerContent — research tab always available', () => {
  beforeEach(() => {
    // The research pseudo-path active with no other tabs. The experimental
    // store is seeded explicitly by each test to pin the scenario.
    useFileViewerStore.setState({
      openTabs: [RESEARCH_TAB_PATH],
      activeFile: RESEARCH_TAB_PATH,
      files: {},
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

  it('renders the research workspace while the experimental switch is on', () => {
    useExperimentalStore.setState({ enabled: true, loaded: true })
    renderContent()
    expect(document.querySelector('[data-testid="research-workspace"]')).not.toBeNull()
  })

  it('renders the research workspace even with the experimental switch off', () => {
    useExperimentalStore.setState({ enabled: false, loaded: true })
    renderContent()
    expect(document.querySelector('[data-testid="research-workspace"]')).not.toBeNull()
  })
})
