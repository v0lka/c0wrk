import { describe, it, expect } from 'vitest'
import { handleContextFill, handleCompactionStarted, handleCompactionFinished, type ContextFillStore } from '@/hooks/events/useContextEvents'
import type { ContextFillData } from '@/types/events'
import type { TokenInfo } from '@/types/models'

interface Recorded {
  stepFill: Array<{ sessionId: string; stepId: string; fill: number }>
  sessionTokens: Array<Partial<TokenInfo>>
}

function makeStore(): ContextFillStore & { recorded: Recorded } {
  const recorded: Recorded = { stepFill: [], sessionTokens: [] }
  return {
    recorded,
    setStepContextFill: (sessionId, stepId, fill) => { recorded.stepFill.push({ sessionId, stepId, fill }) },
    setSessionTokens: (_sessionId, tokens) => { recorded.sessionTokens.push(tokens) },
  }
}

function makeData(overrides: Partial<ContextFillData> & { plan_step_id?: string }): ContextFillData {
  return {
    fill_percent: 42.5,
    used_tokens: 8500,
    max_tokens: 20000,
    status: 'ok',
    session_input_tokens: 100,
    session_output_tokens: 50,
    model: 'qwen3.6',
    family: 'openai_compatible',
    ...overrides,
  }
}

describe('handleContextFill', () => {
  it('session-root event merges fill_percent/used_tokens/max_tokens into session tokens', () => {
    // The SetDisplayContextWindowForModel re-broadcast arrives with no
    // plan_step_id once a lazy local-model probe lands — the status bar must
    // correct immediately, not wait for the next LLM call.
    const store = makeStore()
    handleContextFill(store, 'sess-1', makeData({}))
    expect(store.recorded.sessionTokens).toHaveLength(1)
    expect(store.recorded.sessionTokens[0]).toMatchObject({
      total_input_tokens: 100,
      total_output_tokens: 50,
      model: 'qwen3.6',
      family: 'openai_compatible',
      fill_percent: 42.5,
      used_tokens: 8500,
      max_tokens: 20000,
    })
    expect(store.recorded.stepFill).toHaveLength(0)
  })

  it('step-scoped event updates only the step fill and token totals', () => {
    // A subagent step's own fill must NOT clobber the conductor's
    // session-level fill the status bar renders.
    const store = makeStore()
    handleContextFill(store, 'sess-1', makeData({ plan_step_id: 'step-9' }))
    expect(store.recorded.stepFill).toEqual([{ sessionId: 'sess-1', stepId: 'step-9', fill: 42.5 }])
    expect(store.recorded.sessionTokens).toHaveLength(1)
    expect(store.recorded.sessionTokens[0]).toEqual({
      total_input_tokens: 100,
      total_output_tokens: 50,
      model: 'qwen3.6',
      family: 'openai_compatible',
    })
  })

  it('session-root event preserves previously-known fill when fields are absent', () => {
    // The type guard (isContextFillData) requires only fill_percent+status;
    // a payload missing used_tokens/max_tokens must not overwrite the last
    // known values with 0 (store merge semantics keep them when omitted).
    const store = makeStore()
    handleContextFill(store, 'sess-1', makeData({ used_tokens: undefined, max_tokens: undefined }))
    expect(store.recorded.sessionTokens[0]).not.toHaveProperty('used_tokens')
    expect(store.recorded.sessionTokens[0]).not.toHaveProperty('max_tokens')
    expect(store.recorded.sessionTokens[0]).toMatchObject({ fill_percent: 42.5 })
  })
})

