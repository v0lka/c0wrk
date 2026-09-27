// The ONE wording of "this call may wait on a cold embedded local model", plus
// the gate that decides when saying so is true.
//
// The one-shot service calls (Optimize prompt, Generate commit message, title
// generation) have NO client-side timeout on purpose: when the effective model
// is the embedded local model, the backend first waits for a cold weight load
// (minutes) and only then generates — `ensureEmbeddedReadyForLLMRequest`. That
// gate is a NO-OP for every other provider, so the long-wait clause must be
// gated on the EFFECTIVE MODEL, never on "an RPC is in flight": telling a user
// on a remote provider that a multi-gigabyte local model is loading is the wrong
// diagnosis for an ordinary sub-second call, and it trains users to distrust the
// latency copy precisely where it matters.
//
// React-free by design (the same split as lib/embeddedModelView): the hook that
// reads the store lives in hooks/useServiceCallTitle.

import { bareModel, isCompositeModelId } from '@/lib/modelId'
import type { EmbeddedLLMStatus } from '@/api/embedded'

/** The long-wait clause of a pending service-call tooltip. A single constant
 *  shared by every surface, so the copies cannot drift apart. */
export const COLD_EMBEDDED_LOAD_HINT =
  'a cold embedded local model loads first, which can take minutes'

/** True when `effectiveModelId` names the embedded local model AND that model
 *  is not resident — the only case in which a service call blocks on a cold
 *  multi-gigabyte load.
 *
 *  False (i.e. a plain pending title) when: no snapshot has been read yet, the
 *  model is not installed, it is already `loaded` (the request is an ordinary
 *  sub-second call), or the effective model is any other provider's.
 *
 *  `llm.default_model` may be stored as a BARE name while the snapshot's
 *  `model_id` is always the composite "embedded/<name>", so a bare selector is
 *  matched against the snapshot's bare model too. */
export function isColdEmbeddedTarget(
  effectiveModelId: string | null | undefined,
  status: EmbeddedLLMStatus | null,
): boolean {
  if (status === null || !status.installed || status.loaded) return false
  if (!effectiveModelId) return false
  if (effectiveModelId === status.model_id) return true
  return !isCompositeModelId(effectiveModelId) && effectiveModelId === bareModel(status.model_id)
}

/** Build a service-call button's `title`: the idle label while nothing is in
 *  flight, the pending label once it is, with the long-wait clause appended
 *  ONLY for a cold embedded target (see the file header). */
export function serviceCallTitle(args: {
  pending: boolean
  coldEmbedded: boolean
  pendingLabel: string
  idleTitle: string
}): string {
  const { pending, coldEmbedded, pendingLabel, idleTitle } = args
  if (!pending) return idleTitle
  return coldEmbedded ? `${pendingLabel} — ${COLD_EMBEDDED_LOAD_HINT}` : pendingLabel
}
