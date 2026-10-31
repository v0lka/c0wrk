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

// Reading positions (session scope): the file-viewer twin of chatStore's
// scrollPositions — written on viewer unmount / tab re-target, restored on the
// next mount / activation. In-memory only; entries die with their tab.
describe('fileViewerStore.scrollPositions', () => {
  beforeEach(() => {
    useFileViewerStore.setState({ files: {}, openTabs: [], activeFile: null, scrollPositions: {} })
  })

  it('saves a position per path and overwrites it on a later save', () => {
    useFileViewerStore.setState({ openTabs: ['/a.ts', '/b.ts'] })
    const store = useFileViewerStore.getState()
    store.saveScrollPosition('/a.ts', { scrollTop: 4200, scrollHeight: 10000 })
    store.saveScrollPosition('/b.ts', { scrollTop: 0, scrollHeight: 500 })
    store.saveScrollPosition('/a.ts', { scrollTop: 5000, scrollHeight: 12000 })

    const positions = useFileViewerStore.getState().scrollPositions
    expect(positions['/a.ts']).toEqual({ scrollTop: 5000, scrollHeight: 12000 })
    expect(positions['/b.ts']).toEqual({ scrollTop: 0, scrollHeight: 500 })
  })

  it('keeps the map reference when the saved position is unchanged (React #185)', () => {
    useFileViewerStore.setState({ openTabs: ['/a.ts'] })
    const store = useFileViewerStore.getState()
    store.saveScrollPosition('/a.ts', { scrollTop: 4200, scrollHeight: 10000 })
    const before = useFileViewerStore.getState().scrollPositions

    store.saveScrollPosition('/a.ts', { scrollTop: 4200, scrollHeight: 10000 })

    expect(useFileViewerStore.getState().scrollPositions).toBe(before)
  })

  it('ignores saves for paths that are not open tabs (a close is never resurrected)', () => {
    // The mounted viewer's unmount/re-target save fires AFTER closeFile has
    // removed the entry — the guard must keep it removed.
    useFileViewerStore.setState({
      openTabs: ['/b.ts'],
      scrollPositions: { '/a.ts': { scrollTop: 1, scrollHeight: 10 } },
    })
    const before = useFileViewerStore.getState().scrollPositions

    useFileViewerStore.getState().saveScrollPosition('/a.ts', { scrollTop: 99, scrollHeight: 100 })

    expect(useFileViewerStore.getState().scrollPositions).toBe(before)
    expect(useFileViewerStore.getState().scrollPositions['/a.ts'])
      .toEqual({ scrollTop: 1, scrollHeight: 10 })
  })

  it('drops the closed tab position on closeFile but keeps other tabs', () => {
    useFileViewerStore.getState().openFile('/a.ts')
    useFileViewerStore.getState().openFile('/b.ts')
    useFileViewerStore.getState().saveScrollPosition('/a.ts', { scrollTop: 42, scrollHeight: 100 })
    useFileViewerStore.getState().saveScrollPosition('/b.ts', { scrollTop: 7, scrollHeight: 200 })

    useFileViewerStore.getState().closeFile('/a.ts')

    const positions = useFileViewerStore.getState().scrollPositions
    expect(positions['/a.ts']).toBeUndefined()
    expect(positions['/b.ts']).toEqual({ scrollTop: 7, scrollHeight: 200 })
  })

  it('keeps only the surviving tab position on closeOthersFiles', () => {
    useFileViewerStore.getState().openFile('/a.ts')
    useFileViewerStore.getState().openFile('/b.ts')
    useFileViewerStore.getState().saveScrollPosition('/a.ts', { scrollTop: 42, scrollHeight: 100 })
    useFileViewerStore.getState().saveScrollPosition('/b.ts', { scrollTop: 7, scrollHeight: 200 })

    useFileViewerStore.getState().closeOthersFiles('/b.ts')

    expect(useFileViewerStore.getState().scrollPositions).toEqual({
      '/b.ts': { scrollTop: 7, scrollHeight: 200 },
    })
  })

  it('resets all positions on closeAllFiles', () => {
    useFileViewerStore.getState().openFile('/a.ts')
    useFileViewerStore.getState().saveScrollPosition('/a.ts', { scrollTop: 42, scrollHeight: 100 })

    useFileViewerStore.getState().closeAllFiles()

    expect(useFileViewerStore.getState().scrollPositions).toEqual({})
  })

  it('prunes stale entries on restoreProjectFiles, keeping the live reference when nothing is stale', () => {
    // A stale /a.ts entry next to the restored /b.ts tab set → pruned.
    useFileViewerStore.setState({
      openTabs: ['/b.ts'],
      activeFile: '/b.ts',
      files: { '/b.ts': { content: '', loading: false } },
      scrollPositions: {
        '/a.ts': { scrollTop: 5, scrollHeight: 50 },
        '/b.ts': { scrollTop: 6, scrollHeight: 60 },
      },
    })

    useFileViewerStore.getState().restoreProjectFiles(['/b.ts'], '/b.ts')

    expect(useFileViewerStore.getState().scrollPositions).toEqual({
      '/b.ts': { scrollTop: 6, scrollHeight: 60 },
    })

    // A restore that only ADDS a tab (nothing stale) keeps the same map
    // reference (reference-stable no-op for the positions slice).
    const before = useFileViewerStore.getState().scrollPositions
    useFileViewerStore.getState().restoreProjectFiles(['/b.ts', '/c.ts'], '/b.ts')
    expect(useFileViewerStore.getState().scrollPositions).toBe(before)
  })

  it('never persists reading positions (session scope, like the chat)', () => {
    useFileViewerStore.getState().openFile('/a.ts')
    useFileViewerStore.getState().saveScrollPosition('/a.ts', { scrollTop: 42, scrollHeight: 100 })

    const partialize = useFileViewerStore.persist.getOptions().partialize
    const persisted = partialize!(useFileViewerStore.getState()) as Record<string, unknown>
    expect('scrollPositions' in persisted).toBe(false)
  })
})
