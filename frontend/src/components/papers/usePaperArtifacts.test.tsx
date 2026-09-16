// @vitest-environment jsdom
//
// usePaperArtifacts — the paper workspace's artifact loader. These tests cover
// the refresh-key contract: a library sync (a `papers:changed` refetch after the
// study-paper skill appends sections on a deepen) re-lists the directory and
// rebuilds the section set, while never dropping the sections already loaded.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { listDirectoryMock, readFileMock } = vi.hoisted(() => ({
  listDirectoryMock: vi.fn(),
  readFileMock: vi.fn(),
}))

vi.mock('@/api/workspace', () => ({
  listDirectory: listDirectoryMock,
  readFile: readFileMock,
}))

import { usePaperArtifacts } from './usePaperArtifacts'

function entry(name: string) {
  return { name, path: `/ws/.research/papers/demo/${name}`, is_dir: false }
}

function Harness({ dir, refreshKey }: { dir: string; refreshKey: number }) {
  const artifacts = usePaperArtifacts(dir, refreshKey)
  return (
    <div>
      <span data-testid="note">{artifacts.note.content}</span>
      <span data-testid="note-missing">{String(artifacts.note.missing)}</span>
      <span data-testid="literature">{artifacts.literature.content}</span>
      <span data-testid="literature-missing">{String(artifacts.literature.missing)}</span>
    </div>
  )
}

const DIR = '/ws/.research/papers/demo'

let root: Root | null = null
let container: HTMLDivElement | null = null

async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

async function render(refreshKey: number): Promise<void> {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(<Harness dir={DIR} refreshKey={refreshKey} />)
  })
  await settle()
}

async function rerender(refreshKey: number): Promise<void> {
  await act(async () => {
    root!.render(<Harness dir={DIR} refreshKey={refreshKey} />)
  })
  await settle()
}

function text(testId: string): string {
  return document.querySelector(`[data-testid="${testId}"]`)?.textContent ?? ''
}

beforeEach(() => {
  listDirectoryMock.mockReset()
  readFileMock.mockReset()
})

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  root = null
  container = null
})

describe('usePaperArtifacts — refresh key', () => {
  it('lists once and reads only the present artifacts', async () => {
    listDirectoryMock.mockResolvedValue([entry('note.md')])
    readFileMock.mockResolvedValue('Note body')

    await render(0)

    expect(listDirectoryMock).toHaveBeenCalledTimes(1)
    expect(text('note')).toBe('Note body')
    expect(text('note-missing')).toBe('false')
    // literature.md is absent → the section degrades to missing (no read).
    expect(text('literature-missing')).toBe('true')
  })

  it('re-lists and rebuilds sections when the key changes (deepen appended one)', async () => {
    listDirectoryMock.mockResolvedValueOnce([entry('note.md')])
    readFileMock.mockResolvedValue('Note v1')

    await render(0)
    expect(listDirectoryMock).toHaveBeenCalledTimes(1)
    expect(text('note')).toBe('Note v1')

    // The skill appended literature.md and the library synced (key bump).
    listDirectoryMock.mockResolvedValueOnce([entry('note.md'), entry('literature.md')])
    readFileMock.mockImplementation(async (path: string) =>
      path.endsWith('literature.md') ? 'Literature appended' : 'Note v1',
    )

    await rerender(1)

    expect(listDirectoryMock).toHaveBeenCalledTimes(2)
    // The section written before the deepen survives…
    expect(text('note')).toBe('Note v1')
    // …and the appended one is built out on top of it.
    expect(text('literature')).toBe('Literature appended')
    expect(text('literature-missing')).toBe('false')
  })
})
