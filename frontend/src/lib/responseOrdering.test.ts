import { describe, it, expect } from 'vitest'
import { createResponseOrdering } from './responseOrdering'

describe('createResponseOrdering', () => {
  it('the only ticket is current', () => {
    const ordering = createResponseOrdering()
    const t = ordering.begin()
    expect(t.isCurrent()).toBe(true)
  })

  it('discards the older ticket when a newer fetch begins (responses resolve out of order)', () => {
    // Regression: stale response overwrites fresh state. Ticket 1 (slow,
    // stale) resolves AFTER ticket 2 (fresh) — its result must be dropped.
    const ordering = createResponseOrdering()
    const stale = ordering.begin()
    const fresh = ordering.begin()

    expect(fresh.isCurrent()).toBe(true)
    expect(stale.isCurrent()).toBe(false)
  })

  it('keeps discarding every superseded ticket across a burst', () => {
    const ordering = createResponseOrdering()
    const tickets = Array.from({ length: 5 }, () => ordering.begin())
    const last = ordering.begin()

    expect(last.isCurrent()).toBe(true)
    for (const t of tickets) {
      expect(t.isCurrent()).toBe(false)
    }
  })

  it('tickets are independent snapshots (begin does not mutate prior tickets)', () => {
    const ordering = createResponseOrdering()
    const a = ordering.begin()
    const before = a.isCurrent()
    ordering.begin()
    const after = a.isCurrent()

    expect(before).toBe(true)
    expect(after).toBe(false)
  })

  it('guards are independent of each other', () => {
    // Two separate fetch streams (e.g. two panels) must not invalidate each
    // other's tickets.
    const orderingA = createResponseOrdering()
    const orderingB = createResponseOrdering()
    const a1 = orderingA.begin()
    orderingB.begin()
    orderingB.begin()

    expect(a1.isCurrent()).toBe(true)
  })
})
