// @vitest-environment jsdom
// FileViewerContent — the research viewer tab is always available.
//
// The research pseudo-path (c0wrk:research) renders the ResearchWorkspace
// unconditionally — RESEARCH is not gated on the experimental-features
// switch (which now controls only the E2S execution mode), so the workspace
// must render even while that switch is off.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/hooks/useFileViewerData', () => ({ useFileViewerData: vi.fn() }))
vi.mock('@/components/research/ResearchWorkspace', () => ({
  ResearchWorkspace: () => <div data-testid="research-workspace" />,
}))
vi.mock('@/components/papers/PaperWorkspace', () => ({
  PaperWorkspace: ({ slug }: { slug: string }) => (
    <div data-testid="paper-workspace" data-slug={slug} />
  ),
}))
vi.mock('@/components/fileViewer/ImageFileViewer', () => ({
  ImageFileViewer: ({ dataUrl, path }: { dataUrl: string; path: string }) => (
    <div data-testid="image-viewer" data-src={dataUrl} data-path={path} />
  ),
}))

import { FileViewerContent } from './FileViewerContent'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { RESEARCH_TAB_PATH } from '@/stores/researchStore'
import { PAPER_TAB_PREFIX } from '@/stores/paperStore'
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

describe('FileViewerContent — paper tab routing', () => {
  it('routes the c0wrk:paper:<slug> prefix to the paper workspace with the slug', () => {
    const path = `${PAPER_TAB_PREFIX}vaswani-2017-attention`
    act(() => {
      useFileViewerStore.setState({ openTabs: [path], activeFile: path, files: {} })
    })
    renderContent()
    const el = document.querySelector('[data-testid="paper-workspace"]')
    expect(el).not.toBeNull()
    expect(el?.getAttribute('data-slug')).toBe('vaswani-2017-attention')
  })

  it('renders nothing for a bare paper prefix (empty slug)', () => {
    act(() => {
      useFileViewerStore.setState({
        openTabs: [PAPER_TAB_PREFIX],
        activeFile: PAPER_TAB_PREFIX,
        files: {},
      })
    })
    renderContent()
    expect(document.querySelector('[data-testid="paper-workspace"]')).toBeNull()
  })
})

describe('FileViewerContent — image routing', () => {
  const imagePath = '/ws/assets/plot.png'

  it('routes an image path to the image viewer with the data URL', () => {
    act(() => {
      useFileViewerStore.setState({
        openTabs: [imagePath],
        activeFile: imagePath,
        files: {
          [imagePath]: {
            content: '',
            loading: false,
            imageDataUrl: 'data:image/png;base64,AAA',
          },
        },
      })
    })
    renderContent()
    const el = document.querySelector('[data-testid="image-viewer"]')
    expect(el).not.toBeNull()
    expect(el?.getAttribute('data-src')).toBe('data:image/png;base64,AAA')
    expect(el?.getAttribute('data-path')).toBe(imagePath)
  })

  it('shows the unsupported-format notice for an image path lacking a data URL', () => {
    act(() => {
      useFileViewerStore.setState({
        openTabs: [imagePath],
        activeFile: imagePath,
        files: { [imagePath]: { content: '', loading: false } },
      })
    })
    renderContent()
    expect(document.querySelector('[data-testid="image-viewer"]')).toBeNull()
    expect(document.body.textContent).toContain('Unsupported file format')
  })
})
