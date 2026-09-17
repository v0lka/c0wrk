// @vitest-environment jsdom
// useFileViewerData — image files are fetched as data URLs, never read as text
// and never diffed. This pins the branch that routes image paths away from the
// code-viewer pipeline.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { readFileMock, readImageAsDataURLMock, getFileDiffMock, getFileDiffHunksMock } = vi.hoisted(() => ({
  readFileMock: vi.fn(),
  readImageAsDataURLMock: vi.fn(),
  getFileDiffMock: vi.fn(),
  getFileDiffHunksMock: vi.fn().mockResolvedValue([]),
}))

vi.mock('@/api/workspace', () => ({
  readFile: readFileMock,
  readImageAsDataURL: readImageAsDataURLMock,
  getFileDiff: getFileDiffMock,
}))
vi.mock('@/api/git', () => ({ getFileDiffHunks: getFileDiffHunksMock }))
vi.mock('@/api/runtime', () => ({ subscribe: vi.fn(() => () => {}) }))

import { useFileViewerData } from './useFileViewerData'
import { useFileViewerStore } from '@/stores/fileViewerStore'

function Probe({ activeFile, openTabs }: { activeFile: string | null; openTabs: string[] }) {
  useFileViewerData(activeFile, openTabs)
  return null
}

let root: Root | null = null
let container: HTMLDivElement | null = null

async function mount(path: string): Promise<void> {
  useFileViewerStore.setState({ openTabs: [path], activeFile: path, files: {} })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(<Probe activeFile={path} openTabs={[path]} />)
  })
}

beforeEach(() => {
  readFileMock.mockReset()
  readImageAsDataURLMock.mockReset()
  getFileDiffMock.mockReset()
})

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  root = null
  container = null
})

describe('useFileViewerData — image files', () => {
  it('fetches an image as a data URL and skips the text read + diff', async () => {
    const path = '/ws/assets/plot.png'
    readImageAsDataURLMock.mockResolvedValue('data:image/png;base64,AAA')

    await mount(path)

    expect(readImageAsDataURLMock).toHaveBeenCalledWith(path)
    expect(readFileMock).not.toHaveBeenCalled()
    expect(getFileDiffMock).not.toHaveBeenCalled()

    const file = useFileViewerStore.getState().files[path]
    expect(file?.imageDataUrl).toBe('data:image/png;base64,AAA')
    expect(file?.loading).toBe(false)
  })

  it('records an error when the image read fails', async () => {
    const path = '/ws/assets/broken.png'
    readImageAsDataURLMock.mockRejectedValue(new Error('file too large'))

    await mount(path)

    const file = useFileViewerStore.getState().files[path]
    expect(file?.error).toBe('file too large')
    expect(file?.imageDataUrl).toBeUndefined()
  })

  it('reads non-image files as text (no data URL)', async () => {
    const path = '/ws/src/main.ts'
    readFileMock.mockResolvedValue('const x = 1\n')

    await mount(path)

    expect(readFileMock).toHaveBeenCalledWith(path)
    expect(readImageAsDataURLMock).not.toHaveBeenCalled()

    const file = useFileViewerStore.getState().files[path]
    expect(file?.content).toBe('const x = 1\n')
    expect(file?.imageDataUrl).toBeUndefined()
  })
})
