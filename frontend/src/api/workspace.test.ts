// @vitest-environment jsdom
//
// Tests for probePathStat in api/workspace.ts — the tri-state existence
// probe the markdown link resolver uses to rank candidate paths. The seam
// contract under test: a definitive stat verdict maps to `exists`/`missing`,
// while ANY failure (RPC error, malformed payload) maps to `unknown` — never
// to `missing`, so a failed probe can never silently advance link resolution
// to the next candidate.

import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockApp = vi.hoisted(() => ({
  StatPath: vi.fn(),
}))
// The malformed-payload case below feeds garbage on purpose and the real
// logger would print an error for it; the mock intercepts it at the source
// so the suite output stays clean AND the diagnostic itself is asserted.
const loggerMock = vi.hoisted(() => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))
vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => loggerMock)

import { probePathStat } from './workspace'

describe('probePathStat', () => {
  beforeEach(() => {
    mockApp.StatPath.mockReset()
    loggerMock.logger.error.mockClear()
  })

  it('maps an existing file to "exists"', async () => {
    mockApp.StatPath.mockResolvedValue({ exists: true, is_dir: false })
    await expect(probePathStat('/ws/docs/b.md')).resolves.toBe('exists')
    expect(mockApp.StatPath).toHaveBeenCalledWith('/ws/docs/b.md')
  })

  it('maps a missing path to "missing"', async () => {
    mockApp.StatPath.mockResolvedValue({ exists: false, is_dir: false })
    await expect(probePathStat('/ws/docs/b.md')).resolves.toBe('missing')
  })

  it('maps a directory to "exists" (the viewer surfaces the refusal)', async () => {
    // A directory at the candidate path EXISTS — resolution must open it
    // and let the viewer show its error, not treat it as absent.
    mockApp.StatPath.mockResolvedValue({ exists: true, is_dir: true })
    await expect(probePathStat('/ws/docs')).resolves.toBe('exists')
  })

  it('maps an RPC failure to "unknown", never "missing"', async () => {
    mockApp.StatPath.mockRejectedValue(new Error('backend unavailable'))
    await expect(probePathStat('/ws/docs/b.md')).resolves.toBe('unknown')
    expect(loggerMock.logger.error).not.toHaveBeenCalled()
  })

  it('maps a malformed response to "unknown" and logs it once', async () => {
    mockApp.StatPath.mockResolvedValue({ unexpected: 'shape' })
    await expect(probePathStat('/ws/docs/b.md')).resolves.toBe('unknown')
    expect(loggerMock.logger.error).toHaveBeenCalledTimes(1)
    expect(loggerMock.logger.error).toHaveBeenCalledWith(
      'probePathStat: unexpected response shape',
      { unexpected: 'shape' },
    )
  })
})
