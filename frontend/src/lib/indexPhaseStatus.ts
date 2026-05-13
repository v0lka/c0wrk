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
  state: 'idle' | 'indexing' | 'ready' | 'reindexing',
  phase: IndexPhase | undefined,
): DerivedStatus {
  if (state === 'idle') {
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
