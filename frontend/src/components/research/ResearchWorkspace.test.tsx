// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchWorkspace } from './ResearchWorkspace'
import { useResearchStore } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import {
  getResearchStatus,
  getResearchGraph,
  updateHypothesis,
} from '@/api/research'
import type {
  ResearchStatus,
  ResearchGraphResponse,
} from '@/types/models'

vi.mock('@/api/research', () => ({
  getResearchStatus: vi.fn(),
  getResearchGraph: vi.fn(),
  updateHypothesis: vi.fn(),
  createHypothesis: vi.fn(),
}))

// The data-sync hooks are side-effect only; stub them so the component tests
// exercise its own logic (filter toggle + edit persistence) against a seeded
// store without firing backend fetches on mount.
vi.mock('@/hooks/useResearchStatusEvents', () => ({
  useResearchStatusEvents: () => {},
}))
vi.mock('@/hooks/useResearchFileWatcher', () => ({
  useResearchFileWatcher: () => {},
}))

// The RESULT field is a CodeMirror editor. CodeMirror applies user input
// through DOM mutations + MutationObserver, which cannot be faithfully
// simulated in jsdom; stub it with a plain controlled textarea (same
// value/onChange contract) so the tests exercise the workspace's own data
// flow — draft → dirty → buildUpdateFields → updateHypothesis.
vi.mock('@/components/fileViewer/MiniCodeMirrorField', () => ({
  MiniCodeMirrorField: ({
    value,
    onChange,
  }: {
    value: string
    onChange: (v: string) => void
  }) => (
    <textarea
      aria-label="Hypothesis result"
      value={value}
      onChange={(e) => onChange(e.target.value)}
    />
  ),
}))

function makeStatus(): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/root/.research',
    root: {
      path: '/root/.research',
      index: [{ id: 'R-001', path: 'R-001/brief.md' }],
      active_project_id: 'R-001',
      projects: [
        {
          id: 'R-001',
          brief: { id: 'R-001', title: 'Test Research' },
          graph: {
            nodes: [
              { id: 'H-001', title: 'Root hypothesis', status: 'open' },
              {
                id: 'H-002',
                title: 'Completed child',
                status: 'confirmed',
                parents: ['H-001'],
              },
            ],
            edges: [{ from: 'H-001', to: 'H-002' }],
          },
          metrics: {
            total: 2,
            by_status: { open: 1, confirmed: 1 },
            confirmation_rate: 0.5,
            depth: 2,
            breadth: 1,
          },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
      ],
    },
  }
}

function makeGraphResponse(): ResearchGraphResponse {
  return {
    project_id: 'p1',
    graph: {
      nodes: [
        { id: 'H-001', title: 'Root hypothesis', status: 'confirmed' },
        { id: 'H-002', title: 'Completed child', status: 'confirmed', parents: ['H-001'] },
      ],
      edges: [{ from: 'H-001', to: 'H-002' }],
    },
    metrics: {
      total: 2,
      by_status: { confirmed: 2 },
      confirmation_rate: 1,
      depth: 2,
      breadth: 1,
    },
    has_report: false,
    log: [],
  }
}

async function renderWorkspace(): Promise<{ container: HTMLElement; root: Root }> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<ResearchWorkspace />)
  })
  return { container, root }
}

let activeRoot: Root | null = null

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getResearchStatus).mockResolvedValue(makeStatus())
  vi.mocked(getResearchGraph).mockResolvedValue(makeGraphResponse())
  vi.mocked(updateHypothesis).mockResolvedValue(makeGraphResponse())

  useProjectStore.setState({ projects: null, activeProjectId: 'p1', lastRealProjectId: 'p1' })
  useResearchStore.getState().reset()
  useResearchStore.getState().loadStatus(makeStatus(), 'p1')
  // Hypothesis-card links mutate the (persisted) viewer store; isolate tabs
  // between tests so assertions never see a previous test's opened cards.
  useFileViewerStore.setState({ openTabs: [], activeFile: null, collapsed: false })
})

