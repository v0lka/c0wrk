// @vitest-environment jsdom
//
// PaperLiterature — the Literature section: it must render the DAG from
// literature.json, degrade EXPLICITLY for every failure mode (missing file,
// unreadable file, invalid JSON, offline/unresolved run), and surface the run
// outcome (running the helper through the backend).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/api/workspace', () => ({ listDirectory: vi.fn(), readFile: vi.fn() }))
vi.mock('@/api/papers', () => ({ runPaperLiterature: vi.fn() }))
vi.mock('@/lib/markdownConfig', () => ({
  Markdown: ({ content }: { content: string }) => <div data-testid="paper-markdown">{content}</div>,
}))

import { listDirectory, readFile } from '@/api/workspace'
import { runPaperLiterature } from '@/api/papers'
import { TooltipProvider } from '@/components/ui/tooltip'
import { PaperLiterature } from './PaperLiterature'
import { usePaperStore } from '@/stores/paperStore'
import type { PaperLiteratureResult, PaperRecord } from '@/api/papers'
import type { PaperArtifact } from './usePaperArtifacts'

const RAW = JSON.stringify({
  seed: { title: 'Seed Paper', year: 2017, doi: '10.1/seed' },
  predecessors: [
    { title: 'Pred A', year: 2014, doi: '10.1/a' },
    { title: 'Pred B', year: 1997, doi: '10.1/b' },
  ],
  citing: [{ title: 'Citer', year: 2019, doi: '10.1/c', reasons: ['critique'] }],
  contradictions: [{ title: 'Citer', year: 2019, doi: '10.1/c', reasons: ['critique'] }],
})

const DIR = '/ws/.research/papers/demo'
const PAPER: PaperRecord = {
  id: 'P-001',
  slug: 'demo',
  title: 'Demo Paper',
  authors: [],
  year: 2024,
  venue: '',
  identifiers: [],
  mode: 'deep',
  reading: '',
  verdict: '',
  confidence: '',
  research_ids: [],
  anchors: [],
  claims: [],
  red_flags: [],
  uncertainties: [],
  dir: DIR,
  card_path: 'papers/demo/paper.md',
  pinned: false,
  linked_research: [],
}

const ABSENT_MARKDOWN: PaperArtifact = { fileName: '', content: '', loading: false, missing: true, error: null }

function entry(name: string): { name: string; path: string; is_dir: boolean } {
  return { name, path: `${DIR}/${name}`, is_dir: false }
}

let root: Root | null = null
let container: HTMLDivElement | null = null

function render(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(
      <TooltipProvider>
        <PaperLiterature paper={PAPER} markdownArtifact={ABSENT_MARKDOWN} />
      </TooltipProvider>,
    )
  })
}

/** Flush the async artifact probe. */
async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
  })
}

