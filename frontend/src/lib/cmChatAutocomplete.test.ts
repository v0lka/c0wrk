// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { EditorView } from '@codemirror/view'
import { EditorState } from '@codemirror/state'
import { startCompletion, currentCompletions } from '@codemirror/autocomplete'
import type { FileEntry } from '@/types/models'
import { useFileTreeStore } from '@/stores/fileTreeStore'

const { listDirectoryMock, getSessionWorkspaceMock } = vi.hoisted(() => ({
  listDirectoryMock: vi.fn(),
  getSessionWorkspaceMock: vi.fn(),
}))

vi.mock('@/api/workspace', () => ({
  listDirectory: (...args: unknown[]) => listDirectoryMock(...args),
  getSessionWorkspace: (...args: unknown[]) => getSessionWorkspaceMock(...args),
}))
vi.mock('@/api/skills', () => ({
  listSkills: vi.fn().mockResolvedValue([]),
}))
vi.mock('@/api/agents', () => ({
  listAgents: vi.fn().mockResolvedValue([]),
}))
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn(() => () => {}),
}))
// The failure-throttling tests below deliberately reject listDirectory;
// without the mock, logger.error writes the expected failure to the real
// console and pollutes the test output.
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { createChatAutocomplete } from './cmChatAutocomplete'
import { createChatExtensions } from './cmChatExtensions'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { MutableRefObject } from 'react'
import { Compartment } from '@codemirror/state'

const ENTRIES: FileEntry[] = [
  { name: 'alpha.txt', path: '/ws/alpha.txt', is_dir: false },
  { name: 'beta', path: '/ws/beta', is_dir: true },
]

async function until(cond: () => boolean, what: string, timeoutMs = 4000): Promise<void> {
  const start = Date.now()
  while (!cond()) {
    if (Date.now() - start > timeoutMs) {
      throw new Error(`timeout waiting for: ${what}`)
    }
    await new Promise((resolve) => setTimeout(resolve, 20))
  }
}

function makeView(): { view: EditorView; host: HTMLElement } {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const view = new EditorView({
    state: EditorState.create({ doc: '', extensions: [createChatAutocomplete()] }),
    parent: host,
  })
  return { view, host }
}

function typeAndComplete(view: EditorView, text: string): void {
  view.dispatch(view.state.replaceSelection(text))
  startCompletion(view)
}

const views: { view: EditorView; host: HTMLElement }[] = []

