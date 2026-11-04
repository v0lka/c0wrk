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

export type VectorIndexState =
  'idle' | 'indexing' | 'ready' | 'reindexing' | 'unavailable' | 'loading' | 'disabled' | 'preparing' | 'migration-rebuild'

export function deriveDotStatus(
  state: VectorIndexState,
  phase: IndexPhase | undefined,
): DerivedStatus {
  // `loading` (ADR-064): the branch-scoped DB is being opened; neither index is
  // built or being built yet, so both dots stay idle — never a "building"
  // claim for what is only an open.
  // `preparing` / `migration-rebuild` (ADR-104 stage 2): the view-model attach
  // window and the legacy store promotion — pre-work or store-shape repair,
  // not an embedding/lexical pass, so the dots stay idle for them too.
  // `disabled` (ADR-103): indexing is off via the master switch — nothing is
  // built or building either; idle dots, like every other non-running state.
  if (
    state === 'idle' ||
    state === 'unavailable' ||
    state === 'loading' ||
    state === 'disabled' ||
    state === 'preparing' ||
    state === 'migration-rebuild'
  ) {
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
