// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// vi.mock factories are hoisted, so the mock objects must be created via
// vi.hoisted() to be accessible inside the factory.
const { gitMocks, workspaceMocks, trustMocks } = vi.hoisted(() => ({
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
  trustMocks: {
    trustGitRepo: vi.fn(),
  },
}))

vi.mock('@/api/git', () => gitMocks)

// getFileDiff is imported from @/api/workspace in CommitSection.
vi.mock('@/api/workspace', () => workspaceMocks)

// TrustGitRepo flows through the gitConfigRisk wrapper — one import path
// for backend calls.
vi.mock('@/api/gitConfigRisk', () => trustMocks)

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn() },
}))

import { CommitSection } from './CommitSection'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

let container: HTMLDivElement
let root: Root

/** Create a promise whose settlement is controlled by the test. */
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

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
  // A real project is active by default, mirroring CODE mode with a project
  // selected. Per-project tests override this explicitly.
  useProjectStore.setState({ activeProjectId: 'proj-a' })
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

/** Find the "Commit" button (contains the word "Commit"). */
function commitBtn(): HTMLButtonElement {
  const btn = Array.from(container.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('Commit'),
  )
  expect(btn).toBeDefined()
  return btn as HTMLButtonElement
}

function textarea(): HTMLTextAreaElement {
  const el = container.querySelector('textarea')
  expect(el).toBeDefined()
  return el as HTMLTextAreaElement
}

