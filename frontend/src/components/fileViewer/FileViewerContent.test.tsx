// @vitest-environment jsdom
// FileViewerContent — the experimental gate on the research viewer tab.
//
// The research pseudo-path (c0wrk:research) must render the ResearchWorkspace
// ONLY while the experimental-features switch is on. When the switch is off
// the pseudo-path renders nothing — a lingering tab (see the purge in
// ResearchEventBridge.test.tsx) stays inert instead of showing a still
// interactive workspace over frozen store data.

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

describe('FileViewerContent — research tab experimental gate', () => {
  beforeEach(() => {
    // The research pseudo-path active with no other tabs. The experimental
    // store is seeded loaded so useExperimentalFeatures never fetches.
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

  it('renders nothing for the research pseudo-path when the switch is off', () => {
    useExperimentalStore.setState({ enabled: false, loaded: true })
    renderContent()
    expect(document.querySelector('[data-testid="research-workspace"]')).toBeNull()
    expect(container!.innerHTML).toBe('')
  })
})
