// Pure aggregation helpers for the global active-sessions badge (the sessions
// dropdown in the sidebar). No React, no store imports — every function here is
// unit-testable in isolation and composable at the call site:
//
//   chatStore snapshot  ──deriveLiveSessionFlags──▶ Record<id, LiveSessionFlags>
//   pendingOverride (GetPendingActions) ──mergePendingOverride──▶ effective flags
//   sessions snapshot + effective flags ──aggregateBadgeFlags──▶ BadgeFlags
//
// The DB-side snapshot (SessionInfo[]) may lag reality: it is refreshed
// debounced (activeSessionsStore.refresh) and only reflects what the backend
// persisted at query time. The LIVE side (taskActive / paused / HITL prompts)
// is read from chatStore — the single source of truth for live execution
// state, kept in sync for background sessions by useBackgroundSessionWatcher
// (same rationale as useSessionStatusIndicator).

import type { PendingActionsResponse } from '@/api/chat'
import type { ChatMessageUI } from '@/types/messages'
import type { SessionInfo } from '@/types/models'
import { hasUnresolvedHITL } from '@/lib/hitlTypes'

/** Per-session display status for the global sessions badge / switcher rows.
 *  Exactly one color per session; see sessionDisplayStatus for priorities. */
export type SessionDisplayStatus = 'pending' | 'failed' | 'active' | 'paused' | 'idle'

/** Live per-session execution flags derived from chatStore. */
export interface LiveSessionFlags {
  /** A task is currently running in this session. */
  readonly taskActive: boolean
  /** The running task is cooperatively suspended at a checkpoint. */
  readonly paused: boolean
  /** An unresolved HITL prompt (tool_confirm / ask_user / step_limit /
   *  plan_review / goal_proposal) awaits the user. */
  readonly hasPendingHITL: boolean
}

/** Stable "nothing live" flags — returned by lookups for sessions chatStore
 *  knows nothing about. A shared constant, never a fresh object, so callers
 *  can compare by reference (React #185 convention). */
export const NO_LIVE_FLAGS: LiveSessionFlags = { taskActive: false, paused: false, hasPendingHITL: false }

/** Aggregate badge state over ALL sessions (see aggregateBadgeFlags). */
export interface BadgeFlags {
  /** Red — at least one live session has a failed task. */
  readonly error: boolean
  /** Yellow — at least one session awaits a HITL response. */
  readonly attention: boolean
  /** Green — at least one session is actively processing. */
  readonly active: boolean
  /** Gray — at least one session is paused. */
  readonly paused: boolean
  /** Any of the above — a non-archived session is in a non-idle state. */
  readonly anyLive: boolean
}

/** Stable all-false badge state — returned by aggregateBadgeFlags when no
 *  session is live, so selectors can keep a referentially stable value. */
export const NO_BADGE_FLAGS: BadgeFlags = { error: false, attention: false, active: false, paused: false, anyLive: false }

/**
 * Does this session count as "live" for the badge — i.e. worth surfacing to
 * the user because something is unfinished or in flight?
 *
 * True when the session is NOT archived and ANY of:
 * - the DB says it has an unfinished task (unfinished_task_status !== ''),
 * - a task is live-running or live-paused (chatStore flags),
 * - an unresolved HITL prompt awaits the user.
 */
export function isLiveSession(session: SessionInfo, live: LiveSessionFlags): boolean {
  if (session.archived) return false
  if (live.taskActive || live.paused || live.hasPendingHITL) return true
  return session.unfinished_task_status !== ''
}

/**
 * Derive the single display status for a session.
 *
 * Priority (most urgent first): pending (yellow) > failed (red) > active
 * (green) > paused (gray) > idle.
 * - `pending` — an unresolved HITL prompt: the user's response is the next
 *   step, the most informative signal even while taskActive is still true.
 * - `failed` — the DB recorded a failed (resumable) unfinished task; beats a
 *   concurrently true running flag so a restart-race never paints failure green.
 * - `active` — running live, OR the DB says in_progress.
 * - `paused` — live-paused, OR the DB says paused.
 * - Unknown non-empty unfinished_task_status values (future backends) render
 *   as `active` — the session IS unfinished, silently showing idle would hide it.
 *
 * Archived sessions always render idle (the badge only surfaces live work).
 */
