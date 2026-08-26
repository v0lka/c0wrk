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
import { createChatExtensions } from './cmChatExtensions'
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