/** Select a DAG node to open its editable sidebar card. */
async function selectNode(container: HTMLElement, id: string) {
  const node = container.querySelector(`[data-node-id="${id}"]`) as SVGElement
  await act(async () => {
    node.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Edit the RESULT field (mocked as a controlled textarea, see mock above). */
function setResultText(container: HTMLElement, text: string) {
  const result = container.querySelector<HTMLTextAreaElement>(
    'textarea[aria-label="Hypothesis result"]',
  )!
  const proto = Object.getPrototypeOf(result) as {
    value: PropertyDescriptor & { set?: (v: string) => void }
  }
  Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(result, text)
  result.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('ResearchWorkspace — filter toggle', () => {
  it('hides completed (terminal) hypotheses when the toggle is on', async () => {
    const { container } = await renderWorkspace()

    // Before toggling: both nodes are present in the DAG.
    expect(container.querySelector('[data-node-id="H-001"]')).not.toBeNull()
    expect(container.querySelector('[data-node-id="H-002"]')).not.toBeNull()

    const checkbox = container.querySelector<HTMLInputElement>(
      'input[aria-label="Hide completed hypotheses"]',
    )!
    await act(async () => {
      checkbox.click()
    })

    // After toggling: the terminal node is pruned; the open node remains.
    expect(container.querySelector('[data-node-id="H-001"]')).not.toBeNull()
    expect(container.querySelector('[data-node-id="H-002"]')).toBeNull()
  })
})

describe('ResearchWorkspace — edit persistence', () => {
  it('persists a status change through updateHypothesis', async () => {
    const { container } = await renderWorkspace()

    // Select H-001 to open the editable card.
    const node = container.querySelector('[data-node-id="H-001"]') as SVGElement
    await act(async () => {
      node.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // Change the status to 'confirmed'.
    const select = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Hypothesis status"]',
    )!
    await act(async () => {
      select.value = 'confirmed'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })

    // Save — the button becomes enabled once a field differs.
    const save = container.querySelector<HTMLButtonElement>(
      '[data-testid="hypothesis-save"]',
    )!
    expect(save.disabled).toBe(false)
    await act(async () => {
      save.click()
      // Flush the async save (updateHypothesis → loadGraph) inside act.
      await new Promise((r) => setTimeout(r, 0))
    })

    // Only the changed status field is sent; result/timebox are omitted.
    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'H-001', {
      status: 'confirmed',
    })
  })

  it('does not call updateHypothesis when nothing changed', async () => {
    const { container } = await renderWorkspace()

    const node = container.querySelector('[data-node-id="H-001"]') as SVGElement
    await act(async () => {
      node.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    const save = container.querySelector<HTMLButtonElement>(
      '[data-testid="hypothesis-save"]',
    )!
    // No field differs from the original node → save stays disabled.
    expect(save.disabled).toBe(true)

    expect(updateHypothesis).not.toHaveBeenCalled()
  })

  it('persists result and timebox edits through updateHypothesis', async () => {
    const { container } = await renderWorkspace()

    await selectNode(container, 'H-001')

    const timebox = container.querySelector<HTMLInputElement>(
      'input[aria-label="Hypothesis timebox"]',
    )!
    // Use the native value setter so React's controlled-input value tracker
    // observes the change, then fire the input event React maps to onChange.
    const setValue = (el: HTMLInputElement, value: string) => {
      const proto = Object.getPrototypeOf(el) as {
        value: PropertyDescriptor & { set?: (v: string) => void }
      }
      Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(el, value)
      el.dispatchEvent(new Event('input', { bubbles: true }))
    }
    await act(async () => {
      setValue(timebox, '2 weeks')
      setResultText(container, 'It works')
    })

    const save = container.querySelector<HTMLButtonElement>(
      '[data-testid="hypothesis-save"]',
    )!
    await act(async () => {
      save.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    // Only the changed fields (result + timebox) are sent; status is unchanged.
    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'H-001', {
      result: 'It works',
      timebox: '2 weeks',
    })
  })
})

describe('ResearchWorkspace — hypothesis card links', () => {
  it('opens the selected hypothesis markdown card from the header link', async () => {
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-002')

    const header = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Open H-002 markdown card"]',
    )!
    await act(async () => {
      header.click()
    })

    const viewer = useFileViewerStore.getState()
    expect(viewer.openTabs).toContain('/root/.research/R-001/hypotheses/H-002.md')
    expect(viewer.activeFile).toBe('/root/.research/R-001/hypotheses/H-002.md')
  })

  it('opens a parent hypothesis card from the parents list', async () => {
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-002')

    const parentLink = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Open H-001 markdown card"], button[title="Open H-001 markdown card"]',
    )!
    await act(async () => {
      parentLink.click()
    })

    expect(useFileViewerStore.getState().openTabs).toContain(
      '/root/.research/R-001/hypotheses/H-001.md',
    )
  })

  it('shows the full title as a tooltip on the header link', async () => {
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-001')

    const header = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Open H-001 markdown card"]',
    )!
    expect(header.title).toBe('Root hypothesis')
  })
})

describe('ResearchWorkspace — layout', () => {
  it('renders no bottom brief/prior-art/report bar', async () => {
    const { container } = await renderWorkspace()
    expect(container.textContent).not.toContain('Prior art')
    expect(container.textContent).not.toContain('Brief')
    expect(container.textContent).not.toContain('Report')
  })

  it('keeps a resizable separator between the DAG and the sidebar', async () => {
    const { container } = await renderWorkspace()

    const separator = container.querySelector<HTMLElement>('[role="separator"]')!
    expect(separator.getAttribute('aria-orientation')).toBe('vertical')

    const sidebar = container.querySelector<HTMLElement>(
      '[data-testid="hypothesis-sidebar"]',
    )!
    expect(sidebar.style.width).toBe('288px')

    // Keyboard resize: ArrowLeft on the right-side handle grows the sidebar
    // (direction −1, mirroring the docked file viewer).
    await act(async () => {
      separator.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }),
      )
    })
    expect(sidebar.style.width).toBe('298px')

    // Drag resize: mousedown on the handle, then a document-level mousemove
    // 100px to the left grows the sidebar by 100px (clamped at the max).
    await act(async () => {
      separator.dispatchEvent(
        new MouseEvent('mousedown', { clientX: 400, bubbles: true }),
      )
      document.dispatchEvent(new MouseEvent('mousemove', { clientX: 300 }))
      document.dispatchEvent(new MouseEvent('mouseup'))
    })
    expect(sidebar.style.width).toBe('398px')
  })
})
