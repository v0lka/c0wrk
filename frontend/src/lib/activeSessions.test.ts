// Unit tests for lib/activeSessions.ts — pure badge aggregation helpers.

import { describe, it, expect } from 'vitest'
import {
  NO_BADGE_FLAGS,
  NO_LIVE_FLAGS,
  NO_LIVE_ROWS,
  aggregateBadgeFlags,
  deriveBadgeFlags,
  deriveLiveSessionFlags,
  hasPendingActions,
  isLiveSession,
  liveSessionsSignature,
  mergePendingOverride,
  sessionDisplayStatus,
  sortedLiveRows,
} from './activeSessions'
import type { LiveChatSnapshot, LiveSessionFlags } from './activeSessions'
import type { PendingActionsResponse } from '@/api/chat'
import type { ChatMessageUI, MessageType } from '@/types/messages'
import type { SessionInfo } from '@/types/models'

let counter = 0
function makeMsg(overrides: Partial<ChatMessageUI> & { type: MessageType }): ChatMessageUI {
  counter++
  return {
    id: `msg-${counter}`,
    sessionId: 'sess-1',
    content: '',
    timestamp: 1000 + counter,
    ...overrides,
  }
}

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: 'sess-1',
    project_id: 'p1',
    name: 'Session',
    created_at: new Date(0).toISOString(),
    last_active_at: new Date(0).toISOString(),
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: false,
    unfinished_task_status: '',
    ...overrides,
  }
}

function flags(overrides: Partial<LiveSessionFlags> = {}): LiveSessionFlags {
  return { ...NO_LIVE_FLAGS, ...overrides }
}

function emptyPending(): PendingActionsResponse {
  return { tool_confirms: [], step_limits: [], plan_approvals: [], ask_user: [], goal_proposals: [] }
}

describe('isLiveSession', () => {
  it('returns false for an idle, non-archived session', () => {
    expect(isLiveSession(makeSession(), NO_LIVE_FLAGS)).toBe(false)
  })

  it('returns true for each unfinished_task_status value', () => {
    for (const status of ['failed', 'in_progress', 'paused']) {
      expect(isLiveSession(makeSession({ unfinished_task_status: status }), NO_LIVE_FLAGS)).toBe(true)
    }
  })

  it('returns false for an explicitly empty status string', () => {
    expect(isLiveSession(makeSession({ unfinished_task_status: '' }), NO_LIVE_FLAGS)).toBe(false)
  })

  it('returns true for each live signal', () => {
    expect(isLiveSession(makeSession(), flags({ taskActive: true }))).toBe(true)
    expect(isLiveSession(makeSession(), flags({ paused: true }))).toBe(true)
    expect(isLiveSession(makeSession(), flags({ hasPendingHITL: true }))).toBe(true)
  })

  it('archived always wins — never live regardless of flags or status', () => {
    expect(isLiveSession(makeSession({ archived: true, unfinished_task_status: 'failed' }), NO_LIVE_FLAGS)).toBe(false)
    expect(isLiveSession(makeSession({ archived: true }), flags({ taskActive: true }))).toBe(false)
    expect(isLiveSession(makeSession({ archived: true }), flags({ hasPendingHITL: true }))).toBe(false)
  })
})

describe('sessionDisplayStatus — the four colors', () => {
  it('idle: nothing live, nothing unfinished', () => {
    expect(sessionDisplayStatus(makeSession(), NO_LIVE_FLAGS)).toBe('idle')
  })

  it('pending (yellow): unresolved HITL prompt', () => {
    expect(sessionDisplayStatus(makeSession(), flags({ hasPendingHITL: true }))).toBe('pending')
  })

  it('failed (red): db unfinished_task_status === "failed"', () => {
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'failed' }), NO_LIVE_FLAGS)).toBe('failed')
  })

  it('active (green): live running flag', () => {
    expect(sessionDisplayStatus(makeSession(), flags({ taskActive: true }))).toBe('active')
  })

  it('active (green): db status in_progress without live flags', () => {
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'in_progress' }), NO_LIVE_FLAGS)).toBe('active')
  })

  it('paused (gray): live paused flag', () => {
    expect(sessionDisplayStatus(makeSession(), flags({ paused: true }))).toBe('paused')
  })

  it('paused (gray): db status paused without live flags', () => {
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'paused' }), NO_LIVE_FLAGS)).toBe('paused')
  })

  it('archived renders idle even with unfinished work', () => {
    expect(sessionDisplayStatus(makeSession({ archived: true, unfinished_task_status: 'failed' }), flags({ taskActive: true }))).toBe('idle')
  })
})

