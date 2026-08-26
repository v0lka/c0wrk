import { describe, it, expect } from 'vitest'
import { computeChatInputDisabled, computeChatPlaceholder } from '@/lib/chatInputLock'

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