/** Simulate typing into the textarea via a native 'input' event. */
function typeDraft(text: string) {
  act(() => {
    const el = textarea()
    const setter = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      'value',
    )!.set!
    setter.call(el, text)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

/** Switch the active project (as ProjectSelector does). */
function switchProject(projectId: string | null) {
  act(() => {
    useProjectStore.setState({ activeProjectId: projectId })
  })
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
    // The result was written into the active project's slice (and thus the
    // textarea).
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.message).toBe(
      'feat: add files',
    )
    expect(textarea().value).toBe('feat: add files')
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
    expect(
      useGitPanelStore.getState().commitByProject['proj-a']!.isGenerating,
    ).toBe(false)
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

describe('CommitSection — per-project commit state', () => {
  it('keeps a separate draft per project and restores it on A→B→A', () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
    })
    render()
    typeDraft('feat: draft for A')
    expect(textarea().value).toBe('feat: draft for A')

    // Switch to project B — the box shows B's (empty) draft.
    switchProject('proj-b')
    expect(textarea().value).toBe('')
    typeDraft('fix: draft for B')

    // Switch back to A — A's draft is restored exactly.
    switchProject('proj-a')
    expect(textarea().value).toBe('feat: draft for A')
    switchProject('proj-b')
    expect(textarea().value).toBe('fix: draft for B')
  })

  it('survives GitPanel unmount (CHAT mode) in memory', () => {
    render()
    typeDraft('feat: keep me')
    switchProject('proj-a') // no-op, just ensure same project

    // Simulate the CHAT↔CODE mode switch unmounting the GitPanel…
    act(() => {
      root.render(null)
    })
    // …and returning to CODE mode later.
    render()
    expect(textarea().value).toBe('feat: keep me')
  })

  it('REGRESSION: generate result lands in the project captured at click time', async () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
    })
    const pending = deferred<string>()
    gitMocks.generateCommitMessage.mockReturnValueOnce(pending.promise)

    render()
    await act(async () => {
      generateBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // Generation is in flight — flagged on A's slice only.
    expect(
      useGitPanelStore.getState().commitByProject['proj-a']!.isGenerating,
    ).toBe(true)

    // Switch to project B while the promise is still pending.
    switchProject('proj-b')
    expect(
      useGitPanelStore.getState().commitByProject['proj-b'],
    ).toBeUndefined()

    await act(async () => {
      pending.resolve('feat: generated for A')
    })
    await flush()

    // The result landed in A's slice; B's slice is untouched and the box
    // (now showing B) stays empty.
    const byProject = useGitPanelStore.getState().commitByProject
    expect(byProject['proj-a']!.message).toBe('feat: generated for A')
    expect(byProject['proj-a']!.isGenerating).toBe(false)
    expect(byProject['proj-b']).toBeUndefined()
    expect(textarea().value).toBe('')
    expect(container.textContent).not.toContain('feat: generated for A')
  })

  it('REGRESSION: generate error lands in the project captured at click time', async () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
    })
    const pending = deferred<string>()
    gitMocks.generateCommitMessage.mockReturnValueOnce(pending.promise)

    render()
    await act(async () => {
      generateBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // Switch to project B while the promise is still pending.
    switchProject('proj-b')

    await act(async () => {
      pending.reject(new Error('model offline'))
    })
    await flush()

    const byProject = useGitPanelStore.getState().commitByProject
    expect(byProject['proj-a']!.error).toBe('model offline')
    expect(byProject['proj-a']!.isGenerating).toBe(false)
    // B was never touched, and the error is NOT shown while viewing B.
    expect(byProject['proj-b']).toBeUndefined()
    expect(container.textContent).not.toContain('model offline')

    // Switching back to A surfaces the stored error.
    switchProject('proj-a')
    expect(container.textContent).toContain('model offline')
  })

  it('commits the active project draft, records it in the console and clears the draft', async () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
      commitByProject: {
        'proj-a': {
          message: 'feat: a',
          isGenerating: false,
          isCommitting: false,
          error: null,
        },
      },
    })
    gitMocks.commit.mockResolvedValue({ sha: 'full-sha-abcdef123456' })

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.commit).toHaveBeenCalledWith('feat: a', false)
    // The successful commit consumes the draft…
    const slice = useGitPanelStore.getState().commitByProject['proj-a']!
    expect(slice.message).toBe('')
    // …and its outcome lands in the git-operation console (short SHA label).
    const rec = useGitPanelStore.getState().operationByProject['proj-a']!
    expect(rec.kind).toBe('commit')
    expect(rec.ok).toBe(true)
    expect(rec.label).toBe('Committed full-sh')
    expect(rec.error).toBeNull()
    // No inline success surface (banner) remains in the commit box.
    expect(container.textContent).not.toContain('Committed')
    expect(container.textContent).not.toContain('full-sh')
  })

  it('keeps the commit-in-flight flag in the store across a GitPanel unmount', async () => {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
      commitByProject: {
        'proj-a': {
          message: 'feat: a',
          isGenerating: false,
          isCommitting: false,
          error: null,
        },
      },
    })
    const { promise, reject } = deferred<string>()
    gitMocks.commit.mockReturnValueOnce(promise)

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    // In flight: the flag lives in the store and both buttons are disabled.
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.isCommitting).toBe(true)
    expect(commitBtn().disabled).toBe(true)
    expect(generateBtn().disabled).toBe(true)

    // A CHAT↔CODE mode switch unmounts the GitPanel mid-commit; the flag
    // must survive in the store instead of resetting with the component.
    act(() => {
      root.unmount()
    })
    container.remove()
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    render()

    // The remounted section still reflects the in-flight commit.
    expect(commitBtn().disabled).toBe(true)
    expect(container.textContent).not.toContain('Committed')

    reject(new Error('hook declined'))
    await flush()
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.isCommitting).toBe(false)
    // The failure is a git-operation result: it lands in the console, never
    // as an inline error under the textarea.
    const rec = useGitPanelStore.getState().operationByProject['proj-a']!
    expect(rec.ok).toBe(false)
    expect(rec.error).toBe('hook declined')
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.error).toBeNull()
    expect(container.textContent).not.toContain('hook declined')
    expect(commitBtn().disabled).toBe(false)
  })
})