describe('handleCompactionStarted / handleCompactionFinished', () => {
  interface CompactionRecorded {
    compacting: Array<{ sessionId: string; value: boolean }>
    activity: Array<{ sessionId: string; status: string | null }>
    pausing: Array<{ sessionId: string; value: boolean }>
    paused: Array<{ sessionId: string; value: boolean }>
    taskActive: Array<{ sessionId: string; value: boolean }>
  }

  function makeCompactionStore() {
    const recorded: CompactionRecorded = { compacting: [], activity: [], pausing: [], paused: [], taskActive: [] }
    return {
      recorded,
      setCompacting: (sessionId: string, value: boolean) => { recorded.compacting.push({ sessionId, value }) },
      setActivityStatus: (sessionId: string, status: string | null) => { recorded.activity.push({ sessionId, status }) },
      setPausing: (sessionId: string, value: boolean) => { recorded.pausing.push({ sessionId, value }) },
      setPaused: (sessionId: string, value: boolean) => { recorded.paused.push({ sessionId, value }) },
      setTaskActive: (sessionId: string, value: boolean) => { recorded.taskActive.push({ sessionId, value }) },
    }
  }

  it('started locks the session and shows the Compacting activity', () => {
    const store = makeCompactionStore()
    handleCompactionStarted(store, 'sess-1')
    expect(store.recorded.compacting).toEqual([{ sessionId: 'sess-1', value: true }])
    expect(store.recorded.activity).toEqual([{ sessionId: 'sess-1', status: 'Compacting' }])
  })

  it('finished on success releases the lock and leaves the label to the auto-resume path', () => {
    const store = makeCompactionStore()
    handleCompactionFinished(store, 'sess-1', { success: true, resumed: true })
    expect(store.recorded.compacting).toEqual([{ sessionId: 'sess-1', value: false }])
    expect(store.recorded.activity).toHaveLength(0)
  })

  it('finished on success without a resume clears the activity (idle session)', () => {
    // A session with no running task has nothing to resume and no task events
    // incoming — the "Compacting" label must not linger forever.
    const store = makeCompactionStore()
    handleCompactionFinished(store, 'sess-1', { success: true, resumed: false })
    expect(store.recorded.compacting).toEqual([{ sessionId: 'sess-1', value: false }])
    expect(store.recorded.activity).toEqual([{ sessionId: 'sess-1', status: null }])
  })

  it('finished on failure releases the lock and surfaces the failure label', () => {
    const store = makeCompactionStore()
    handleCompactionFinished(store, 'sess-1', { success: false, error: 'boom' })
    expect(store.recorded.compacting).toEqual([{ sessionId: 'sess-1', value: false }])
    expect(store.recorded.activity).toEqual([{ sessionId: 'sess-1', status: 'Compaction failed' }])
  })

  it('finished on cancellation without a resume clears the activity', () => {
    const store = makeCompactionStore()
    handleCompactionFinished(store, 'sess-1', { success: false, cancelled: true, resumed: false })
    expect(store.recorded.activity).toEqual([{ sessionId: 'sess-1', status: null }])
  })

  it('finished on cancellation WITH an auto-resume keeps the label for task_resumed', () => {
    // The flow cancelled the compaction but still auto-resumed the task it
    // had paused: task_resumed ("Resuming...") owns the next label, so the
    // handler must not clear the activity here.
    const store = makeCompactionStore()
    handleCompactionFinished(store, 'sess-1', { success: false, cancelled: true, resumed: true })
    expect(store.recorded.activity).toHaveLength(0)
  })

  it('finished with a failed auto-resume re-applies the paused state', () => {
    // The flow paused the task but its auto-resume failed: session_paused was
    // suppressed while compacting, so the handler must land the paused state
    // itself (same transitions as handleSessionPausedEvent) for the
    // Resume/Stop controls to appear.
    const store = makeCompactionStore()
    handleCompactionFinished(store, 'sess-1', { success: true, resumed: false, paused_without_resume: true })
    expect(store.recorded.compacting).toEqual([{ sessionId: 'sess-1', value: false }])
    expect(store.recorded.pausing).toEqual([{ sessionId: 'sess-1', value: false }])
    expect(store.recorded.paused).toEqual([{ sessionId: 'sess-1', value: true }])
    expect(store.recorded.taskActive).toEqual([{ sessionId: 'sess-1', value: false }])
    expect(store.recorded.activity).toEqual([{ sessionId: 'sess-1', status: 'Paused' }])
  })

  it('finished with a failed auto-resume AND an error keeps the failure label', () => {
    // The compaction itself failed and the resume failed too: the paused
    // state still applies, but the label reports the failure.
    const store = makeCompactionStore()
    handleCompactionFinished(store, 'sess-1', { success: false, error: 'boom', paused_without_resume: true })
    expect(store.recorded.paused).toEqual([{ sessionId: 'sess-1', value: true }])
    expect(store.recorded.activity).toEqual([{ sessionId: 'sess-1', status: 'Compaction failed' }])
  })
})