describe('cmChatAutocomplete @-file source', () => {
  afterEach(() => {
    for (const { view, host } of views) {
      view.destroy()
      host.remove()
    }
    views.length = 0
    vi.clearAllMocks()
    useFileTreeStore.setState({ rootPath: '' })
    useSessionStore.setState({ sessions: null, activeSessionId: null })
    useProjectStore.setState({ projects: null, activeProjectId: null })
  })

  it('recovers after a transient empty listing instead of caching it until restart', async () => {
    useFileTreeStore.setState({ rootPath: '/ws' })
    const fixture = makeView()
    views.push(fixture)
    const { view } = fixture

    // Phase 1 — the workspace directory does not exist yet (No Project
    // sessions materialize it lazily). The API layer degrades the invalid
    // payload to [] without throwing. No completions may appear — and,
    // crucially, that [] must NOT be cached as a successful load.
    listDirectoryMock.mockResolvedValueOnce([])
    typeAndComplete(view, '@')
    await until(() => listDirectoryMock.mock.calls.length >= 1, 'first listDirectory call')
    expect(listDirectoryMock).toHaveBeenCalledWith('/ws', true)
    // Let the async source settle, then confirm no options were produced.
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(currentCompletions(view.state)).toEqual([])

    // Phase 2 — the directory now exists. Typing more must trigger a
    // refetch and produce @-completions WITHOUT an app restart. With the
    // old always-cache-empty behavior this never recovered.
    listDirectoryMock.mockResolvedValueOnce(ENTRIES)
    typeAndComplete(view, 'a')
    await until(
      () => currentCompletions(view.state).some((c) => c.label === 'alpha.txt'),
      '@-completions after the workspace materialized',
    )

    // Phase 3 — a non-empty listing IS cached: further triggers must not
    // refetch.
    typeAndComplete(view, 'l')
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(listDirectoryMock).toHaveBeenCalledTimes(2)
    expect(currentCompletions(view.state).some((c) => c.label === 'alpha.txt')).toBe(true)
  })

  it('resolves the completion root from the backend session workspace when stores disagree', async () => {
    // Frontend/backend desync after rapid switches: the file-tree root still
    // points at the previous project's workspace while the backend has moved
    // on. The completion must fetch the backend-authoritative root — a root
    // taken from the file tree alone fails containment on every call and
    // @-hints stay empty until an app restart.
    useFileTreeStore.setState({ rootPath: '/stale-project-ws' })
    useSessionStore.setState({ sessions: [], activeSessionId: 's1' })
    getSessionWorkspaceMock.mockResolvedValue('/current-project-ws')
    listDirectoryMock.mockResolvedValue([
      { name: 'gamma.ts', path: '/current-project-ws/gamma.ts', is_dir: false },
    ])

    const fixture = makeView()
    views.push(fixture)
    const { view } = fixture

    typeAndComplete(view, '@')
    await until(
      () => currentCompletions(view.state).some((c) => c.label === 'gamma.ts'),
      '@-completions from the backend-authoritative root',
    )
    expect(listDirectoryMock).toHaveBeenCalledWith('/current-project-ws', true)
    expect(listDirectoryMock).not.toHaveBeenCalledWith('/stale-project-ws', true)
  })

  it('resolves the completion root once per session, re-resolving only after a switch', async () => {
    // Every keystroke of an @-query re-runs the completion source; root
    // resolution must be memoized per active session instead of issuing one
    // GetSessionWorkspace RPC per trigger — and must re-resolve when the
    // active session changes.
    useSessionStore.setState({ sessions: [], activeSessionId: 's1' })
    getSessionWorkspaceMock.mockResolvedValue('/ws-one')
    listDirectoryMock.mockResolvedValue([
      { name: 'one.txt', path: '/ws-one/one.txt', is_dir: false },
    ])

    const fixture = makeView()
    views.push(fixture)
    const { view } = fixture

    typeAndComplete(view, '@')
    await until(
      () => currentCompletions(view.state).some((c) => c.label === 'one.txt'),
      'completions from the s1 root',
    )
    expect(getSessionWorkspaceMock).toHaveBeenCalledTimes(1)

    // Further triggers within the same session hit the memo — no extra RPC.
    typeAndComplete(view, 'n')
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(getSessionWorkspaceMock).toHaveBeenCalledTimes(1)
    expect(currentCompletions(view.state).some((c) => c.label === 'one.txt')).toBe(true)

    // A session switch drops the memo: the next trigger re-resolves against
    // the new session's workspace.
    useSessionStore.setState({ sessions: [], activeSessionId: 's2' })
    getSessionWorkspaceMock.mockResolvedValue('/ws-two')
    listDirectoryMock.mockResolvedValue([
      { name: 'two.txt', path: '/ws-two/two.txt', is_dir: false },
    ])
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: '@' } })
    startCompletion(view)
    await until(
      () => currentCompletions(view.state).some((c) => c.label === 'two.txt'),
      'completions from the re-resolved s2 root',
    )
    expect(getSessionWorkspaceMock).toHaveBeenCalledTimes(2)
  })

  it('refetches when the active project changes even if the tree root is unchanged', async () => {
    useFileTreeStore.setState({ rootPath: '/ws' })
    useProjectStore.setState({ projects: [], activeProjectId: 'proj-a' })
    listDirectoryMock.mockResolvedValue(ENTRIES)

    const fixture = makeView()
    views.push(fixture)
    const { view } = fixture

    typeAndComplete(view, '@')
    await until(() => currentCompletions(view.state).length > 0, 'initial completions')
    const callsAfterFirst = listDirectoryMock.mock.calls.length

    // A project switch invalidates the completion cache even when the file
    // tree has not caught up yet (e.g. FileTreePanel unmounted — collapsed
    // sidebar or a non-explorer workspace tab).
    useProjectStore.setState({ projects: [], activeProjectId: 'proj-b' })
    typeAndComplete(view, 'l')
    await until(
      () => listDirectoryMock.mock.calls.length > callsAfterFirst,
      'refetch after project switch',
    )
  })

  it('throttles refetching while the listing keeps failing, then retries after the cooldown', async () => {
    useFileTreeStore.setState({ rootPath: '/ws' })
    listDirectoryMock.mockRejectedValue(new Error('path outside project workspace'))

    const fixture = makeView()
    views.push(fixture)
    const { view } = fixture

    typeAndComplete(view, '@')
    await until(() => listDirectoryMock.mock.calls.length >= 1, 'first failing fetch')
    await new Promise((resolve) => setTimeout(resolve, 50))

    // Failures repeat per keystroke while broken — the cooldown bounds the
    // retry storm without giving up self-healing.
    typeAndComplete(view, 'a')
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(listDirectoryMock.mock.calls.length).toBe(1)

    // After the cooldown the next trigger retries and recovers.
    listDirectoryMock.mockResolvedValue(ENTRIES)
    await new Promise((resolve) => setTimeout(resolve, 1100))
    typeAndComplete(view, 'a')
    await until(
      () => currentCompletions(view.state).some((c) => c.label === 'alpha.txt'),
      'completions recover after the cooldown expires',
    )
  }, 8000)
})

describe('chat editor tooltip placement', () => {
  // The completion tooltip must render in a body-level container. When it
  // lives inside the editor, the ChatInput shell's `overflow-hidden` clips
  // it: on WebKit (WKWebView on macOS) CodeMirror falls back from
  // viewport-fixed to editor-absolute tooltip positioning after the first
  // re-measure, and an editor-inside absolute tooltip is cut off at the
  // input area boundaries — hiding most of the / @ # hint list.
  const views2: { view: EditorView; host: HTMLElement }[] = []

  afterEach(() => {
    for (const { view, host } of views2) {
      view.destroy()
      host.remove()
    }
    views2.length = 0
    vi.clearAllMocks()
    useFileTreeStore.setState({ rootPath: '' })
  })

  it('renders completion hints in a body-level container so the input shell cannot clip them', async () => {
    useFileTreeStore.setState({ rootPath: '/ws' })
    listDirectoryMock.mockResolvedValue(ENTRIES)

    const host = document.createElement('div')
    host.className = 'cm-chat-container'
    document.body.appendChild(host)
    const onSendRef: MutableRefObject<(() => void) | null> = { current: null }
    const view = new EditorView({
      state: EditorState.create({
        doc: '',
        extensions: createChatExtensions(onSendRef, new Compartment()),
      }),
      parent: host,
    })
    views2.push({ view, host })

    typeAndComplete(view, '@')
    await until(
      () => currentCompletions(view.state).length > 0,
      '@-completions to open the tooltip',
    )

    const tooltip = document.body.querySelector('.cm-tooltip-autocomplete')
    expect(tooltip).not.toBeNull()
    // The tooltip must live OUTSIDE the editor wrapper — anything inside it
    // is subject to the input shell's overflow clipping.
    expect(host.contains(tooltip as Node)).toBe(false)
    // And it must sit in CodeMirror's own container directly under <body>.
    expect(tooltip?.parentElement?.parentElement).toBe(document.body)
  })
})
