import { describe, it, expect, beforeEach } from 'vitest'
import {
  DEFAULT_AUTONOMY_MODE,
  DEFAULT_SILENT_POLICIES,
  isSilentMode,
  useAutonomyStore,
} from './autonomyStore'

describe('autonomyStore', () => {
  beforeEach(() => {
    useAutonomyStore.setState({
      autonomy_mode: DEFAULT_AUTONOMY_MODE,
      ...DEFAULT_SILENT_POLICIES,
      loaded: false,
    })
  })

  it('starts in standard mode with the documented defaults', () => {
    const s = useAutonomyStore.getState()
    expect(s.autonomy_mode).toBe('standard')
    expect(s.tool_confirm.mode).toBe('judge')
    expect(s.step_limit.mode).toBe('auto')
    expect(s.ask_user.mode).toBe('disable')
    expect(s.loaded).toBe(false)
  })

  it('setAutonomy replaces the posture and latches loaded', () => {
    useAutonomyStore.getState().setAutonomy({
      autonomy_mode: 'silent',
      tool_confirm: { mode: 'deny' },
      step_limit: { mode: 'stop' },
      ask_user: { mode: 'enable' },
    })

    const s = useAutonomyStore.getState()
    expect(s.autonomy_mode).toBe('silent')
    expect(s.tool_confirm.mode).toBe('deny')
    expect(s.step_limit.mode).toBe('stop')
    expect(s.ask_user.mode).toBe('enable')
    expect(s.loaded).toBe(true)
  })

  it('setAutonomy fills every missing field with its documented default', () => {
    useAutonomyStore.getState().setAutonomy({ autonomy_mode: 'silent' })

    const s = useAutonomyStore.getState()
    expect(s.autonomy_mode).toBe('silent')
    expect(s.tool_confirm.mode).toBe('judge')
    expect(s.step_limit.mode).toBe('auto')
    expect(s.ask_user.mode).toBe('disable')
  })

  it('setAutonomy fails safe to standard on an unrecognized mode value', () => {
    // A drifting backend value must never latch an autonomous posture the
    // config loader would reject (mirrors the backend's own fail-safe).
    useAutonomyStore.getState().setAutonomy({
      autonomy_mode: 'yolo' as unknown as 'silent',
    })
    expect(useAutonomyStore.getState().autonomy_mode).toBe('standard')
  })

  describe('isSilentMode', () => {
    it('is false in standard mode', () => {
      useAutonomyStore.getState().setAutonomy({ autonomy_mode: 'standard' })
      expect(isSilentMode()).toBe(false)
    })

    it('is false in assisted mode', () => {
      useAutonomyStore.getState().setAutonomy({ autonomy_mode: 'assisted' })
      expect(isSilentMode()).toBe(false)
    })

    it('is true in silent mode — no post-task UI interception at all', () => {
      useAutonomyStore.getState().setAutonomy({ autonomy_mode: 'silent' })
      expect(isSilentMode()).toBe(true)
    })
  })
})
