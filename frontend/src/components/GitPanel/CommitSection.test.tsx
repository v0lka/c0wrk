// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

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

  it('triggers generation and inserts the generated message', async () => {
    useGitPanelStore.setState({
      entries: [
        makeEntry({ path: 'a.ts', staged: true }),
        makeEntry({ path: 'b.ts', staged: true }),
        makeEntry({ path: 'c.ts', staged: false }), // unstaged — ignored
      ],
    })
    gitMocks.generateCommitMessage.mockResolvedValue('feat: add files')

    render()
    const btn = generateBtn()
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // The backend runs `git diff --staged` itself, so the frontend only
    // triggers generation — no per-file diff collection.
    expect(workspaceMocks.getFileDiff).not.toHaveBeenCalled()
    expect(gitMocks.generateCommitMessage).toHaveBeenCalledTimes(1)
    expect(gitMocks.generateCommitMessage).toHaveBeenCalledWith()
    // The result was written into the store (and thus the textarea).
    expect(useGitPanelStore.getState().commitMessage).toBe('feat: add files')
  })

  it('shows an inline error when generation fails', async () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
    })
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

  it('shows an error when the backend reports no staged changes', async () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
    })
    // The backend runs `git diff --staged` and returns this error when
    // the staged changeset is empty (e.g. stale panel entries).
    gitMocks.generateCommitMessage.mockRejectedValue(
      new Error('no staged changes to generate a commit message from'),
    )

    render()
    const btn = generateBtn()
    await act(async () => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.generateCommitMessage).toHaveBeenCalled()
    expect(container.textContent).toContain(
      'no staged changes to generate a commit message from',
    )
  })
})
