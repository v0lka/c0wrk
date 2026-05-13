import { describe, it, expect, beforeEach } from 'vitest'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'

function resetStore() {
  useVectorIndexStore.getState().reset()
  useVectorIndexStore.getState().setMode('hybrid')
}

describe('vectorIndexStore', () => {
  beforeEach(() => {
    resetStore()
  })

  it('defaults mode to hybrid', () => {
    expect(useVectorIndexStore.getState().mode).toBe('hybrid')
  })

  it('setMode switches between hybrid/vector/lexical', () => {
    const { setMode } = useVectorIndexStore.getState()
    setMode('vector')
    expect(useVectorIndexStore.getState().mode).toBe('vector')
    setMode('lexical')
    expect(useVectorIndexStore.getState().mode).toBe('lexical')
  })

  it('addMustMatch appends unique trimmed tokens', () => {
    const { addMustMatch } = useVectorIndexStore.getState()
    addMustMatch('  alpha  ')
    addMustMatch('beta')
    addMustMatch('alpha') // duplicate
    addMustMatch('   ')   // blank
    expect(useVectorIndexStore.getState().mustMatch).toEqual(['alpha', 'beta'])
  })

  it('removeMustMatch removes a token by exact value', () => {
    const { addMustMatch, removeMustMatch } = useVectorIndexStore.getState()
    addMustMatch('alpha')
    addMustMatch('beta')
    addMustMatch('gamma')
    removeMustMatch('beta')
    expect(useVectorIndexStore.getState().mustMatch).toEqual(['alpha', 'gamma'])
    // removing a non-existent token is a no-op
    removeMustMatch('missing')
    expect(useVectorIndexStore.getState().mustMatch).toEqual(['alpha', 'gamma'])
  })

  it('setPhase updates phase and is surfaced to consumers', () => {
    const { setPhase } = useVectorIndexStore.getState()
    setPhase('embedding')
    expect(useVectorIndexStore.getState().phase).toBe('embedding')
    setPhase('lexical')
    expect(useVectorIndexStore.getState().phase).toBe('lexical')
    setPhase(undefined)
    expect(useVectorIndexStore.getState().phase).toBeUndefined()
  })

  it('clearFilter clears query, filePattern, and mustMatch but preserves mode', () => {
    const { setQuery, setFilePattern, addMustMatch, setMode, clearFilter } = useVectorIndexStore.getState()
    setQuery('hello')
    setFilePattern('**/*.go')
    addMustMatch('alpha')
    setMode('lexical')
    clearFilter()
    const s = useVectorIndexStore.getState()
    expect(s.query).toBe('')
    expect(s.filePattern).toBe('')
    expect(s.mustMatch).toEqual([])
    expect(s.mode).toBe('lexical')
  })
})