describe('sessionDisplayStatus — priorities', () => {
  it('pending beats failed, active and paused', () => {
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'failed' }), flags({ hasPendingHITL: true }))).toBe('pending')
    expect(sessionDisplayStatus(makeSession(), flags({ hasPendingHITL: true, taskActive: true }))).toBe('pending')
    expect(sessionDisplayStatus(makeSession(), flags({ hasPendingHITL: true, paused: true }))).toBe('pending')
  })

  it('failed beats active and paused', () => {
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'failed' }), flags({ taskActive: true }))).toBe('failed')
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'failed' }), flags({ paused: true }))).toBe('failed')
  })

  it('active beats paused', () => {
    // Spec priority active > paused: a live running flag paints green even if
    // the session is (or its snapshot says) paused.
    expect(sessionDisplayStatus(makeSession(), flags({ taskActive: true, paused: true }))).toBe('active')
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'paused' }), flags({ taskActive: true }))).toBe('active')
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'in_progress' }), flags({ paused: true }))).toBe('active')
  })

  it('an unknown non-empty status renders active, not idle', () => {
    // Future backends may add statuses; an unfinished session must not
    // silently disappear into idle.
    expect(sessionDisplayStatus(makeSession({ unfinished_task_status: 'queued' }), NO_LIVE_FLAGS)).toBe('active')
  })
})

describe('aggregateBadgeFlags', () => {
  it('empty set: returns the stable NO_BADGE_FLAGS constant', () => {
    const result = aggregateBadgeFlags([], {})
    expect(result).toBe(NO_BADGE_FLAGS)
    expect(result).toEqual({ error: false, attention: false, active: false, paused: false, anyLive: false })
  })

  it('all-idle sessions: no flags, stable constant', () => {
    expect(aggregateBadgeFlags([makeSession({ id: 'a' }), makeSession({ id: 'b' })], {})).toBe(NO_BADGE_FLAGS)
  })

  it('error only: one failed session', () => {
    const result = aggregateBadgeFlags(
      [makeSession({ id: 'a', unfinished_task_status: 'failed' })],
      {},
    )
    expect(result).toEqual({ error: true, attention: false, active: false, paused: false, anyLive: true })
  })

  it('attention only: one session blocked on HITL', () => {
    const result = aggregateBadgeFlags([makeSession({ id: 'a' })], { a: flags({ hasPendingHITL: true }) })
    expect(result).toEqual({ error: false, attention: true, active: false, paused: false, anyLive: true })
  })

  it('active only: one running session', () => {
    const result = aggregateBadgeFlags([makeSession({ id: 'a' })], { a: flags({ taskActive: true }) })
    expect(result).toEqual({ error: false, attention: false, active: true, paused: false, anyLive: true })
  })

  it('paused only: one paused session', () => {
    const result = aggregateBadgeFlags(
      [makeSession({ id: 'a', unfinished_task_status: 'paused' })],
      {},
    )
    expect(result).toEqual({ error: false, attention: false, active: false, paused: true, anyLive: true })
  })

  it('mixed: all four flags true at once', () => {
    const result = aggregateBadgeFlags(
      [
        makeSession({ id: 'fail', unfinished_task_status: 'failed' }),
        makeSession({ id: 'hitl' }),
        makeSession({ id: 'run' }),
        makeSession({ id: 'pause', unfinished_task_status: 'paused' }),
      ],
      {
        hitl: flags({ hasPendingHITL: true }),
        run: flags({ taskActive: true }),
      },
    )
    expect(result).toEqual({ error: true, attention: true, active: true, paused: true, anyLive: true })
  })

  it('sessions missing from the live-flags map fall back to their db snapshot', () => {
    // chatStore knows nothing about the session (messages never loaded) — the
    // db in_progress status still paints it green.
    const result = aggregateBadgeFlags([makeSession({ id: 'a', unfinished_task_status: 'in_progress' })], {})
    expect(result.active).toBe(true)
  })

  it('archived sessions are excluded even with failed status', () => {
    const result = aggregateBadgeFlags([makeSession({ archived: true, unfinished_task_status: 'failed' })], {})
    expect(result).toBe(NO_BADGE_FLAGS)
  })
})

