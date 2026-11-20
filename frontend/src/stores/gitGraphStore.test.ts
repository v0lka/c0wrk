// Unit tests for the app-session expansion store of the file viewer's
// Git History page (gitGraphStore).
//
// Plain node environment — no persist middleware, nothing touches
// localStorage; each test starts from an empty byProject map.
import { describe, it, expect, beforeEach } from 'vitest'
import { useGitGraphStore } from './gitGraphStore'

beforeEach(() => {
  useGitGraphStore.setState({ byProject: {} })
})

describe('useGitGraphStore — card expansion', () => {
  it('marks one card expanded per project', () => {
    useGitGraphStore.getState().expandCard('p1', 'aaa')
    expect(useGitGraphStore.getState().byProject['p1']).toEqual({
      expandedSha: 'aaa',
      expandedFiles: {},
    })
  })

  it('expanding a second card replaces the first', () => {
    useGitGraphStore.getState().expandCard('p1', 'aaa')
    useGitGraphStore.getState().expandCard('p1', 'bbb')
    expect(useGitGraphStore.getState().byProject['p1']?.expandedSha).toBe('bbb')
  })

  it('collapseCard clears the card but keeps the per-commit file sets', () => {
    useGitGraphStore.getState().expandCard('p1', 'aaa')
    useGitGraphStore.getState().toggleFile('p1', 'aaa', 'src/a.ts')
    useGitGraphStore.getState().collapseCard('p1')
    const entry = useGitGraphStore.getState().byProject['p1']
    expect(entry?.expandedSha).toBeNull()
    expect(entry?.expandedFiles['aaa']?.has('src/a.ts')).toBe(true)
  })

  it('collapseCard is a no-op when nothing is expanded', () => {
    useGitGraphStore.getState().collapseCard('p1')
    expect(useGitGraphStore.getState().byProject['p1']).toBeUndefined()
  })
})

describe('useGitGraphStore — file toggles', () => {
  it('toggles one file open inside a commit card', () => {
    useGitGraphStore.getState().expandCard('p1', 'aaa')
    useGitGraphStore.getState().toggleFile('p1', 'aaa', 'src/a.ts')
    expect(useGitGraphStore.getState().byProject['p1']?.expandedFiles['aaa']).toEqual(new Set(['src/a.ts']))
  })

  it('toggling the same path again closes it', () => {
    useGitGraphStore.getState().expandCard('p1', 'aaa')
    useGitGraphStore.getState().toggleFile('p1', 'aaa', 'src/a.ts')
    useGitGraphStore.getState().toggleFile('p1', 'aaa', 'src/a.ts')
    expect(useGitGraphStore.getState().byProject['p1']?.expandedFiles['aaa']?.size).toBe(0)
  })

  it('keeps file sets per commit and does not touch the expanded card', () => {
    useGitGraphStore.getState().expandCard('p1', 'aaa')
    useGitGraphStore.getState().toggleFile('p1', 'aaa', 'src/a.ts')
    useGitGraphStore.getState().toggleFile('p1', 'bbb', 'README.md')
    const files = useGitGraphStore.getState().byProject['p1']?.expandedFiles
    expect(files?.['aaa']).toEqual(new Set(['src/a.ts']))
    expect(files?.['bbb']).toEqual(new Set(['README.md']))
    expect(useGitGraphStore.getState().byProject['p1']?.expandedSha).toBe('aaa')
  })
})

describe('useGitGraphStore — per-project scoping', () => {
  it('isolates expansion state per project', () => {
    useGitGraphStore.getState().expandCard('p1', 'aaa')
    useGitGraphStore.getState().toggleFile('p1', 'aaa', 'src/a.ts')
    expect(useGitGraphStore.getState().byProject['p2']).toBeUndefined()
    expect(useGitGraphStore.getState().byProject['p1']?.expandedSha).toBe('aaa')
  })

  it('carries existing file sets over when a new card expands', () => {
    useGitGraphStore.getState().expandCard('p1', 'aaa')
    useGitGraphStore.getState().toggleFile('p1', 'aaa', 'src/a.ts')
    useGitGraphStore.getState().expandCard('p1', 'bbb')
    const entry = useGitGraphStore.getState().byProject['p1']
    expect(entry?.expandedSha).toBe('bbb')
    expect(entry?.expandedFiles['aaa']?.has('src/a.ts')).toBe(true)
  })
})

describe('useGitGraphStore — empty scope guard', () => {
  it('drops every decision when there is no active project', () => {
    useGitGraphStore.getState().expandCard('', 'aaa')
    useGitGraphStore.getState().toggleFile('', 'aaa', 'src/a.ts')
    expect(useGitGraphStore.getState().byProject['']).toBeUndefined()

    // Even a pre-existing entry (never produced by the store itself) is
    // left untouched by an empty-scope collapse.
    useGitGraphStore.setState({
      byProject: { '': { expandedSha: 'aaa', expandedFiles: {} } },
    })
    useGitGraphStore.getState().collapseCard('')
    expect(useGitGraphStore.getState().byProject['']?.expandedSha).toBe('aaa')
  })
})
