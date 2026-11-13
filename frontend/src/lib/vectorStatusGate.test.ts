// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { vectorSearchUsable } from './vectorStatusGate'
import type { VectorIndexStatus } from '@/types/models'

const status = (
  state: VectorIndexStatus['state'],
  errorKind?: VectorIndexStatus['error_kind'],
): VectorIndexStatus => ({
  state,
  error_kind: errorKind,
  progress: 0,
  files_indexed: 0,
  total_files: 0,
})

describe('vectorSearchUsable', () => {
  it('is true only for ready and for the unavailable+suspended pair', () => {
    expect(vectorSearchUsable(status('ready'))).toBe(true)
    expect(vectorSearchUsable(status('unavailable', 'suspended'))).toBe(true)

    // Every non-ready state without the suspension stays gated.
    for (const state of ['idle', 'loading', 'indexing', 'reindexing', 'unavailable', 'disabled'] as const) {
      expect(vectorSearchUsable(status(state)), state).toBe(false)
    }

    // An unavailable whose kind is anything but suspended stays gated —
    // including an absent kind.
    for (const kind of [
      undefined,
      'repairable',
      'transient',
      'fatal',
      'unknown',
      'no_target',
      'shutting_down',
      'loading',
      'restarting',
    ] as const) {
      expect(vectorSearchUsable(status('unavailable', kind)), kind ?? 'missing kind').toBe(false)
    }

    // The suspended kind in a non-unavailable state is not the breaker
    // verdict (defensive: the pair is what the backend emits).
    expect(vectorSearchUsable(status('ready', 'suspended'))).toBe(true)
    expect(vectorSearchUsable(status('idle', 'suspended'))).toBe(false)
  })
})
