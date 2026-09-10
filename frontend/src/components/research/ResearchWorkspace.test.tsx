// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchWorkspace } from './ResearchWorkspace'
import { TooltipProvider } from '@/components/ui/tooltip'
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
  getResearchNextStep: vi.fn(),
  updateHypothesis: vi.fn(),
  createHypothesis: vi.fn(),
}))

// No data-sync hooks are mocked or mounted here: ResearchWorkspace is a pure
// view over researchStore (sync lives in the App-root ResearchEventBridge),
// so the tests below seed the store directly and exercise the workspace's
// own logic against it.

// The long-form fields (Statement / Verification Criterion / Experiment
// Notes / Result) are CodeMirror editors. CodeMirror applies user input
// through DOM mutations + MutationObserver, which cannot be faithfully
// simulated in jsdom; stub them with a plain controlled textarea (same
// value/onChange/aria-label contract) so the tests exercise the workspace's
// own data flow — draft → dirty → buildUpdateFields → updateHypothesis. The
// stub also counts MOUNTS: the workspace keys the card on the hypothesis id,
// so every selection switch must remount the editors — a fresh CodeMirror
// instance whose doc/cursor/history (and any mount-time capture) never leak
// across cards.
const editorMounts = vi.hoisted(() => ({ count: 0 }))

vi.mock('@/components/fileViewer/MiniCodeMirrorField', async () => {
  const { useEffect } = await import('react')
  return {
    MiniCodeMirrorField: ({
      value,
      onChange,
      ariaLabel,
    }: {
      value: string
      onChange: (v: string) => void
      ariaLabel: string
    }) => {
      useEffect(() => {
        editorMounts.count += 1
      }, [])
      return (
        <textarea
          aria-label={ariaLabel ?? 'Hypothesis result'}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )
    },
  }
})

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
    // The DAG nodes embed a Radix Tooltip (the hover hypothesis card), which
    // requires a provider ancestor — the app mounts one at the root; tests
    // mirror that here.
    root.render(
      <TooltipProvider>
        <ResearchWorkspace />
      </TooltipProvider>,
    )
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
  editorMounts.count = 0
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

/** Set a long-form field (mocked as a controlled textarea, see mock above)
 *  by its aria-label ("Hypothesis statement" / "Hypothesis result" / …). */
function setSectionText(container: HTMLElement, label: string, text: string) {
  const field = container.querySelector<HTMLTextAreaElement>(
    `textarea[aria-label="Hypothesis ${label}"]`,
  )!
  const proto = Object.getPrototypeOf(field) as {
    value: PropertyDescriptor & { set?: (v: string) => void }
  }
  Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(field, text)
  field.dispatchEvent(new Event('input', { bubbles: true }))
}

/** Edit an <input> by aria-label using the native value setter (so React's
 *  controlled-input value tracker observes the change). */
function setInputValue(container: HTMLElement, label: string, value: string) {
  const field = container.querySelector<HTMLInputElement>(
    `input[aria-label="${label}"]`,
  )!
  const proto = Object.getPrototypeOf(field) as {
    value: PropertyDescriptor & { set?: (v: string) => void }
  }
  Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(field, value)
  field.dispatchEvent(new Event('input', { bubbles: true }))
}

/** Choose a <select> option by aria-label using the native value setter
 *  (so React's controlled-select value tracker observes the change). */
function setSelectValue(container: HTMLElement, label: string, value: string) {
  const field = container.querySelector<HTMLSelectElement>(
    `select[aria-label="${label}"]`,
  )!
  const proto = Object.getPrototypeOf(field) as {
    value: PropertyDescriptor & { set?: (v: string) => void }
  }
  Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(field, value)
  field.dispatchEvent(new Event('change', { bubbles: true }))
}

