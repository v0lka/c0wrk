// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's `persist` middleware captures at store-creation time (via
// createJSONStorage(() => window.localStorage)). Install an in-memory
// polyfill before any store module is imported so gitPanelStore works.
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  g.IS_REACT_ACT_ENVIRONMENT = true
  win.IS_REACT_ACT_ENVIRONMENT = true
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() { return map.size },
  }
})

// vi.mock factories are hoisted, so the mock objects must be created via
// vi.hoisted() to be accessible inside the factory.
const { gitMocks, workspaceMocks } = vi.hoisted(() => ({
  gitMocks: {
    commit: vi.fn(),
    generateCommitMessage: vi.fn(),
  },
  workspaceMocks: {
    getFileDiff: vi.fn(),
    // Re-export the rest as no-ops so other imports of the module resolve.
    getGitStatus: vi.fn(),
    getCurrentBranch: vi.fn(),
  },
}))

vi.mock('@/api/git', () => gitMocks)

// getFileDiff is imported from @/api/workspace in CommitSection.
vi.mock('@/api/workspace', () => workspaceMocks)

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn() },
}))

import { CommitSection } from './CommitSection'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

let container: HTMLDivElement
let root: Root

function makeEntry(
  overrides: Partial<GitPanelEntry> & { path: string },
): GitPanelEntry {
  return {
    status: 'M',
    staged: false,
    diffStat: null,
    indexStatus: '',
    worktreeStatus: '',
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  useGitPanelStore.getState().reset()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

function render() {
  act(() => {
    root.render(<CommitSection />)
  })
}

/** Flush all pending microtasks inside act() so async state updates settle. */
async function flush() {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** Find the "Generate" button (contains the word "Generate"). */
function generateBtn(): HTMLButtonElement {
  const btn = Array.from(container.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('Generate'),
  )
  expect(btn).toBeDefined()
  return btn as HTMLButtonElement
}

describe('CommitSection — AI generate button', () => {
  it('disables the Generate button when there are no staged files', () => {
    render()
    expect(generateBtn().disabled).toBe(true)
  })

  it('enables the Generate button when there are staged files', () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
    })
    render()
    expect(generateBtn().disabled).toBe(false)
  })

  it('collects staged diffs and inserts the generated message', async () => {
    useGitPanelStore.setState({
      entries: [
        makeEntry({ path: 'a.ts', staged: true }),
        makeEntry({ path: 'b.ts', staged: true }),
        makeEntry({ path: 'c.ts', staged: false }), // unstaged — ignored
      ],
    })
    workspaceMocks.getFileDiff.mockImplementation((p: string) =>
      Promise.resolve(`diff for ${p}`),
    )
    gitMocks.generateCommitMessage.mockResolvedValue('feat: add files')

    render()
    const btn = generateBtn()
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // Only staged files were diffed.
    expect(workspaceMocks.getFileDiff).toHaveBeenCalledWith('a.ts')
    expect(workspaceMocks.getFileDiff).toHaveBeenCalledWith('b.ts')
    expect(workspaceMocks.getFileDiff).not.toHaveBeenCalledWith('c.ts')
    // The combined diff (joined) was passed to the generator.
    expect(gitMocks.generateCommitMessage).toHaveBeenCalledWith(
      'diff for a.ts\ndiff for b.ts',
    )
    // The result was written into the store (and thus the textarea).
    expect(useGitPanelStore.getState().commitMessage).toBe('feat: add files')
  })

  it('shows an inline error when generation fails', async () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
    })
    workspaceMocks.getFileDiff.mockResolvedValue('diff for a.ts')
    gitMocks.generateCommitMessage.mockRejectedValue(
      new Error('llm router not available'),
    )

    render()
    const btn = generateBtn()
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(container.textContent).toContain('llm router not available')
    expect(useGitPanelStore.getState().isGeneratingCommit).toBe(false)
  })

  it('shows an error when there is no staged diff to generate from', async () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
    })
    // Every diff returns empty → nothing to generate from.
    workspaceMocks.getFileDiff.mockResolvedValue('')

    render()
    const btn = generateBtn()
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.generateCommitMessage).not.toHaveBeenCalled()
    expect(container.textContent).toContain(
      'No staged changes to generate a commit message from',
    )
  })

  it('swallows per-file diff failures and still generates', async () => {
    useGitPanelStore.setState({
      entries: [
        makeEntry({ path: 'bad.ts', staged: true }),
        makeEntry({ path: 'good.ts', staged: true }),
      ],
    })
    workspaceMocks.getFileDiff.mockImplementation((p: string) =>
      p === 'bad.ts'
        ? Promise.reject(new Error('boom'))
        : Promise.resolve('diff for good.ts'),
    )
    gitMocks.generateCommitMessage.mockResolvedValue('fix: thing')

    render()
    const btn = generateBtn()
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.generateCommitMessage).toHaveBeenCalledWith('diff for good.ts')
    expect(useGitPanelStore.getState().commitMessage).toBe('fix: thing')
  })
})
