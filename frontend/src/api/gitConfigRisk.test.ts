// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'

const onGlobalEventMock = vi.fn()
const reportDroppedEventMock = vi.fn()

vi.mock('./runtime', () => ({
  onGlobalEvent: (...args: unknown[]) => onGlobalEventMock(...(args as [])) as never,
  getApp: vi.fn(),
  reportDroppedEvent: (...args: unknown[]) => reportDroppedEventMock(...(args as [])) as never,
}))

import { onGitConfigRisk } from './gitConfigRisk'
import type { GitConfigRiskData } from '@/types/events'

const valid: GitConfigRiskData = {
  path: '/repo',
  source: 'project',
  notice: 'Repository-defined git hooks do not run inside c0wrk.',
  findings: [{ key: 'core.fsmonitor', description: 'runs a monitor command' }],
}

beforeEach(() => {
  onGlobalEventMock.mockReset()
  reportDroppedEventMock.mockReset()
  onGlobalEventMock.mockImplementation((_event: string, handler: (data: unknown) => void) => handler)
})

describe('onGitConfigRisk payload validation', () => {
  it('reports malformed payloads instead of dropping them silently (finding [33])', () => {
    const cb = vi.fn()
    const handler = onGitConfigRisk(cb) as unknown as (data: unknown) => void

    handler({ not: 'valid' })

    expect(cb).not.toHaveBeenCalled()
    expect(reportDroppedEventMock).toHaveBeenCalledTimes(1)
    expect(reportDroppedEventMock).toHaveBeenCalledWith('project:git_config_risk', { not: 'valid' })
  })

  it('reports a null payload and still does not invoke the callback', () => {
    const cb = vi.fn()
    const handler = onGitConfigRisk(cb) as unknown as (data: unknown) => void

    handler(null)

    expect(cb).not.toHaveBeenCalled()
    expect(reportDroppedEventMock).toHaveBeenCalledWith('project:git_config_risk', null)
  })

  it('passes valid payloads through untouched', () => {
    const cb = vi.fn()
    const handler = onGitConfigRisk(cb) as unknown as (data: unknown) => void

    handler(valid)

    expect(cb).toHaveBeenCalledWith(valid)
    expect(reportDroppedEventMock).not.toHaveBeenCalled()
  })
})
