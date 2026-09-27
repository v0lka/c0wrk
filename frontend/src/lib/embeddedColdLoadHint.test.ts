// @vitest-environment node
//
// lib/embeddedColdLoadHint — the gate that decides when a service-call tooltip
// may claim a cold embedded local model is loading. The default configuration
// has NO embedded model, so the negative cases are the point of the suite: a
// remote provider must never be told a multi-gigabyte local load is under way.

import { describe, it, expect } from 'vitest'
import {
  COLD_EMBEDDED_LOAD_HINT,
  isColdEmbeddedTarget,
  serviceCallTitle,
} from './embeddedColdLoadHint'
import { makeColdEmbeddedStatus, makeEmbeddedStatus } from '@/test/embeddedStatusFixture'

describe('isColdEmbeddedTarget — the effective-model gate', () => {
  it('is false before any snapshot has been read', () => {
    expect(isColdEmbeddedTarget('embedded/Bonsai 2 27B', null)).toBe(false)
  })

  it('is false for the default configuration (nothing installed)', () => {
    expect(isColdEmbeddedTarget('anthropic/claude-sonnet', makeEmbeddedStatus())).toBe(false)
  })

  it('is false for a remote provider even with the embedded model installed cold', () => {
    expect(isColdEmbeddedTarget('anthropic/claude-sonnet', makeColdEmbeddedStatus())).toBe(false)
  })

  it('is false for an empty or null effective model', () => {
    expect(isColdEmbeddedTarget('', makeColdEmbeddedStatus())).toBe(false)
    expect(isColdEmbeddedTarget(null, makeColdEmbeddedStatus())).toBe(false)
    expect(isColdEmbeddedTarget(undefined, makeColdEmbeddedStatus())).toBe(false)
  })

  it('is true for the composite embedded id while the model is cold', () => {
    expect(isColdEmbeddedTarget('embedded/Bonsai 2 27B', makeColdEmbeddedStatus())).toBe(true)
  })

  it('is true for the bare spelling of the same model (a non-composite default_model)', () => {
    expect(isColdEmbeddedTarget('Bonsai 2 27B', makeColdEmbeddedStatus())).toBe(true)
  })

  it('is false once the embedded model is resident — the call is ordinary', () => {
    expect(
      isColdEmbeddedTarget('embedded/Bonsai 2 27B', makeColdEmbeddedStatus({ loaded: true })),
    ).toBe(false)
  })

  it('stays true while a load is already in progress', () => {
    expect(
      isColdEmbeddedTarget('embedded/Bonsai 2 27B', makeColdEmbeddedStatus({ loading: true })),
    ).toBe(true)
  })

  it('does not match a different provider exposing the same bare name', () => {
    expect(isColdEmbeddedTarget('openai/Bonsai 2 27B', makeColdEmbeddedStatus())).toBe(false)
  })
})

describe('serviceCallTitle — the pending tooltip', () => {
  it('returns the idle title while nothing is in flight', () => {
    expect(
      serviceCallTitle({
        pending: false,
        coldEmbedded: true,
        pendingLabel: 'Optimizing…',
        idleTitle: 'Optimize prompt',
      }),
    ).toBe('Optimize prompt')
  })

  it('appends the long-wait clause for a cold embedded target', () => {
    expect(
      serviceCallTitle({
        pending: true,
        coldEmbedded: true,
        pendingLabel: 'Optimizing…',
        idleTitle: 'Optimize prompt',
      }),
    ).toBe(`Optimizing… — ${COLD_EMBEDDED_LOAD_HINT}`)
  })

  it('keeps a plain pending title for every other provider', () => {
    expect(
      serviceCallTitle({
        pending: true,
        coldEmbedded: false,
        pendingLabel: 'Generating…',
        idleTitle: 'Generate commit message with AI',
      }),
    ).toBe('Generating…')
  })
})
