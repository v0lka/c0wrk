import type { VectorIndexStatus } from '@/types/models'

/**
 * Whether the vector-search path may fire: the query/pattern inputs, the
 * Search button, the Enter shortcut, the auto-search effect, and the result
 * count caption are all gated by this one predicate, so the widget-level
 * gate and the execution-level gates can never drift apart.
 *
 * The index must be ready — searches would otherwise wait on a readiness
 * that never returns. The ADR-122 suspension is the deliberate exception:
 * the circuit breaker stops the UPDATE pipeline only, readiness is never
 * dropped, and a live collection keeps serving queries (ADR-122 rejects
 * failing the search path on suspend), so the search path stays open while
 * the suspension reason renders inline as the honest explanation.
 */
export function vectorSearchUsable(
  status: Pick<VectorIndexStatus, 'state' | 'error_kind'>,
): boolean {
  if (status.state === 'ready') return true
  return status.state === 'unavailable' && status.error_kind === 'suspended'
}
