import type { IndexPhase } from '@/types/models'

// Per-index dot state derived from top-level state + phase.
// - green  : this index is built / not currently being (re)built
// - active : this index is actively being built (spinning pulse)
// - idle   : not initialized yet
export type DotState = 'green' | 'active' | 'idle'

export interface DerivedStatus {
  vectorDot: DotState
  lexicalDot: DotState
  bothReady: boolean
}

export function deriveDotStatus(
  state: 'idle' | 'indexing' | 'ready' | 'reindexing' | 'unavailable' | 'loading',
  phase: IndexPhase | undefined,
): DerivedStatus {
  // `loading` (ADR-064): the branch-scoped DB is being opened; neither index is
  // built or being built yet, so both dots stay idle — never a "building"
  // claim for what is only an open.
  if (state === 'idle' || state === 'unavailable' || state === 'loading') {
    return { vectorDot: 'idle', lexicalDot: 'idle', bothReady: false }
  }
  if (state === 'ready') {
    return { vectorDot: 'green', lexicalDot: 'green', bothReady: true }
  }
  // indexing / reindexing
  const effective: IndexPhase = phase ?? 'both'
  const vectorDot: DotState = effective === 'lexical' ? 'green' : 'active'
  const lexicalDot: DotState = effective === 'embedding' ? 'green' : 'active'
  return { vectorDot, lexicalDot, bothReady: false }
}