/** The full-draft expectation for a node's own snapshot. */
function expectDraftSnapshot(
  status: string,
  result: string,
  parents = '',
  title = expect.any(String),
) {
  return {
    title,
    parents,
    status,
    decision: '',
    statement: '',
    verification_criterion: '',
    experiment_notes: '',
    timebox: '',
    result,
  }
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

  it('carries the RESEARCH disable toggle in its header', async () => {
    const { container } = await renderWorkspace()

    // The workspace tab is the only place the disable control renders while
    // RESEARCH is on (the panel header lost it to the project picker).
    const disable = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Disable RESEARCH mode"]',
    )
    expect(disable).not.toBeNull()
    expect(disable!.className).toContain('text-destructive')
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

    // Change the status to 'in-progress' — a legal open → in-progress move;
    // the select only offers the current status and its legal targets (the
    // backend state machine rejects every other jump).
    const select = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Hypothesis status"]',
    )!
    await act(async () => {
      select.value = 'in-progress'
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

    // Only the changed status field is sent; result/timebox are omitted. The
    // call carries the expected research project ([19]) resolved fresh from
    // the store at save time.
    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'R-001', 'H-001', {
      status: 'in-progress',
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

    await act(async () => {
      setInputValue(container, 'Hypothesis timebox', '2 weeks')
      setSectionText(container, 'result', 'It works')
    })

    const save = container.querySelector<HTMLButtonElement>(
      '[data-testid="hypothesis-save"]',
    )!
    await act(async () => {
      save.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    // Only the changed fields (result + timebox) are sent; status is unchanged.
    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'R-001', 'H-001', {
      result: 'It works',
      timebox: '2 weeks',
    })
  })

  it('persists title, parents, decision, and long-form sections through updateHypothesis', async () => {
    const { container } = await renderWorkspace()

    await selectNode(container, 'H-001')

    await act(async () => {
      setInputValue(container, 'Hypothesis title', 'Renamed hypothesis')
      setInputValue(container, 'Hypothesis parents', 'H-002')
      setSelectValue(container, 'Hypothesis decision', 'continue')
      setSectionText(container, 'statement', 'New statement.')
      setSectionText(container, 'verification criterion', 'New criterion.')
      setSectionText(container, 'experiment notes', 'New notes.')
    })

    const save = container.querySelector<HTMLButtonElement>(
      '[data-testid="hypothesis-save"]',
    )!
    expect(save.disabled).toBe(false)
    await act(async () => {
      save.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'R-001', 'H-001', {
      title: 'Renamed hypothesis',
      parents: ['H-002'],
      decision: 'continue',
      statement: 'New statement.',
      verification_criterion: 'New criterion.',
      experiment_notes: 'New notes.',
    })
  })

  it('offers only the legal status transitions in the card select', async () => {
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-001')

    // H-001 is 'open': the select must offer open → in-progress/cancelled
    // and hide illegal jumps (e.g. open → confirmed) the backend rejects.
    const select = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Hypothesis status"]',
    )!
    expect(Array.from(select.options).map((o) => o.value)).toEqual([
      'open',
      'in-progress',
      'cancelled',
    ])
  })

  it('offers the fixed decision vocabulary in the card select', async () => {
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-001')

    // The decision is a combobox, not free text: undecided ('') plus the
    // methodology's four canonical decisions (research-decision skill).
    const select = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Hypothesis decision"]',
    )!
    expect(Array.from(select.options).map((o) => o.value)).toEqual([
      '',
      'continue',
      'pivot',
      'kill',
      'fork',
    ])
    // The undecided option carries a human label; canonical values as-is.
    expect(Array.from(select.options).map((o) => o.textContent)).toEqual([
      'undecided',
      'continue',
      'pivot',
      'kill',
      'fork',
    ])
  })

  it('keeps a legacy free-text decision visible and replaceable', async () => {
    // An older card may carry a non-canonical Decision (the backend stores
    // the field verbatim): the select must still render a matching option —
    // and the user can replace it with a canonical one.
    const status = makeStatus()
    status.root!.projects[0]!.graph.nodes[0]!.decision = 'investigate deeper'
    useResearchStore.getState().loadStatus(status, 'p1')
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-001')

    const select = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Hypothesis decision"]',
    )!
    expect(Array.from(select.options).map((o) => o.value)).toEqual([
      'investigate deeper',
      '',
      'continue',
      'pivot',
      'kill',
      'fork',
    ])

    // Replacing it with a canonical decision marks the draft dirty and
    // persists exactly the decision field.
    await act(async () => {
      setSelectValue(container, 'Hypothesis decision', 'kill')
    })
    const save = container.querySelector<HTMLButtonElement>(
      '[data-testid="hypothesis-save"]',
    )!
    expect(save.disabled).toBe(false)
    await act(async () => {
      save.click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'R-001', 'H-001', {
      decision: 'kill',
    })
  })
})