export function sessionDisplayStatus(session: SessionInfo, live: LiveSessionFlags): SessionDisplayStatus {
  if (session.archived) return 'idle'
  if (live.hasPendingHITL) return 'pending'
  const db = session.unfinished_task_status
  if (db === 'failed') return 'failed'
  if (live.taskActive || db === 'in_progress') return 'active'
  if (live.paused || db === 'paused') return 'paused'
  if (db !== '') return 'active'
  return 'idle'
}

/**
 * Fold per-session statuses into the aggregate badge flags: each flag is true
 * when at least one live session shows the corresponding status. Archived and
 * idle sessions contribute nothing. Returns the shared NO_BADGE_FLAGS constant
 * (stable reference) when nothing is live.
 *
 * `liveFlags` maps sessionId → live flags; missing entries fall back to
 * NO_LIVE_FLAGS (sessions chatStore knows nothing about are judged purely on
 * their DB snapshot).
 */
export function aggregateBadgeFlags(
  sessions: readonly SessionInfo[],
  liveFlags: Readonly<Record<string, LiveSessionFlags>>,
): BadgeFlags {
  let error = false
  let attention = false
  let active = false
  let paused = false
  for (const session of sessions) {
    const live = liveFlags[session.id] ?? NO_LIVE_FLAGS
    if (!isLiveSession(session, live)) continue
    const status = sessionDisplayStatus(session, live)
    switch (status) {
      case 'pending': attention = true; break
      case 'failed': error = true; break
      case 'active': active = true; break
      case 'paused': paused = true; break
    }
  }
  if (!error && !attention && !active && !paused) return NO_BADGE_FLAGS
  return { error, attention, active, paused, anyLive: true }
}

/** The chatStore slices deriveLiveSessionFlags needs. Structural (plain data)
 *  so tests can pass literals without React. */
export interface LiveChatSnapshot {
  readonly taskActive: Readonly<Record<string, boolean>>
  readonly paused: Readonly<Record<string, boolean>>
  readonly messageOrder: Readonly<Record<string, readonly string[]>>
  readonly messages: Readonly<Record<string, Readonly<Record<string, ChatMessageUI>>>>
}

/**
 * Derive live flags for every session chatStore has live knowledge of, in the
 * same shape useSessionStatusIndicator reads a single session (global variant):
 * taskActive / paused maps plus a HITL scan over the ordered messages.
 *
 * ⚠️ Allocates a fresh record on every call — NEVER use as a Zustand selector
 * (React #185); call it inside useMemo / plain code instead. Only sessions
 * with at least one live signal get an entry; look up with
 * `flags[id] ?? NO_LIVE_FLAGS`.
 */
export function deriveLiveSessionFlags(chat: LiveChatSnapshot): Readonly<Record<string, LiveSessionFlags>> {
  const result: Record<string, LiveSessionFlags> = {}
  for (const [sessionId, running] of Object.entries(chat.taskActive)) {
    if (running) result[sessionId] = { taskActive: true, paused: false, hasPendingHITL: false }
  }
  for (const [sessionId, isPaused] of Object.entries(chat.paused)) {
    if (!isPaused) continue
    const existing = result[sessionId]
    if (existing) {
      if (!existing.paused) result[sessionId] = { ...existing, paused: true }
    } else {
      result[sessionId] = { taskActive: false, paused: true, hasPendingHITL: false }
    }
  }
  for (const [sessionId, order] of Object.entries(chat.messageOrder)) {
    const index = chat.messages[sessionId]
    if (!index || order.length === 0) continue
    const msgs: ChatMessageUI[] = []
    for (const id of order) {
      const m = index[id]
      if (m) msgs.push(m)
    }
    if (!hasUnresolvedHITL(msgs)) continue
    const existing = result[sessionId]
    if (existing) {
      if (!existing.hasPendingHITL) result[sessionId] = { ...existing, hasPendingHITL: true }
    } else {
      result[sessionId] = { taskActive: false, paused: false, hasPendingHITL: true }
    }
  }
  return result
}

/**
 * OR-merge the authoritative GetPendingActions override into derived live
 * flags: `override[id] === true` forces hasPendingHITL on. `false` / absent
 * entries never CLEAR a live-derived HITL signal — the override only adds
 * knowledge chatStore cannot have (e.g. a session whose messages were never
 * loaded but whose task is blocked on a prompt).
 *
 * Returns the SAME reference as `base` when the merge changes nothing, so
 * memoized consumers keep referential stability.
 */
