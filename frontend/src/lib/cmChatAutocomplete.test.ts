// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { EditorView } from '@codemirror/view'
import { EditorState } from '@codemirror/state'
import { startCompletion, currentCompletions } from '@codemirror/autocomplete'
import type { FileEntry } from '@/types/models'
import { useFileTreeStore } from '@/stores/fileTreeStore'

const { listDirectoryMock } = vi.hoisted(() => ({ listDirectoryMock: vi.fn() }))

vi.mock('@/api/workspace', () => ({
  listDirectory: (...args: unknown[]) => listDirectoryMock(...args),
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

import { createChatAutocomplete } from './cmChatAutocomplete'

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
})