describe('CommitSection — suppressed commit dialog', () => {
  const SUPPRESSED = {
    hooks: ['pre-commit', 'commit-msg'],
    signing_repo: false,
    signing_global: true,
  }

  /** Stage one file and type a draft so Commit is enabled. */
  function stageAndDraft(message = 'feat: a') {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
      commitByProject: {
        'proj-a': {
          message,
          isGenerating: false,
          isCommitting: false,
          error: null,
        },
      },
    })
  }

  /** Buttons portaled into document.body by the Radix Dialog. */
  function dialogBtn(testId: string): HTMLButtonElement {
    const el = document.body.querySelector(`[data-testid="${testId}"]`)
    expect(el).toBeDefined()
    return el as HTMLButtonElement
  }

  function dialogVisible(): boolean {
    return document.body.querySelector('[data-testid="commit-suppressed-dialog"]') !== null
  }

  function skipCheckbox(): HTMLInputElement {
    const el = document.body.querySelector('[data-testid="commit-suppressed-skip-checkbox"]')
    expect(el).toBeDefined()
    return el as HTMLInputElement
  }

  it('opens the dialog on a Suppressed result and lists hooks/signing', async () => {
    stageAndDraft()
    gitMocks.commit.mockResolvedValueOnce({ suppressed: SUPPRESSED })

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(dialogVisible()).toBe(true)
    const text = document.body.textContent ?? ''
    expect(text).toContain('pre-commit')
    expect(text).toContain('commit-msg')
    expect(text).toContain('commit.gpgsign (global config)')
    // Nothing was committed and the draft survives for the decision.
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.message).toBe('feat: a')
    // A withheld commit is a decision request, not a commit: it must not land
    // in the console.
    expect(useGitPanelStore.getState().operationByProject['proj-a']).toBeUndefined()
  })

  it('closes the dialog on Cancel, leaving the commit withheld', async () => {
    stageAndDraft()
    gitMocks.commit.mockResolvedValueOnce({ suppressed: SUPPRESSED })
    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()
    expect(dialogVisible()).toBe(true)

    const cancel = Array.from(document.body.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Cancel'),
    )
    expect(cancel).toBeDefined()
    await act(async () => {
      cancel!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(dialogVisible()).toBe(false)
    expect(gitMocks.commit).toHaveBeenCalledTimes(1)
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.message).toBe('feat: a')
    expect(useGitPanelStore.getState().operationByProject['proj-a']).toBeUndefined()
  })

  it('Trust path: TrustGitRepo(workspacePath) then re-commit succeeds with hooks output', async () => {
    stageAndDraft()
    gitMocks.commit
      .mockResolvedValueOnce({ suppressed: SUPPRESSED })
      .mockResolvedValueOnce({ sha: 'trusted-sha-1234567890', output: 'hook: prettier ran\n[main trusted-s] feat: a' })
    trustMocks.trustGitRepo.mockResolvedValue(undefined)
    // The active project's workspace path (proj-a) is what trust targets.
    useProjectStore.setState({
      projects: [{
        id: 'proj-a',
        name: 'A',
        workspace_path: '/ws/proj-a',
        is_external: false,
        is_no_project: false,
        created_at: '2026-01-01T00:00:00Z',
        last_active_at: '2026-01-01T00:00:00Z',
      }],
    })

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    await act(async () => {
      dialogBtn('commit-suppressed-trust-btn').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // Trust was recorded for the ACTIVE project's workspace path…
    expect(trustMocks.trustGitRepo).toHaveBeenCalledWith('/ws/proj-a')
    // …then the commit was re-run (force=false — trust makes it run).
    expect(gitMocks.commit).toHaveBeenNthCalledWith(2, 'feat: a', false)
    expect(dialogVisible()).toBe(false)
    const rec = useGitPanelStore.getState().operationByProject['proj-a']!
    expect(rec.kind).toBe('commit')
    expect(rec.ok).toBe(true)
    expect(rec.label).toBe('Committed trusted')
    expect(rec.output).toBe('hook: prettier ran\n[main trusted-s] feat: a')
    // The successful (trusted) commit consumes the draft.
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.message).toBe('')
  })

  it('Force path: sends force=true and persists the skip flag when checked', async () => {
    stageAndDraft()
    gitMocks.commit
      .mockResolvedValueOnce({ suppressed: SUPPRESSED })
      .mockResolvedValueOnce({ sha: 'forced-sha-1234567890', output: '' })
    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // Check "Don't ask again" then force-commit (jsdom: click flips a
    // controlled checkbox and fires the change event).
    act(() => {
      skipCheckbox().click()
    })
    await act(async () => {
      dialogBtn('commit-suppressed-force-btn').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.commit).toHaveBeenNthCalledWith(2, 'feat: a', true)
    expect(dialogVisible()).toBe(false)
    // The per-project flag was persisted BEFORE the commit fires.
    expect(useGitPanelStore.getState().skipCommitSuppressByProject['proj-a']).toBe(true)
    const rec = useGitPanelStore.getState().operationByProject['proj-a']!
    expect(rec.ok).toBe(true)
    expect(rec.label).toBe('Committed forced-')
  })

  it('Force path without the checkbox leaves the flag unset', async () => {
    stageAndDraft()
    gitMocks.commit
      .mockResolvedValueOnce({ suppressed: SUPPRESSED })
      .mockResolvedValueOnce({ sha: 'forced-sha-1234567890' })
    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    await act(async () => {
      dialogBtn('commit-suppressed-force-btn').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.commit).toHaveBeenNthCalledWith(2, 'feat: a', true)
    expect(useGitPanelStore.getState().skipCommitSuppressByProject['proj-a']).toBeUndefined()
  })

  it('skip flag set → next commit sends force=true without opening the dialog', async () => {
    stageAndDraft()
    const { setSkipCommitSuppress } = useGitPanelStore.getState()
    setSkipCommitSuppress('proj-a', true)
    gitMocks.commit.mockResolvedValueOnce({ sha: 'auto-forced-sha-1234' })

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.commit).toHaveBeenCalledWith('feat: a', true)
    expect(dialogVisible()).toBe(false)
    const rec = useGitPanelStore.getState().operationByProject['proj-a']!
    expect(rec.ok).toBe(true)
    expect(rec.label).toBe('Committed auto-fo')
  })

  it('error from the trust path closes the dialog and records the failure in the console', async () => {
    stageAndDraft()
    gitMocks.commit.mockResolvedValueOnce({ suppressed: SUPPRESSED })
    trustMocks.trustGitRepo.mockRejectedValue(new Error('config not initialized'))
    useProjectStore.setState({
      projects: [{
        id: 'proj-a',
        name: 'A',
        workspace_path: '/ws/proj-a',
        is_external: false,
        is_no_project: false,
        created_at: '2026-01-01T00:00:00Z',
        last_active_at: '2026-01-01T00:00:00Z',
      }],
    })

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()
    await act(async () => {
      dialogBtn('commit-suppressed-trust-btn').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(dialogVisible()).toBe(false)
    // The trust failure is a failed commit operation → the console, not inline.
    const rec = useGitPanelStore.getState().operationByProject['proj-a']!
    expect(rec.kind).toBe('commit')
    expect(rec.ok).toBe(false)
    expect(rec.error).toBe('config not initialized')
    expect(container.textContent).not.toContain('config not initialized')
    // Only the original withheld attempt ran.
    expect(gitMocks.commit).toHaveBeenCalledTimes(1)
  })

  it('still-suppressed after trust keeps the dialog open (nested work tree)', async () => {
    stageAndDraft()
    gitMocks.commit
      .mockResolvedValueOnce({ suppressed: SUPPRESSED })
      .mockResolvedValueOnce({ suppressed: { hooks: ['pre-commit'] } })
    trustMocks.trustGitRepo.mockResolvedValue(undefined)
    useProjectStore.setState({
      projects: [{
        id: 'proj-a',
        name: 'A',
        workspace_path: '/ws/proj-a',
        is_external: false,
        is_no_project: false,
        created_at: '2026-01-01T00:00:00Z',
        last_active_at: '2026-01-01T00:00:00Z',
      }],
    })

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()
    await act(async () => {
      dialogBtn('commit-suppressed-trust-btn').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // The dialog stays open with the fresh suppression so the user can fall
    // back to the hardened commit explicitly.
    expect(dialogVisible()).toBe(true)
    expect(gitMocks.commit).toHaveBeenCalledTimes(2)
    // A still-withheld commit is recorded nowhere.
    expect(useGitPanelStore.getState().operationByProject['proj-a']).toBeUndefined()
  })
})

describe('CommitSection — commit result goes to the console', () => {
  /** Stage one file and type a draft so Commit is enabled. */
  function stageAndDraft(message = 'feat: a') {
    useGitPanelStore.setState({
      entries: [makeEntry({ path: 'a.ts', staged: true })],
      commitByProject: {
        'proj-a': {
          message,
          isGenerating: false,
          isCommitting: false,
          error: null,
        },
      },
    })
  }

  it('records a successful commit (with its output) and renders no inline surface', async () => {
    stageAndDraft()
    gitMocks.commit.mockResolvedValue({
      sha: 'live-sha-abcdef1234',
      output: '[main abc123d] hook said: clean',
    })

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const rec = useGitPanelStore.getState().operationByProject['proj-a']!
    expect(rec.kind).toBe('commit')
    expect(rec.ok).toBe(true)
    expect(rec.output).toBe('[main abc123d] hook said: clean')
    // No green "Committed" banner and no hook-output surface remain.
    expect(container.textContent).not.toContain('Committed')
    expect(container.querySelector('[data-testid="commit-output-toggle"]')).toBeNull()
    expect(container.querySelector('[data-testid="commit-output-content"]')).toBeNull()
  })

  it('records a failed commit in the console instead of an inline error', async () => {
    stageAndDraft()
    gitMocks.commit.mockRejectedValue(new Error('hook declined'))

    render()
    await act(async () => {
      commitBtn().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const rec = useGitPanelStore.getState().operationByProject['proj-a']!
    expect(rec.kind).toBe('commit')
    expect(rec.ok).toBe(false)
    expect(rec.error).toBe('hook declined')
    // The failure is not surfaced inline in the commit box.
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.error).toBeNull()
    expect(container.textContent).not.toContain('hook declined')
  })
})