export function mergePendingOverride(
  base: Readonly<Record<string, LiveSessionFlags>>,
  override: Readonly<Record<string, boolean>>,
): Readonly<Record<string, LiveSessionFlags>> {
  let merged: Record<string, LiveSessionFlags> | null = null
  for (const [sessionId, pending] of Object.entries(override)) {
    if (!pending) continue
    const existing = base[sessionId]
    if (existing?.hasPendingHITL) continue
    if (!merged) merged = { ...base }
    merged[sessionId] = existing
      ? { ...existing, hasPendingHITL: true }
      : { taskActive: false, paused: false, hasPendingHITL: true }
  }
  return merged ?? base
}

/** True when a GetPendingActions response contains at least one pending
 *  prompt of any kind. `null` (RPC failure / malformed payload) counts as
 *  "no pending" — the override must fail open, not paint phantom yellow. */
export function hasPendingActions(response: PendingActionsResponse | null): boolean {
  if (!response) return false
  return (
    response.tool_confirms.length > 0 ||
    response.step_limits.length > 0 ||
    response.plan_approvals.length > 0 ||
    response.ask_user.length > 0 ||
    response.goal_proposals.length > 0
  )
}

/**
 * Stable string signature of the set of live session ids (taskActive or
 * paused). Changes exactly when a session STARTS or STOPS being live — i.e. on
 * task transitions (start / complete / error / cancel / pause / resume / resumable
 * failure) — and not on streaming chunks or message appends. Used as an effect
 * dependency to re-snapshot the DB list after transitions; the primitive
 * string return keeps it safe as a Zustand selector (React #185).
 */
export function liveSessionsSignature(
  taskActive: Readonly<Record<string, boolean>>,
  paused: Readonly<Record<string, boolean>>,
): string {
  const ids = new Set<string>()
  for (const [id, running] of Object.entries(taskActive)) {
    if (running) ids.add(id)
  }
  for (const [id, isPaused] of Object.entries(paused)) {
    if (isPaused) ids.add(id)
  }
  if (ids.size === 0) return ''
  return [...ids].sort().join('\n')
}

/**
 * One-shot composition for the badge: derive live flags from the chat
 * snapshot, OR-merge the pending override, aggregate over the session list.
 * Convenience entry point for the UI (all steps are individually testable).
 * `sessions` may be null (never loaded) — treated as an empty list.
 */
export function deriveBadgeFlags(
  sessions: readonly SessionInfo[] | null,
  chat: LiveChatSnapshot,
  override: Readonly<Record<string, boolean>>,
): BadgeFlags {
  const live = mergePendingOverride(deriveLiveSessionFlags(chat), override)
  return aggregateBadgeFlags(sessions ?? [], live)
}

/** One row of the active-sessions dropdown: the live session plus its
 *  derived display status. */
export interface LiveSessionRow {
  readonly session: SessionInfo
  readonly status: SessionDisplayStatus
}

/** Stable empty rows list — returned by sortedLiveRows when there is nothing
 *  live, so memoized consumers keep a referentially stable value. */
export const NO_LIVE_ROWS: readonly LiveSessionRow[] = []

/** Display statuses that sort to the top of the sessions dropdown: something
 *  needs the user (pending) or died and can be resumed (failed). */
const URGENT_STATUSES: ReadonlySet<SessionDisplayStatus> = new Set(['pending', 'failed'])

/**
 * The dropdown's row list: every live session (isLiveSession) with its
 * display status, sorted urgent-first (pending/failed), then by most recent
 * activity (last_active_at descending) within each group. `sessions` may be
 * null (never loaded) — treated as an empty list.
 */
export function sortedLiveRows(
  sessions: readonly SessionInfo[] | null,
  liveFlags: Readonly<Record<string, LiveSessionFlags>>,
): readonly LiveSessionRow[] {
  if (!sessions) return NO_LIVE_ROWS
  const rows: LiveSessionRow[] = []
  for (const session of sessions) {
    const live = liveFlags[session.id] ?? NO_LIVE_FLAGS
    if (!isLiveSession(session, live)) continue
    rows.push({ session, status: sessionDisplayStatus(session, live) })
  }
  rows.sort((a, b) => {
    const urgent = Number(URGENT_STATUSES.has(b.status)) - Number(URGENT_STATUSES.has(a.status))
    if (urgent !== 0) return urgent
    // ISO-8601 timestamps sort chronologically as plain strings.
    return b.session.last_active_at.localeCompare(a.session.last_active_at)
  })
  return rows
}
