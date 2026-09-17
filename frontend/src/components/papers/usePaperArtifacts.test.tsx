// @vitest-environment jsdom
//
// usePaperArtifacts — the paper workspace's artifact loader. These tests cover
// the refresh-key contract: a library sync (a `papers:changed` refetch after the
// study-paper skill appends sections on a deepen) re-lists the directory and
// rebuilds the section set, while KEEPING the sections already loaded for the
// whole duration of the async re-read (the regression detector asserts the
// intermediate frame, not just the settled state — see Issue 31). They also
// cover the honest candidate list (only files a producer writes) and the
// directory-listing failure → error state (Issue 57).

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
  const a = usePaperArtifacts(dir, refreshKey)
  return (
    <div>
      <span data-testid="note">{a.note.content}</span>
      <span data-testid="note-missing">{String(a.note.missing)}</span>
      <span data-testid="note-error">{String(a.note.error)}</span>
      <span data-testid="appraisal">{a.appraisal.content}</span>
      <span data-testid="appraisal-missing">{String(a.appraisal.missing)}</span>
      <span data-testid="literature-missing">{String(a.literature.missing)}</span>
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

async function mount(refreshKey: number): Promise<void> {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
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

describe('usePaperArtifacts', () => {
  it('lists once and reads only the artifacts a producer actually writes', async () => {
    listDirectoryMock.mockResolvedValue([entry('note.md')])
    readFileMock.mockResolvedValue('Note body')

    await mount(0)

    expect(listDirectoryMock).toHaveBeenCalledTimes(1)
    expect(text('note')).toBe('Note body')
    expect(text('note-missing')).toBe('false')
    // The optional sections (source.md / literature.md / a per-paper comparison)
    // have no candidate file in this directory, so they degrade to "no artifact"
    // and are never read.
    expect(text('literature-missing')).toBe('true')
    expect(readFileMock).toHaveBeenCalledTimes(1)
  })

  it('keeps the loaded sections while a refreshKey re-run is in flight (Issues 6/31)', async () => {
    listDirectoryMock.mockResolvedValueOnce([entry('note.md')])
    readFileMock.mockResolvedValue('Note v1')

    await mount(0)
    expect(text('note')).toBe('Note v1')

    // Gate the refresh's directory listing so the frame BETWEEN the key bump and
    // the resolve is observable. A blank-then-reload implementation (the Issue 6
    // regression) would have emptied the section here, and this assertion fails.
    let releaseListing: (value: unknown) => void = () => {}
    listDirectoryMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          releaseListing = resolve
        }),
    )
    readFileMock.mockImplementation(async (path: string) =>
      path.endsWith('appraisal.md') ? 'Appraisal v2' : 'Note v1',
    )

    await act(async () => {
      root!.render(<Harness dir={DIR} refreshKey={1} />)
    })
    // Mid-refresh: the previously loaded content must still be visible.
    expect(text('note')).toBe('Note v1')

    await act(async () => {
      releaseListing([entry('note.md'), entry('appraisal.md')])
      await new Promise((r) => setTimeout(r, 0))
    })

    // The section written before the refresh survives, and the appended one is
    // built out on top of it.
    expect(text('note')).toBe('Note v1')
    expect(text('appraisal')).toBe('Appraisal v2')
    expect(text('appraisal-missing')).toBe('false')
  })

  it('surfaces a directory-listing failure as an error, not as "no artifacts" (Issue 57)', async () => {
    listDirectoryMock.mockRejectedValueOnce(new Error('EACCES: permission denied'))
    readFileMock.mockResolvedValue('should not be read')

    await mount(0)

    expect(text('note-missing')).toBe('false')
    expect(text('note-error')).toBe('EACCES: permission denied')
    expect(readFileMock).not.toHaveBeenCalled()
  })
})