describe('deriveLiveSessionFlags', () => {
  it('empty chat state yields an empty record', () => {
    const chat: LiveChatSnapshot = { taskActive: {}, paused: {}, messageOrder: {}, messages: {} }
    expect(deriveLiveSessionFlags(chat)).toEqual({})
  })

  it('derives taskActive and paused flags per session', () => {
    const chat: LiveChatSnapshot = {
      taskActive: { 's-run': true, 's-idle': false },
      paused: { 's-paused': true },
      messageOrder: {},
      messages: {},
    }
    expect(deriveLiveSessionFlags(chat)).toEqual({
      's-run': { taskActive: true, paused: false, hasPendingHITL: false },
      's-paused': { taskActive: false, paused: true, hasPendingHITL: false },
    })
  })

  it('combines flags for one session (running + paused entries)', () => {
    const chat: LiveChatSnapshot = {
      taskActive: { 's1': true },
      paused: { 's1': true },
      messageOrder: {},
      messages: {},
    }
    expect(deriveLiveSessionFlags(chat)['s1']).toEqual({ taskActive: true, paused: true, hasPendingHITL: false })
  })

  it('detects an unresolved HITL prompt from ordered messages', () => {
    const m1 = makeMsg({ type: 'user', sessionId: 's-hitl' })
    const m2 = makeMsg({ type: 'tool_confirm', sessionId: 's-hitl' })
    const chat: LiveChatSnapshot = {
      taskActive: {},
      paused: {},
      messageOrder: { 's-hitl': [m1.id, m2.id] },
      messages: { 's-hitl': { [m1.id]: m1, [m2.id]: m2 } },
    }
    expect(deriveLiveSessionFlags(chat)['s-hitl']).toEqual({ taskActive: false, paused: false, hasPendingHITL: true })
  })

  it('resolved HITL prompts do not flag the session', () => {
    const m = makeMsg({ type: 'ask_user', metadata: { resolved: true } })
    const chat: LiveChatSnapshot = {
      taskActive: {},
      paused: {},
      messageOrder: { s: [m.id] },
      messages: { s: { [m.id]: m } },
    }
    expect(deriveLiveSessionFlags(chat)).toEqual({})
  })

  it('HITL merges with a running flag into one entry', () => {
    const m = makeMsg({ type: 'step_limit' })
    const chat: LiveChatSnapshot = {
      taskActive: { s: true },
      paused: {},
      messageOrder: { s: [m.id] },
      messages: { s: { [m.id]: m } },
    }
    expect(deriveLiveSessionFlags(chat)['s']).toEqual({ taskActive: true, paused: false, hasPendingHITL: true })
  })

  it('messageOrder without a message index contributes nothing', () => {
    const chat: LiveChatSnapshot = {
      taskActive: {},
      paused: {},
      messageOrder: { s: ['gone'] },
      messages: {},
    }
    expect(deriveLiveSessionFlags(chat)).toEqual({})
  })
})

describe('mergePendingOverride', () => {
  it('empty override returns the base reference unchanged', () => {
    const base = deriveLiveSessionFlags({ taskActive: { s: true }, paused: {}, messageOrder: {}, messages: {} })
    expect(mergePendingOverride(base, {})).toBe(base)
  })

  it('override true adds a pending entry for an unknown session', () => {
    const merged = mergePendingOverride({}, { 's-x': true })
    expect(merged['s-x']).toEqual({ taskActive: false, paused: false, hasPendingHITL: true })
  })

  it('override true ORs onto existing flags without dropping them', () => {
    const base = { s: flags({ taskActive: true }) }
    const merged = mergePendingOverride(base, { s: true })
    expect(merged['s']).toEqual({ taskActive: true, paused: false, hasPendingHITL: true })
  })

  it('override false never clears a live-derived HITL signal (OR-only merge)', () => {
    const base = { s: flags({ hasPendingHITL: true }) }
    expect(mergePendingOverride(base, { s: false })).toBe(base)
    expect(mergePendingOverride(base, { s: false })['s']?.hasPendingHITL).toBe(true)
  })

  it('returns the base reference when the override changes nothing', () => {
    const base = { s: flags({ hasPendingHITL: true }) }
    expect(mergePendingOverride(base, { s: true })).toBe(base)
  })
})

describe('hasPendingActions', () => {
  it('null response (RPC failure / malformed) counts as no pending', () => {
    expect(hasPendingActions(null)).toBe(false)
  })

  it('all-empty response is not pending', () => {
    expect(hasPendingActions(emptyPending())).toBe(false)
  })

  it('detects a pending prompt of each kind', () => {
    const kinds: Array<Partial<PendingActionsResponse>> = [
      { tool_confirms: [{ confirm_id: 'c1', tool: 'bash', args: '{}' }] },
      { step_limits: [{ request_id: 'r1', current_step: 5, max_steps: 5 }] },
      { plan_approvals: [{ request_id: 'r2', plan_path: 'p', plan_content: '' }] },
      { ask_user: [{ request_id: 'r3', questions: [] }] },
      { goal_proposals: [{ request_id: 'r4', condition: 'c', verify: 'v' }] },
    ]
    for (const kind of kinds) {
      expect(hasPendingActions({ ...emptyPending(), ...kind } as PendingActionsResponse)).toBe(true)
    }
  })
})

