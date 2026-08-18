/**
 * Response-ordering guard for overlapping async fetches.
 *
 * Problem: when the same resource is fetched more than once in quick
 * succession (initial load + event-driven refetches, or debounced bursts),
 * the HTTP/RPC responses can resolve out of order. Applying whatever settles
 * last lets a STALE response overwrite a FRESH one — e.g. the Blackboard
 * panel losing a just-stored fact until the next unrelated event. The
 * session-switch case (fetch for the previous session landing after the
 * switch) is a variant of the same problem and is handled by a separate
 * cancellation flag in the hook.
 *
 * Usage: create one guard per fetch stream. Each fetch calls `begin()` and
 * captures the returned ticket; before applying the result (success or
 * error), check `ticket.isCurrent()` — only the most recently begun fetch
 * may apply. Discarded responses are dropped silently: a newer fetch has
 * already superseded them (and its own result will arrive).
 */

/** Opaque ticket for one in-flight fetch. */
export interface ResponseOrderTicket {
  /** True while this fetch is still the most recent one of its stream. */
  readonly isCurrent: () => boolean
}

export interface ResponseOrdering {
  /** Marks a new fetch as current and returns its ticket. */
  readonly begin: () => ResponseOrderTicket
}

/** Creates a fresh ordering guard for one fetch stream. */
export function createResponseOrdering(): ResponseOrdering {
  let latest = 0
  return {
    begin: (): ResponseOrderTicket => {
      latest++
      const id = latest
      return {
        isCurrent: (): boolean => id === latest,
      }
    },
  }
}
