// Tests for the file-viewer store's paper-tab action: openPaper must create a
// VIRTUAL tab keyed by the paper pseudo-path (c0wrk:paper:<slug>) so the
// file-viewer content routes it to the PaperWorkspace and never persists it.

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
})
