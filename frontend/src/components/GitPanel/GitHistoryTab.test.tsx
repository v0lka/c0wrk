// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the git API wrappers so tests never touch the Wails backend ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    getGitHistory: vi.fn(),
    getCommitFiles: vi.fn(),
    getCommitFilesBatch: vi.fn(),
  },
}))

vi.mock('@/api/git', () => gitMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

// Mock the file viewer store so we can assert that clicking a commit SHA
// opens the read-only commit-review page via a synthetic pseudo-path.
const { fileViewerMocks } = vi.hoisted(() => ({
  fileViewerMocks: {
    openFile: vi.fn(),
    setCollapsed: vi.fn(),
  },
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: {
    getState: () => ({
      openFile: fileViewerMocks.openFile,
      setCollapsed: fileViewerMocks.setCollapsed,
    }),
  },
}))

// Mock the virtualizer so all rows render in jsdom (which has no layout
// engine, so the scroll container's height is 0 and the real virtualizer
// would mount zero items). The mock returns every item with correct
// absolute offsets so the component's positioning logic is still exercised.
vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: (options: {
    count: number
    estimateSize: (i: number) => number
    getItemKey: (i: number) => string | number
  }) => {
    const items = Array.from({ length: options.count }, (_, i) => ({
      index: i,
      key: options.getItemKey(i),
      start: Array.from({ length: i }, (_, j) => options.estimateSize(j)).reduce(
        (a, b) => a + b,
        0,
      ),
      size: options.estimateSize(i),
    }))
    const totalSize = items.reduce((sum, item) => sum + item.size, 0)
    return {
      getVirtualItems: () => items,
      getTotalSize: () => totalSize,
    }
  },
}))

import { GitHistoryTab } from './GitHistoryTab'
import { TooltipProvider } from '@/components/ui/tooltip'

let container: HTMLDivElement
let root: Root

const COMMITS = [
  { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'j@x', date: '2026-07-10', message: 'feat: x', refs: ['HEAD -> main'] },
  { sha: 'bbb', parents: ['ccc'], author: 'Jane', email: 'j@x', date: '2026-07-09', message: 'docs: readme', refs: [] },
  { sha: 'ccc', parents: [], author: 'Jane', email: 'j@x', date: '2026-07-08', message: 'init', refs: [] },
]

const FILES: Record<string, { path: string; status: string }[]> = {
  aaa: [{ path: 'src/a.ts', status: 'A' }, { path: 'README.md', status: 'M' }],
  bbb: [{ path: 'README.md', status: 'M' }],
  ccc: [{ path: 'src/main.go', status: 'A' }],
}

beforeEach(() => {
  gitMocks.getGitHistory.mockReset()
  gitMocks.getCommitFiles.mockReset()
  gitMocks.getCommitFilesBatch.mockReset()
  gitMocks.getGitHistory.mockResolvedValue(COMMITS)
  gitMocks.getCommitFiles.mockImplementation((sha: string) =>
    Promise.resolve(FILES[sha] ?? []),
  )
  gitMocks.getCommitFilesBatch.mockImplementation((shas: string[]) =>
    Promise.resolve(
      Object.fromEntries(shas.map((sha) => [sha, FILES[sha] ?? []])),
    ),
  )
  fileViewerMocks.openFile.mockReset()
  fileViewerMocks.setCollapsed.mockReset()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => { root.unmount() })
  container.remove()
  document.body.innerHTML = ''
})

/** Set a controlled input's value in a way React detects (see BranchPicker.test). */
function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setter.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

/** Flush microtasks so async effects (getGitHistory / getCommitFiles) resolve. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

function renderTab() {
  act(() => {
    root.render(
      <TooltipProvider>
        <GitHistoryTab />
      </TooltipProvider>,
    )
  })
}

/** Commit messages currently rendered, in DOM order (newest-first). */
function commitMessages(): string[] {
  return Array.from(container.querySelectorAll('button'))
    .map((b) => b.textContent ?? '')
    .filter((t) => COMMITS.some((c) => t.includes(c.message)))
    .map((t) => COMMITS.find((c) => t.includes(c.message))!.message)
}

/** The lane graph renders one <circle> per commit; it is unique to the gutter. */
function graphNodes(): number {
  return container.querySelectorAll('svg circle').length
}