describe('ResearchWorkspace — card drafts are isolated per hypothesis', () => {
  it("editing one card never writes the other card's fields", async () => {
    const { container } = await renderWorkspace()

    // Card 1: edit H-001's result.
    await selectNode(container, 'H-001')
    expect(editorMounts.count).toBe(4) // statement + criterion + notes + result
    await act(async () => {
      setSectionText(container, 'result', 'H-001 finding')
    })
    expect(useResearchStore.getState().hypothesisDraft).toEqual(
      expectDraftSnapshot('open', 'H-001 finding'),
    )

    // Card 2: switch to H-002 and edit its result. The draft must rebase on
    // H-002's OWN snapshot — H-001's fields must not bleed into it (the
    // historical bug: an editor instance reused across cards kept a
    // mount-time onChange closure that spread the FIRST card's draft over
    // every later card).
    await selectNode(container, 'H-002')
    // The card subtree is keyed by hypothesis id → the editors REMOUNT.
    expect(editorMounts.count).toBe(8)
    await act(async () => {
      setSectionText(container, 'result', 'H-002 finding')
    })
    expect(useResearchStore.getState().hypothesisDraft).toEqual(
      expectDraftSnapshot('confirmed', 'H-002 finding', 'H-001'), // H-002's own fields
    )

    // Card 1 again: re-selecting H-001 re-snapshots from the (unchanged)
    // graph — H-001's own original fields, with no H-002 values either.
    await selectNode(container, 'H-001')
    expect(useResearchStore.getState().hypothesisDraft).toEqual(
      expectDraftSnapshot('open', ''),
    )
  })
})

describe('ResearchWorkspace — remount survival (floating-viewer collapse)', () => {
  it('keeps the selected vertex, open card, unsaved draft, filter, and card height across unmount/remount', async () => {
    // Mount 1: select H-001, type an unsaved result edit, enable "Hide
    // completed", and grow the card panel via the keyboard handle.
    const first = await renderWorkspace()
    await selectNode(first.container, 'H-001')
    await act(async () => {
      setSectionText(first.container, 'result', 'unsaved finding')
    })
    const checkbox = first.container.querySelector<HTMLInputElement>(
      'input[aria-label="Hide completed hypotheses"]',
    )!
    await act(async () => {
      checkbox.click()
    })
    const separator = first.container.querySelector<HTMLElement>('[role="separator"]')!
    await act(async () => {
      separator.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }),
      )
    })

    // The floating (unpinned) file viewer auto-collapses when focus moves
    // outside it, unmounting the whole panel including the workspace.
    await act(async () => {
      first.root.unmount()
    })

    // Mount 2: expanding the viewer again remounts the workspace.
    const second = await renderWorkspace()

    // The selection survived: the DAG paints the highlight ring around
    // H-001 (second circle in the node group)…
    const circles = second.container.querySelectorAll(
      '[data-node-id="H-001"] circle',
    )
    expect(circles.length).toBe(2)
    // …and the card panel is open for H-001 with the unsaved draft intact.
    expect(
      second.container.querySelector('button[aria-label="Open H-001 markdown card"]'),
    ).not.toBeNull()
    const result = second.container.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="Hypothesis result"]',
    )!
    expect(result.value).toBe('unsaved finding')
    // The draft differs from the persisted node → Save stays enabled.
    const save = second.container.querySelector<HTMLButtonElement>(
      '[data-testid="hypothesis-save"]',
    )!
    expect(save.disabled).toBe(false)
    // View state survived too: the filter still prunes the terminal node and
    // the card panel keeps the keyboard-grown height (300 → 310, ArrowUp on
    // the divider moves it up = grow).
    expect(second.container.querySelector('[data-node-id="H-002"]')).toBeNull()
    expect(useResearchStore.getState().hideTerminal).toBe(true)
    expect(useResearchStore.getState().cardHeight).toBe(310)
  })

  it('still toggles the selection off by clicking the selected node after a remount', async () => {
    const first = await renderWorkspace()
    await selectNode(first.container, 'H-001')
    await act(async () => {
      first.root.unmount()
    })

    const second = await renderWorkspace()
    // Selection restored by the remount…
    expect(
      second.container.querySelector('[data-testid="hypothesis-card"]'),
    ).not.toBeNull()
    // …and clicking the same node still clears it.
    await selectNode(second.container, 'H-001')
    expect(
      second.container.querySelector('[data-testid="hypothesis-card"]'),
    ).toBeNull()
    expect(useResearchStore.getState().selectedHypothesisId).toBeNull()
  })
})

