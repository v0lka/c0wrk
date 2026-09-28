/**
 * Module-level registry mapping a collapsible's `revealId` (the same stable id
 * that drives {@link chatHoverStore}'s chevron reveal) to that collapsible's
 * `setOpen` callback.
 *
 * Purpose: let outside code (e.g. a "collapse all" sweep) programmatically
 * collapse a block without threading callbacks through props. The registry
 * deliberately does NOT hold React state — only references to the live
 * `setOpen` callbacks, which {@link CollapsibleBlock} registers in a
 * `useEffect` keyed by its `chevronId` and unregisters on cleanup.
 *
 * Semantics:
 * - `register(id, setOpen)` overwrites any previous entry under the same id
 *   (a re-mount/re-register with an identical id replaces the stale callback).
 * - `unregister(id, setOpen)` removes the entry ONLY if the registered
 *   callback is still `setOpen` — so a late cleanup from an unmounted block
 *   cannot unregister a newer block that re-used the id.
 * - `get(id)` returns the registered callback or `undefined` when no live
 *   block currently owns that id.
 */

export type CollapsibleSetOpen = (open: boolean) => void

const registry = new Map<string, CollapsibleSetOpen>()

export const collapsibleRegistry = {
  /**
   * Register (or overwrite) the `setOpen` callback for `id`.
   * Re-registering the same id replaces the previous callback — the newest
   * live block owning an id wins.
   */
  register(id: string, setOpen: CollapsibleSetOpen): void {
    registry.set(id, setOpen)
  },

  /**
   * Remove the registration for `id`, but only if `setOpen` is still the
   * registered callback. This makes register/unregister symmetric even when
   * ids are re-used: a stale cleanup can never evict a newer registration.
   */
  unregister(id: string, setOpen: CollapsibleSetOpen): void {
    if (registry.get(id) === setOpen) {
      registry.delete(id)
    }
  },

  /** The registered `setOpen` for `id`, or `undefined` when none is live. */
  get(id: string): CollapsibleSetOpen | undefined {
    return registry.get(id)
  },
}
