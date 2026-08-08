import { describe, it, expect } from 'vitest'
import { classifySessionEvent } from '@/hooks/events/useSoundEvents'
import type { SessionEventKey } from '@/types/events'

describe('classifySessionEvent', () => {
  it('maps task_complete (success) to a success tone', () => {
    expect(classifySessionEvent('task_complete', { success: true })).toBe('success')
  })

  it('maps task_complete (default success) to a success tone', () => {
    // success is optional; its absence means a clean finish.
    expect(classifySessionEvent('task_complete', { output: 'done' })).toBe('success')
  })

  it('maps a degraded task_complete (success:false) to an error tone, not success', () => {
    // A partial/failed/aborted execution must NOT sound positive.
    expect(
      classifySessionEvent('task_complete', { success: false, completion: 'partial' }),
    ).toBe('error')
  })

  it('maps interactive prompts to the attention tone', () => {
    const attentionEvents: SessionEventKey[] = [
      'ask_user',
      'step_limit',
      'tool_confirm',
      'plan_review_ready',
      'task_failed_resumable',
      'goal_proposal',
    ]
    for (const event of attentionEvents) {
      expect(classifySessionEvent(event, {})).toBe('attention')
    }
  })

  it('maps error and task_cancelled to the error tone', () => {
    expect(classifySessionEvent('error', { error: 'boom' })).toBe('error')
    expect(classifySessionEvent('task_cancelled', undefined)).toBe('error')
  })

  it('returns null (silent) for progress / streaming / lifecycle events', () => {
    // These churn constantly during a run — never audible.
    const silentEvents: SessionEventKey[] = [
      'routing',
      'step_start',
      'step_complete',
      'thought',
      'tool_call',
      'tool_result',
      'assistant_chunk',
      'assistant_done',
      'plan_step_start',
      'plan_step_complete',
      'context_fill',
      'reflection',
      'goal_status',
      'goal_progress',
    ]
    for (const event of silentEvents) {
      expect(classifySessionEvent(event, {})).toBeNull()
    }
  })

  it('handles null/undefined data defensively', () => {
    expect(classifySessionEvent('task_complete', null)).toBe('success')
    expect(classifySessionEvent('task_complete', undefined)).toBe('success')
    expect(classifySessionEvent('ask_user', null)).toBe('attention')
    expect(classifySessionEvent('error', null)).toBe('error')
  })
})