describe('ResearchWorkspace — selection is keyed to its research project', () => {
  /** Seed a second research project (R-002) and make it active — the
   *  same-workspace-project active-R-NNN transition research-init produces. */
  function switchActiveProject() {
    const switched = makeStatus()
    const r1 = switched.root!.projects[0]!
    switched.root!.projects.push({
      ...r1,
      id: 'R-002',
      brief: { id: 'R-002', title: 'Follow-up research' },
    })
    switched.root!.active_project_id = 'R-002'
    useResearchStore.getState().loadStatus(switched, 'p1')
  }

  it('does not rebind a selection (or its unsaved draft) when the active R-NNN switches', async () => {
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-001')
    await act(async () => {
      setSectionText(container, 'result', 'unsaved finding for R-001')
    })
    expect(container.querySelector('[data-testid="hypothesis-card"]')).not.toBeNull()

    await act(async () => {
      switchActiveProject()
    })

    // The stale selection must NOT rebind to R-002's same-id H-001 (which
    // would let Save overwrite another project's card through the
    // active-R-NNN backend semantics): no card, no draft editor, and no
    // highlight ring on the DAG.
    expect(container.querySelector('[data-testid="hypothesis-card"]')).toBeNull()
    expect(
      container.querySelector('textarea[aria-label="Hypothesis result"]'),
    ).toBeNull()
    const circles = container.querySelectorAll('[data-node-id="H-001"] circle')
    expect(circles.length).toBe(1)
    // …while the store still holds the keyed selection + draft (they render
    // again only if that project becomes active once more).
    const s = useResearchStore.getState()
    expect(s.selectedHypothesisId).toBe('H-001')
    expect(s.selectedHypothesisProjectId).toBe('R-001')
    expect(s.hypothesisDraft?.result).toBe('unsaved finding for R-001')
  })

  it('selecting a node after the switch targets the new active project', async () => {
    const { container } = await renderWorkspace()
    await act(async () => {
      switchActiveProject()
    })

    await selectNode(container, 'H-001')

    const s = useResearchStore.getState()
    expect(s.selectedHypothesisId).toBe('H-001')
    expect(s.selectedHypothesisProjectId).toBe('R-002')
    expect(container.querySelector('[data-testid="hypothesis-card"]')).not.toBeNull()
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

  it('shows the full title in the editable title field', async () => {
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-001')

    const title = container.querySelector<HTMLInputElement>(
      'input[aria-label="Hypothesis title"]',
    )!
    expect(title.value).toBe('Root hypothesis')
  })
})

describe('ResearchWorkspace — layout', () => {
  it('renders no bottom brief/prior-art/report bar', async () => {
    const { container } = await renderWorkspace()
    expect(container.textContent).not.toContain('Prior art')
    expect(container.textContent).not.toContain('Brief')
    expect(container.textContent).not.toContain('Report')
  })

  it('keeps a resizable horizontal separator between the DAG and the card panel', async () => {
    const { container } = await renderWorkspace()

    const separator = container.querySelector<HTMLElement>('[role="separator"]')!
    expect(separator.getAttribute('aria-orientation')).toBe('horizontal')

    const cardPanel = container.querySelector<HTMLElement>(
      '[data-testid="hypothesis-sidebar"]',
    )!
    expect(cardPanel.style.height).toBe('300px')

    // Keyboard resize: ArrowUp moves the divider up → the bottom card panel
    // grows (direction -1 on the y axis); ArrowDown moves it down → shrink.
    await act(async () => {
      separator.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }),
      )
    })
    expect(cardPanel.style.height).toBe('310px')
    await act(async () => {
      separator.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
      )
    })
    expect(cardPanel.style.height).toBe('300px')

    // Drag resize: the divider follows the pointer — mousedown on the handle,
    // then a document-level mousemove 100px UP grows the card panel by 100px.
    await act(async () => {
      separator.dispatchEvent(
        new MouseEvent('mousedown', { clientY: 400, bubbles: true }),
      )
      document.dispatchEvent(new MouseEvent('mousemove', { clientY: 300 }))
      document.dispatchEvent(new MouseEvent('mouseup'))
    })
    expect(cardPanel.style.height).toBe('400px')

    // And dragging the divider back DOWN 100px shrinks the card panel again.
    await act(async () => {
      separator.dispatchEvent(
        new MouseEvent('mousedown', { clientY: 400, bubbles: true }),
      )
      document.dispatchEvent(new MouseEvent('mousemove', { clientY: 500 }))
      document.dispatchEvent(new MouseEvent('mouseup'))
    })
    expect(cardPanel.style.height).toBe('300px')
  })

  it('scrolls the whole card in the panel-owned custom-scrollbar region', async () => {
    const { container } = await renderWorkspace()
    await selectNode(container, 'H-001')

    // The card panel (hypothesis-sidebar) is the single scroll region: the
    // WHOLE card — header, field table, long-form sections, Save — flows
    // inside it, so nothing is pinned outside the scroll.
    const cardPanel = container.querySelector<HTMLElement>(
      '[data-testid="hypothesis-sidebar"]',
    )!
    expect(cardPanel.className).toContain('overflow-auto')
    expect(cardPanel.className).toContain('custom-scrollbar')

    // The card must not own an inner scroll region (no height pinning, no
    // pinned header with a separately scrolling body). Only the CodeMirror
    // fields themselves may carry overflow-auto — harmless with max-h-none,
    // they grow to content and never clip, so the panel keeps the overflow.
    const card = container.querySelector<HTMLElement>(
      '[data-testid="hypothesis-card"]',
    )!
    expect(card.className).not.toContain('h-full')
    const innerScroll = Array.from(card.querySelectorAll<HTMLElement>('div')).find(
      (el) =>
        el.className.includes('overflow-auto') &&
        !el.className.includes('cm-viewer-container'),
    )
    expect(innerScroll).toBeUndefined()

    // Methodology card order: Statement → Verification Criterion → Experiment
    // Notes → Result (Result stays last).
    const sectionLabels = [
      'Statement',
      'Verification Criterion',
      'Experiment Notes',
      'Result',
    ]
    const labels = Array.from(card.querySelectorAll('label > span'))
      .map((el) => el.textContent?.trim() ?? '')
      .filter((t) => sectionLabels.includes(t))
    expect(labels).toEqual(sectionLabels)
  })
})