describe('GitHistoryTab file filter', () => {
  it('renders the lane graph and all commits before filtering', async () => {
    renderTab()
    await flush()
    await flush()
    expect(graphNodes()).toBe(3)
    expect(commitMessages()).toEqual(['feat: x', 'docs: readme', 'init'])
  })

  it('hides the graph and narrows to matching commits when filtering', async () => {
    renderTab()
    await flush()
    await flush()
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.ts') })
    await flush()
    await flush()
    // Graph hidden while filtering; only the commit that touched src/a.ts.
    expect(graphNodes()).toBe(0)
    expect(commitMessages()).toEqual(['feat: x'])
  })

  it('shows "No matching commits" when nothing matches', async () => {
    renderTab()
    await flush()
    await flush()
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.rs') })
    await flush()
    await flush()
    expect(container.textContent).toContain('No matching commits')
    expect(graphNodes()).toBe(0)
  })

  it('restores the graph and all commits when the filter is cleared', async () => {
    renderTab()
    await flush()
    await flush()
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.ts') })
    await flush()
    await flush()
    expect(graphNodes()).toBe(0)
    await act(async () => { setInputValue(input, '') })
    await flush()
    await flush()
    expect(graphNodes()).toBe(3)
    expect(commitMessages()).toEqual(['feat: x', 'docs: readme', 'init'])
  })

  it('supports regex mode', async () => {
    renderTab()
    await flush()
    await flush()
    const toggle = container.querySelector('button[title="Switch to regex"]') as HTMLButtonElement
    await act(async () => { toggle.click() })
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, 'src/.*\\.ts') })
    await flush()
    await flush()
    expect(graphNodes()).toBe(0)
    expect(commitMessages()).toEqual(['feat: x'])
  })

  it('shows "Invalid regex" for a syntactically broken regex', async () => {
    renderTab()
    await flush()
    await flush()
    const toggle = container.querySelector('button[title="Switch to regex"]') as HTMLButtonElement
    await act(async () => { toggle.click() })
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '[') })
    await flush()
    expect(container.textContent).toContain('Invalid regex')
    expect(container.textContent).not.toContain('No matching commits')
    expect(graphNodes()).toBe(0)
  })
})

describe('GitHistoryTab rendering', () => {
  it('renders root commits with empty parents array', async () => {
    // The backend sends [] (not null) for root commits via
    // parents := []string{}. The type guard rejects null arrays.
    gitMocks.getGitHistory.mockResolvedValue([
      { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'j@x', date: 'd', message: 'feat', refs: [] },
      { sha: 'bbb', parents: [], author: 'Jane', email: 'j@x', date: 'd', message: 'root', refs: [] },
    ])

    renderTab()
    await flush()
    await flush()
    // Both commits should render — the root commit has empty parents
    expect(graphNodes()).toBe(2)
  })
})

describe('GitHistoryTab commit SHA click', () => {
  it('opens a read-only commit-review page when the SHA is clicked', async () => {
    renderTab()
    await flush()
    await flush()

    // The SHA renders as a role="button" span with title "View commit changes".
    const shaButton = container.querySelector('span[title="View commit changes"]') as HTMLSpanElement
    expect(shaButton).toBeTruthy()
    expect(shaButton.textContent).toBe('aaa')

    await act(async () => { shaButton.click() })

    // The file viewer should be asked to open the synthetic commit path
    // and expand so the review is visible.
    expect(fileViewerMocks.openFile).toHaveBeenCalledWith('c0wrk:commit:aaa')
    expect(fileViewerMocks.setCollapsed).toHaveBeenCalledWith(false)
  })

  it('does not toggle the row expansion when the SHA is clicked', async () => {
    renderTab()
    await flush()
    await flush()

    const shaButton = container.querySelector('span[title="View commit changes"]') as HTMLSpanElement
    // Before clicking the SHA, no commit is expanded.
    expect(container.textContent).not.toContain('Loading files')
    expect(container.textContent).not.toContain('src/a.ts')

    await act(async () => { shaButton.click() })

    // The row should NOT expand — stopPropagation prevents the row toggle.
    expect(container.textContent).not.toContain('src/a.ts')
  })
})
