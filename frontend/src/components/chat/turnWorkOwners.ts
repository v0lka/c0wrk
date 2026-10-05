/**
 * Mapping from an anchor key (a bookmark's `event_key`, or a plan-step id) to
 * the `revealId` of the collapsible block that currently owns the DOM anchor
 * for it.
 *
 * Why this exists: the sticky-turn renderer (ChatMessageRenderer) nests a
 * turn's work items INSIDE the turn's TurnWorkBlock — a Radix collapsible.
 * Radix unmounts collapsed content, so a bookmark/plan-step anchored on a work
 * item has NO DOM anchor while the block is collapsed, and
 * ChatScrollManager's `querySelectorAll('[data-bookmark-id]')` scan misses
 * it. The scan falls back to this registry: it looks the key up here, expands
 * the owning block via {@link collapsibleRegistry}, and retries the scan once
 * the expansion has mounted the content.
 *
 * Only TOP-LEVEL work items register (ChatMessageRenderer / TurnWorkBlock):
 * nested plan_step/subagent children keep their own inline collapsibles and
 * are reachable whenever their parent is expanded, so mapping them here would
 * only widen the collapse-surface without covering anything new.
 *
 * Semantics mirror {@link collapsibleRegistry} exactly — plain `Map`, overwrite
 * on re-register, guarded unregister so a stale cleanup can never evict a
 * newer block that re-used the same key, `get` returning `undefined` when no
 * live block owns the key.
 */

const owners = new Map<string, string>()

export const turnWorkOwners = {
  /** Register (or overwrite) the owning block's revealId for `anchorKey`. */
  register(anchorKey: string, revealId: string): void {
    owners.set(anchorKey, revealId)
  },

  /**
   * Remove the registration for `anchorKey`, but only if `revealId` is still
   * the registered owner — makes register/unregister symmetric under key
   * re-use.
   */
  unregister(anchorKey: string, revealId: string): void {
    if (owners.get(anchorKey) === revealId) {
      owners.delete(anchorKey)
    }
  },

  /** The revealId of the block owning `anchorKey`, or `undefined`. */
  get(anchorKey: string): string | undefined {
    return owners.get(anchorKey)
  },
}