describe('liveSessionsSignature', () => {
  it('empty state yields the empty string', () => {
    expect(liveSessionsSignature({}, {})).toBe('')
    expect(liveSessionsSignature({ a: false }, { b: false })).toBe('')
  })

  it('unions taskActive and paused keys, sorted', () => {
    expect(liveSessionsSignature({ b: true, a: true }, { c: true })).toBe('a\nb\nc')
  })

  it('is insensitive to flag churn inside the live set', () => {
    // s1 transitions running → paused: the SET {s1} is unchanged, so the
    // signature (and thus the refresh trigger) must not change.
    expect(liveSessionsSignature({ s1: true }, {})).toBe(liveSessionsSignature({}, { s1: true }))
  })
})

describe('deriveBadgeFlags (composition)', () => {
  it('null sessions (never loaded) yields the stable no-flags constant', () => {
    expect(deriveBadgeFlags(null, { taskActive: {}, paused: {}, messageOrder: {}, messages: {} }, {})).toBe(NO_BADGE_FLAGS)
  })

  it('chains derive → merge → aggregate end to end', () => {
    const m = makeMsg({ type: 'tool_confirm', sessionId: 's2' })
    const chat: LiveChatSnapshot = {
      taskActive: { s1: true },
      paused: {},
      messageOrder: { s2: [m.id] },
      messages: { s2: { [m.id]: m } },
    }
    const result = deriveBadgeFlags(
      [makeSession({ id: 's1' }), makeSession({ id: 's2' })],
      chat,
      { s3: true }, // override for a session not even in the list — ignored
    )
    expect(result).toEqual({ error: false, attention: true, active: true, paused: false, anyLive: true })
  })

  it('the pending override paints a message-less session yellow', () => {
    // s1's messages were never loaded — only the authoritative
    // GetPendingActions override reveals the blocked prompt.
    const chat: LiveChatSnapshot = { taskActive: {}, paused: {}, messageOrder: {}, messages: {} }
    const result = deriveBadgeFlags([makeSession({ id: 's1' })], chat, { s1: true })
    expect(result).toEqual({ error: false, attention: true, active: false, paused: false, anyLive: true })
  })
})

describe('sortedLiveRows', () => {
  it('returns the stable empty list when sessions have not loaded', () => {
    expect(sortedLiveRows(null, {})).toBe(NO_LIVE_ROWS)
  })

  it('lists only live sessions with their display status', () => {
    const rows = sortedLiveRows(
      [
        makeSession({ id: 'live', unfinished_task_status: 'in_progress' }),
        makeSession({ id: 'idle' }),
        makeSession({ id: 'archived', archived: true, unfinished_task_status: 'failed' }),
      ],
      {},
    )
    expect(rows).toHaveLength(1)
    expect(rows[0]!.session.id).toBe('live')
    expect(rows[0]!.status).toBe('active')
  })

  it('sorts pending and failed first, then each group by last_active_at desc', () => {
    const t = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString()
    const rows = sortedLiveRows(
      [
        makeSession({ id: 'running-old', unfinished_task_status: 'in_progress', last_active_at: t(180) }),
        makeSession({ id: 'failed-recent', unfinished_task_status: 'failed', last_active_at: t(120) }),
        makeSession({ id: 'pending-older', last_active_at: t(60) }),
        makeSession({ id: 'paused-newest', unfinished_task_status: 'paused', last_active_at: t(30) }),
      ],
      { 'pending-older': flags({ hasPendingHITL: true }) },
    )
    expect(rows.map((r) => r.session.id)).toEqual([
      'pending-older', // urgent group, newest first (1h beats 2h)
      'failed-recent', // urgent group, older
      'paused-newest', // non-urgent group, newest first (30m beats 3h)
      'running-old',
    ])
  })

  it('falls back to NO_LIVE_FLAGS for sessions chatStore knows nothing about', () => {
    const rows = sortedLiveRows([makeSession({ id: 's1', unfinished_task_status: 'paused' })], {
      s2: flags({ taskActive: true }),
    })
    expect(rows.map((r) => r.session.id)).toEqual(['s1'])
    expect(rows[0]!.status).toBe('paused')
  })
})
