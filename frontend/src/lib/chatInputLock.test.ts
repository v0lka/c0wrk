import { describe, it, expect } from 'vitest'
import { computeChatInputDisabled, computeChatPlaceholder, computeModeTogglesLocked, modeToggleLockReason } from '@/lib/chatInputLock'

// The input-lock matrix for live-send: a running task keeps the input OPEN
// (messages interject into the next LLM request); the pausing window, the
// compaction window, and the no-project state lock it.

describe('computeChatInputDisabled', () => {
  it('idle session: input enabled', () => {
    expect(
      computeChatInputDisabled({ taskActive: false, paused: false, pausing: false, isNoProject: false, compacting: false }),
    ).toBe(false)
  })

  it('running task: input enabled (live-send)', () => {
    expect(
      computeChatInputDisabled({ taskActive: true, paused: false, pausing: false, isNoProject: false, compacting: false }),
    ).toBe(false)
  })

  it('paused task: input enabled (nudge-resume)', () => {
    expect(
      computeChatInputDisabled({ taskActive: false, paused: true, pausing: false, isNoProject: false, compacting: false }),
    ).toBe(false)
  })

  it('pausing window: input disabled even though a task is running', () => {
    expect(
      computeChatInputDisabled({ taskActive: true, paused: false, pausing: true, isNoProject: false, compacting: false }),
    ).toBe(true)
  })

  it('compacting: input disabled regardless of task state', () => {
    expect(
      computeChatInputDisabled({ taskActive: false, paused: false, pausing: false, isNoProject: false, compacting: true }),
    ).toBe(true)
    expect(
      computeChatInputDisabled({ taskActive: true, paused: false, pausing: false, isNoProject: false, compacting: true }),
    ).toBe(true)
  })

  it('no project: input disabled regardless of task state', () => {
    expect(
      computeChatInputDisabled({ taskActive: false, paused: false, pausing: false, isNoProject: true, compacting: false }),
    ).toBe(true)
    expect(
      computeChatInputDisabled({ taskActive: true, paused: false, pausing: false, isNoProject: true, compacting: false }),
    ).toBe(true)
  })
})

describe('computeChatPlaceholder', () => {
  it('reflects the live-send affordance while a task runs', () => {
    const text = computeChatPlaceholder({ taskActive: true, paused: false, pausing: false, isNoProject: false, compacting: false })
    expect(text).toContain('next request')
  })

  it('reflects the pausing window', () => {
    const text = computeChatPlaceholder({ taskActive: true, paused: false, pausing: true, isNoProject: false, compacting: false })
    expect(text).toContain('Pausing')
  })

  it('reflects the compacting window', () => {
    const text = computeChatPlaceholder({ taskActive: false, paused: false, pausing: false, isNoProject: false, compacting: true })
    expect(text).toContain('Compacting')
  })

  it('reflects the paused state', () => {
    const text = computeChatPlaceholder({ taskActive: false, paused: true, pausing: false, isNoProject: false, compacting: false })
    expect(text).toContain('nudge-resume')
  })

  it('defaults to the send hint for an idle session', () => {
    const text = computeChatPlaceholder({ taskActive: false, paused: false, pausing: false, isNoProject: false, compacting: false })
    expect(text).toContain('Enter to send')
  })

  it('prioritizes no-project over everything', () => {
    const text = computeChatPlaceholder({ taskActive: true, paused: false, pausing: true, isNoProject: true, compacting: true })
    expect(text).toContain('project')
  })

  it('prioritizes compacting over pausing/paused/task states', () => {
    const text = computeChatPlaceholder({ taskActive: true, paused: false, pausing: true, isNoProject: false, compacting: true })
    expect(text).toContain('Compacting')
  })
})

// The goal/E2S mode-toggle lock: locked while ANY live task state can still be
// continued-or-abandoned by the next send (running, pausing, compacting,
// paused, lingering unfinished task); unlocked only in clean states — fresh
// session, settled task (continuation on the inherited blackboard), or a
// failed task the user cancelled.
describe('computeModeTogglesLocked', () => {
  const base = { taskActive: false, pausing: false, compacting: false, paused: false, unfinishedTaskStatus: '' }

  const locked = (overrides: Partial<typeof base>) => computeModeTogglesLocked({ ...base, ...overrides })
  const reason = (overrides: Partial<typeof base>) => modeToggleLockReason({ ...base, ...overrides })

  it('idle session (fresh first message): unlocked', () => {
    expect(locked({})).toBe(false)
    expect(reason({})).toBe('')
  })

  it('settled task (continuation after a successful completion): unlocked', () => {
    // '' is the explicit "task settled" overlay value — a follow-up send
    // continues the completed lineage, the one window where re-arming is
    // meaningful.
    expect(locked({ unfinishedTaskStatus: '' })).toBe(false)
    expect(reason({ unfinishedTaskStatus: '' })).toBe('')
  })

  it('running task: locked', () => {
    expect(locked({ taskActive: true })).toBe(true)
    expect(reason({ taskActive: true })).toBe('Locked while the session is running')
  })

  it('pausing window: locked with the running reason (the task is still live)', () => {
    expect(locked({ taskActive: true, pausing: true })).toBe(true)
    expect(reason({ taskActive: true, pausing: true })).toBe('Locked while the session is running')
  })

  it('compacting: locked with the compaction reason', () => {
    expect(locked({ compacting: true })).toBe(true)
    expect(reason({ compacting: true })).toBe('Locked while context compaction is in flight')
  })

  it('cooperatively paused: locked even though model/reasoning unlock', () => {
    expect(locked({ paused: true })).toBe(true)
    expect(reason({ paused: true })).toBe('Locked while the task is paused — resume or cancel it first')
  })

  it('failed-resumable overlay: locked with the failed reason', () => {
    expect(locked({ unfinishedTaskStatus: 'failed' })).toBe(true)
    expect(reason({ unfinishedTaskStatus: 'failed' })).toBe('Locked while a failed task awaits resume or cancel')
  })

  it('any other lingering unfinished status keeps the lock (fail-closed)', () => {
    expect(locked({ unfinishedTaskStatus: 'paused' })).toBe(true)
    expect(locked({ unfinishedTaskStatus: 'in_progress' })).toBe(true)
    expect(reason({ unfinishedTaskStatus: 'paused' })).toBe('Locked while an unfinished task is pending')
    expect(reason({ unfinishedTaskStatus: 'in_progress' })).toBe('Locked while an unfinished task is pending')
  })

  it('failed task after the user chose cancel (overlay cleared): unlocked', () => {
    expect(locked({ unfinishedTaskStatus: '' })).toBe(false)
  })

  it('compaction outranks the paused/lingering reasons (most specific flow first)', () => {
    expect(reason({ compacting: true, paused: true, unfinishedTaskStatus: 'failed' })).toBe(
      'Locked while context compaction is in flight',
    )
  })
})