function clickRun(): void {
  const el = document.querySelector<HTMLElement>('[data-testid="literature-run"]')
  expect(el).not.toBeNull()
  act(() => {
    el!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

describe('PaperLiterature', () => {
  beforeEach(() => {
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    })
    vi.stubGlobal('cancelAnimationFrame', () => {})
    usePaperStore.getState().loadLibrary({
      project_id: 'p1',
      research_root: '/ws/.research',
      root: '/ws/.research/papers',
      papers: [PAPER],
      pinned: [],
    })
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    container?.remove()
    root = null
    container = null
    vi.clearAllMocks()
    usePaperStore.getState().reset()
  })

  it('renders the DAG from literature.json and the seed summary', async () => {
    vi.mocked(listDirectory).mockResolvedValue([entry('literature.json')])
    vi.mocked(readFile).mockResolvedValue(RAW)
    render()
    await flush()
    expect(document.querySelector('[data-testid="literature-graph"]')).not.toBeNull()
    expect(document.querySelector('[data-node-id="seed"]')).not.toBeNull()
    expect(document.querySelector('[data-testid="paper-literature"]')?.textContent).toContain(
      '2 predecessors · 1 citing · 1 contradictions',
    )
  })

  it('degrades explicitly when literature.json is absent', async () => {
    vi.mocked(listDirectory).mockResolvedValue([])
    render()
    await flush()
    const empty = document.querySelector('[data-testid="literature-empty"]')
    expect(empty).not.toBeNull()
    expect(empty?.textContent).toContain('Run the lookup')
    expect(document.querySelector('[data-testid="literature-graph"]')).toBeNull()
  })

  it('degrades explicitly on invalid JSON', async () => {
    vi.mocked(listDirectory).mockResolvedValue([entry('literature.json')])
    vi.mocked(readFile).mockResolvedValue('{ not json')
    render()
    await flush()
    expect(document.querySelector('[data-testid="literature-parse-error"]')?.textContent).toContain(
      'could not be parsed',
    )
  })

  it('degrades explicitly when the file cannot be read', async () => {
    vi.mocked(listDirectory).mockResolvedValue([entry('literature.json')])
    vi.mocked(readFile).mockRejectedValue(new Error('EACCES'))
    render()
    await flush()
    expect(document.querySelector('[data-testid="literature-read-error"]')?.textContent).toContain(
      'EACCES',
    )
  })

  it('shows the run outcome explicitly when the helper is offline', async () => {
    vi.mocked(listDirectory).mockResolvedValue([])
    vi.mocked(runPaperLiterature).mockResolvedValue({
      status: 'offline',
      message: 'cannot reach api.openalex.org',
      path: '',
      content: '',
    })
    render()
    await flush()
    clickRun()
    await flush()
    const status = document.querySelector('[data-testid="literature-run-status"]')
    expect(status?.getAttribute('data-status')).toBe('offline')
    expect(status?.textContent).toContain('Network unavailable')
    expect(status?.textContent).toContain('cannot reach api.openalex.org')
    // No graph was fabricated from the failed run.
    expect(document.querySelector('[data-testid="literature-graph"]')).toBeNull()
  })

  it('reloads and renders the graph after a successful run', async () => {
    let calls = 0
    vi.mocked(listDirectory).mockImplementation(async () => {
      calls += 1
      return calls === 1 ? [] : [entry('literature.json')]
    })
    vi.mocked(readFile).mockResolvedValue(RAW)
    vi.mocked(runPaperLiterature).mockResolvedValue({
      status: 'ok',
      message: '',
      path: `${DIR}/literature.json`,
      content: RAW,
    })
    render()
    await flush()
    expect(document.querySelector('[data-testid="literature-empty"]')).not.toBeNull()
    clickRun()
    await flush()
    expect(runPaperLiterature).toHaveBeenCalledWith('p1', 'P-001')
    expect(document.querySelector('[data-testid="literature-run-status"]')?.getAttribute('data-status')).toBe(
      'ok',
    )
    expect(document.querySelector('[data-testid="literature-graph"]')).not.toBeNull()
  })

  it('shows the selected node card on selection', async () => {
    vi.mocked(listDirectory).mockResolvedValue([entry('literature.json')])
    vi.mocked(readFile).mockResolvedValue(RAW)
    render()
    await flush()
    const node = document.querySelector('[data-node-id="c1"]') as Element
    act(() => {
      node.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    const detail = document.querySelector('[data-testid="literature-detail"]')
    expect(detail?.getAttribute('data-kind')).toBe('contradiction')
    expect(detail?.textContent).toContain('Citer')
    expect(detail?.textContent).toContain('Contradiction markers: critique')
  })

  it('renders the "generated <date>" staleness hint from the payload', async () => {
    vi.mocked(listDirectory).mockResolvedValue([entry('literature.json')])
    vi.mocked(readFile).mockResolvedValue(
      JSON.stringify({ generated_at: '2026-09-16T12:34:56Z', seed: { title: 'Seed Paper' } }),
    )
    render()
    await flush()
    const hint = document.querySelector('[data-testid="literature-generated"]')
    expect(hint).not.toBeNull()
    expect(hint?.textContent).toBe('generated 2026-09-16')
  })

  it('omits the staleness hint for a legacy payload without generated_at', async () => {
    vi.mocked(listDirectory).mockResolvedValue([entry('literature.json')])
    vi.mocked(readFile).mockResolvedValue(RAW)
    render()
    await flush()
    expect(document.querySelector('[data-testid="literature-generated"]')).toBeNull()
  })

  it('labels the run control as an explicit Refresh', async () => {
    vi.mocked(listDirectory).mockResolvedValue([])
    render()
    await flush()
    const run = document.querySelector('[data-testid="literature-run"]')
    expect(run?.textContent).toContain('Refresh')
  })

  it('offers an actionable path forward for the no_python outcome', async () => {
    vi.mocked(listDirectory).mockResolvedValue([])
    vi.mocked(runPaperLiterature).mockResolvedValue({
      status: 'no_python',
      message: "c0wrk's managed Python interpreter is not installed yet",
      path: '',
      content: '',
    })
    render()
    await flush()
    clickRun()
    await flush()
    const affordance = document.querySelector('[data-testid="literature-no-python"]')
    expect(affordance).not.toBeNull()
    expect(affordance?.textContent).toContain('tool manager')
    const retry = document.querySelector<HTMLElement>('[data-testid="literature-no-python-retry"]')
    expect(retry).not.toBeNull()
    // The affordance's Retry re-runs the lookup (a path forward, not a dead end).
    act(() => {
      retry!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()
    expect(runPaperLiterature).toHaveBeenCalledTimes(2)
  })

  it('drops a run result that resolves after the paper changes (identity guard)', async () => {
    vi.mocked(listDirectory).mockResolvedValue([])
    let resolveRun: (result: PaperLiteratureResult) => void = () => {}
    vi.mocked(runPaperLiterature).mockReturnValue(
      new Promise<PaperLiteratureResult>((resolve) => {
        resolveRun = resolve
      }),
    )
    const other: PaperRecord = {
      ...PAPER,
      id: 'P-002',
      slug: 'other',
      dir: '/ws/.research/papers/other',
    }

    const ownContainer = document.createElement('div')
    document.body.appendChild(ownContainer)
    const ownRoot = createRoot(ownContainer)
    const view = (paper: PaperRecord) => (
      <TooltipProvider>
        <PaperLiterature paper={paper} markdownArtifact={ABSENT_MARKDOWN} />
      </TooltipProvider>
    )

    act(() => {
      ownRoot.render(view(PAPER))
    })
    await flush()
    act(() => {
      ownContainer
        .querySelector<HTMLElement>('[data-testid="literature-run"]')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // The displayed paper changes before the ~90 s-bounded RPC resolves.
    act(() => {
      ownRoot.render(view(other))
    })
    await act(async () => {
      resolveRun({ status: 'offline', message: 'late result', path: '', content: '' })
      await Promise.resolve()
    })

    // Paper A's late status is dropped, not rendered under paper B.
    expect(ownContainer.querySelector('[data-testid="literature-run-status"]')).toBeNull()

    act(() => {
      ownRoot.unmount()
    })
    ownContainer.remove()
  })
})
