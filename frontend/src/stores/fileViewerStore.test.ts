// Tests for the file-viewer store's paper-tab action: openPaper must create a
// VIRTUAL tab keyed by the paper pseudo-path (c0wrk:paper:<slug>) so the
// file-viewer content routes it to the PaperWorkspace and never persists it.
// The last case asserts the OUTCOME (the paper pseudo-path is absent from the
// persisted snapshot), not merely the `virtual` flag, so deleting the
// `partialize` exclusion would fail the suite.

// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { useFileViewerStore } from './fileViewerStore'
import { PAPER_TAB_PREFIX, paperTabPath } from './paperStore'

describe('fileViewerStore.openPaper', () => {
  beforeEach(() => {
    useFileViewerStore.setState({ files: {}, openTabs: [], activeFile: null })
  })

  it('opens a virtual tab for the paper slug', () => {
    useFileViewerStore.getState().openPaper('vaswani-2017-attention')
    const state = useFileViewerStore.getState()
    const path = paperTabPath('vaswani-2017-attention')
    expect(path).toBe(`${PAPER_TAB_PREFIX}vaswani-2017-attention`)
    expect(state.openTabs).toEqual([path])
    expect(state.activeFile).toBe(path)
    expect(state.files[path]?.virtual).toBe(true)
  })

  it('re-activates an already-open paper tab instead of duplicating it', () => {
    useFileViewerStore.getState().openPaper('a')
    useFileViewerStore.getState().openFile('/ws/x.ts')
    useFileViewerStore.getState().openPaper('a')
    const state = useFileViewerStore.getState()
    expect(state.openTabs).toEqual([paperTabPath('a'), '/ws/x.ts'])
    expect(state.activeFile).toBe(paperTabPath('a'))
  })

  it('excludes the virtual paper tab from the persisted snapshot', () => {
    useFileViewerStore.getState().openPaper('vaswani-2017-attention')
    const path = paperTabPath('vaswani-2017-attention')

    // The in-memory state DOES hold the tab…
    expect(useFileViewerStore.getState().openTabs).toContain(path)

    // …but the persisted snapshot drops it (a restart must not rehydrate a
    // pseudo-path that has no on-disk file and no `files[…]` entry).
    const partialize = useFileViewerStore.persist.getOptions().partialize
    expect(partialize).toBeTypeOf('function')
    const persisted = partialize!(useFileViewerStore.getState()) as {
      openTabs: string[]
      activeFile: string | null
    }
    expect(persisted.openTabs).not.toContain(path)
    expect(persisted.activeFile).toBeNull()
  })
})

describe('fileViewerStore.setFileImage', () => {
  const path = '/ws/assets/plot.png'

  beforeEach(() => {
    useFileViewerStore.setState({ files: {}, openTabs: [], activeFile: null })
  })

  it('stores the data URL and clears the loading/binary/error flags', () => {
    useFileViewerStore.setState({
      openTabs: [path],
      activeFile: path,
      files: { [path]: { content: '', loading: true, isBinary: true, error: 'boom' } },
    })

    useFileViewerStore.getState().setFileImage(path, 'data:image/png;base64,AAA')

    const file = useFileViewerStore.getState().files[path]
    expect(file?.imageDataUrl).toBe('data:image/png;base64,AAA')
    expect(file?.loading).toBe(false)
    expect(file?.isBinary).toBeUndefined()
    expect(file?.error).toBeUndefined()
  })

  it('does not persist image bytes (only UI state is persisted)', () => {
    useFileViewerStore.getState().openFile(path)
    useFileViewerStore.getState().setFileImage(path, 'data:image/png;base64,AAA')

    const partialize = useFileViewerStore.persist.getOptions().partialize
    const persisted = partialize!(useFileViewerStore.getState()) as {
      openTabs: string[]
      files?: unknown
    }
    expect(persisted.files).toBeUndefined()
    expect(persisted.openTabs).toContain(path)
  })
})
