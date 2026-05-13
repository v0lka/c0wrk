import { describe, it, expect } from 'vitest'
import { deriveDotStatus } from '@/lib/indexPhaseStatus'

describe('deriveDotStatus', () => {
  it('returns both idle when state is idle', () => {
    expect(deriveDotStatus('idle', undefined)).toEqual({
      vectorDot: 'idle',
      lexicalDot: 'idle',
      bothReady: false,
    })
  })

  it('returns both green and ready when state is ready', () => {
    expect(deriveDotStatus('ready', undefined)).toEqual({
      vectorDot: 'green',
      lexicalDot: 'green',
      bothReady: true,
    })
  })

  it('shows vector active and lexical active when phase=both', () => {
    expect(deriveDotStatus('indexing', 'both')).toEqual({
      vectorDot: 'active',
      lexicalDot: 'active',
      bothReady: false,
    })
  })

  it('shows vector active and lexical green when phase=embedding', () => {
    expect(deriveDotStatus('indexing', 'embedding')).toEqual({
      vectorDot: 'active',
      lexicalDot: 'green',
      bothReady: false,
    })
  })

  it('shows vector green and lexical active when phase=lexical', () => {
    expect(deriveDotStatus('reindexing', 'lexical')).toEqual({
      vectorDot: 'green',
      lexicalDot: 'active',
      bothReady: false,
    })
  })

  it('defaults to phase=both when phase is undefined during indexing', () => {
    expect(deriveDotStatus('indexing', undefined)).toEqual({
      vectorDot: 'active',
      lexicalDot: 'active',
      bothReady: false,
    })
  })
})
