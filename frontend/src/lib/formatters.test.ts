import { describe, it, expect } from 'vitest'
import { formatDuration, formatIdleCountdown, formatTokenCount, formatVersion } from './formatters'

describe('formatDuration', () => {
  it('returns "0ms" for 0', () => {
    expect(formatDuration(0)).toBe('0ms')
  })

  it('returns "0ms" for negative numbers', () => {
    expect(formatDuration(-1)).toBe('0ms')
    expect(formatDuration(-1000)).toBe('0ms')
  })

  it('returns "0ms" for NaN', () => {
    expect(formatDuration(NaN)).toBe('0ms')
  })

  it('returns "0ms" for Infinity', () => {
    expect(formatDuration(Infinity)).toBe('0ms')
    expect(formatDuration(-Infinity)).toBe('0ms')
  })

  it('returns milliseconds for values under 1000', () => {
    expect(formatDuration(500)).toBe('500ms')
    expect(formatDuration(999)).toBe('999ms')
  })

  it('returns seconds for values 1000–59999', () => {
    expect(formatDuration(1000)).toBe('1s')
    expect(formatDuration(5000)).toBe('5s')
    expect(formatDuration(59000)).toBe('59s')
  })

  it('returns minutes for exact minute boundaries', () => {
    expect(formatDuration(60000)).toBe('1m')
    expect(formatDuration(300000)).toBe('5m')
  })

  it('returns minutes and seconds for mixed values', () => {
    expect(formatDuration(90000)).toBe('1m30s')
    expect(formatDuration(150000)).toBe('2m30s')
  })
})

describe('formatTokenCount', () => {
  it('returns "0" for 0', () => {
    expect(formatTokenCount(0)).toBe('0')
  })

  it('returns "0" for negative numbers', () => {
    expect(formatTokenCount(-1)).toBe('0')
    expect(formatTokenCount(-1000)).toBe('0')
  })

  it('returns "0" for NaN', () => {
    expect(formatTokenCount(NaN)).toBe('0')
  })

  it('returns "0" for Infinity', () => {
    expect(formatTokenCount(Infinity)).toBe('0')
    expect(formatTokenCount(-Infinity)).toBe('0')
  })

  it('returns raw number for values under 1000', () => {
    expect(formatTokenCount(500)).toBe('500')
    expect(formatTokenCount(999)).toBe('999')
  })

  it('returns K suffix for values 1000–999999', () => {
    expect(formatTokenCount(1000)).toBe('1.0K')
    expect(formatTokenCount(1500)).toBe('1.5K')
    expect(formatTokenCount(999999)).toBe('1000.0K')
  })

  it('returns M suffix for values >= 1000000', () => {
    expect(formatTokenCount(1000000)).toBe('1.0M')
    expect(formatTokenCount(2300000)).toBe('2.3M')
  })
})

describe('formatVersion', () => {
  it('keeps an existing release prefix', () => {
    expect(formatVersion('v1.2.3')).toBe('v1.2.3')
  })

  it('adds a release prefix when absent', () => {
    expect(formatVersion('1.2.3')).toBe('v1.2.3')
  })

  it('preserves non-release channel labels', () => {
    expect(formatVersion('dev')).toBe('dev')
  })
})

describe('formatIdleCountdown', () => {
  it('reads minutes and padded seconds under an hour', () => {
    expect(formatIdleCountdown(0)).toBe('0m 00s')
    expect(formatIdleCountdown(9)).toBe('0m 09s')
    expect(formatIdleCountdown(2500)).toBe('41m 40s')
    expect(formatIdleCountdown(3599)).toBe('59m 59s')
  })

  it('promotes to hours and padded minutes at the hour boundary', () => {
    expect(formatIdleCountdown(3600)).toBe('1h 00m')
    expect(formatIdleCountdown(3900)).toBe('1h 05m')
    expect(formatIdleCountdown(7200 + 60)).toBe('2h 01m')
  })

  it('floors fractional seconds', () => {
    expect(formatIdleCountdown(89.9)).toBe('1m 29s')
  })

  it('clamps negatives and non-finite inputs to zero', () => {
    expect(formatIdleCountdown(-30)).toBe('0m 00s')
    expect(formatIdleCountdown(Number.NaN)).toBe('0m 00s')
    expect(formatIdleCountdown(Number.POSITIVE_INFINITY)).toBe('0m 00s')
  })
})
