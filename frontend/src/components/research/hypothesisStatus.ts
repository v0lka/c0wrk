// Hypothesis status lifecycle shared by every research UI surface that lets
// the user flip a status (the workspace detail card's select, the
// ResearchQuickMutate active-front dropdowns).
//
// Mirrors the backend transition state machine in core/research/writer.go
// (`transitions`): a status may move only to its listed targets, and terminal
// statuses (confirmed / refuted / cancelled) have no outgoing transitions —
// backward jumps (in-progress → open) and skips (open → confirmed) are
// rejected server-side, so the UI must not offer them.
//
// Pure data + a pure helper — no React/DOM dependencies, unit-testable in
// isolation.
import type { HypothesisStatus } from '@/types/models'

export const STATUS_TRANSITIONS: Record<HypothesisStatus, readonly HypothesisStatus[]> = {
  open: ['in-progress', 'cancelled'],
  'in-progress': ['confirmed', 'refuted', 'cancelled'],
  confirmed: [],
  refuted: [],
  cancelled: [],
}

// statusOptions returns the current status (so the controlled <select> always
// has a matching option) followed by its legal transition targets, hiding
// illegal jumps (e.g. open → confirmed) that the backend would reject. The
// current status is typed as string because a node/draft may carry a
// non-canonical value from an older backend; unknown statuses fall back to no
// outgoing transitions.
export function statusOptions(current: string): string[] {
  const targets = STATUS_TRANSITIONS[current as HypothesisStatus] ?? []
  return [current, ...targets.filter((s) => s !== current)]
}
