import { describe, it, expect } from 'vitest'
import { isContextCompactionData, isSessionTokensData } from './wails'

describe('isContextCompactionData', () => {
  it('returns true for valid object with both fields', () => {
    expect(isContextCompactionData({ before_percent: 80, after_percent: 40 })).toBe(true)
  })

  it('returns true when extra fields are present', () => {
    expect(isContextCompactionData({ before_percent: 80, after_percent: 40, extra: 'field' })).toBe(true)
  })

  it('returns false for null', () => {
    expect(isContextCompactionData(null)).toBe(false)
  })

  it('returns false for undefined', () => {
    expect(isContextCompactionData(undefined)).toBe(false)
  })

  it('returns false for string', () => {
    expect(isContextCompactionData('string')).toBe(false)
  })

  it('returns false for number', () => {
    expect(isContextCompactionData(42)).toBe(false)
  })

  it('returns false for empty object', () => {
    expect(isContextCompactionData({})).toBe(false)
  })

  it('returns false when before_percent is missing', () => {
    expect(isContextCompactionData({ after_percent: 40 })).toBe(false)
  })

  it('returns false when after_percent is missing', () => {
    expect(isContextCompactionData({ before_percent: 80 })).toBe(false)
  })
})

describe('isSessionTokensData', () => {
  it('returns true for valid object with all fields', () => {
    expect(isSessionTokensData({ session_input_tokens: 100, session_output_tokens: 200, model: 'gpt-4', family: 'openai' })).toBe(true)
  })

  it('returns true with only required fields', () => {
    expect(isSessionTokensData({ session_input_tokens: 0, session_output_tokens: 0 })).toBe(true)
  })

  it('returns false for null', () => {
    expect(isSessionTokensData(null)).toBe(false)
  })

  it('returns false for undefined', () => {
    expect(isSessionTokensData(undefined)).toBe(false)
  })

  it('returns false for string', () => {
    expect(isSessionTokensData('string')).toBe(false)
  })

  it('returns false for number', () => {
    expect(isSessionTokensData(42)).toBe(false)
  })

  it('returns false for empty object', () => {
    expect(isSessionTokensData({})).toBe(false)
  })

  it('returns false when session_input_tokens is missing', () => {
    expect(isSessionTokensData({ session_output_tokens: 200 })).toBe(false)
  })

  it('returns false when session_output_tokens is missing', () => {
    expect(isSessionTokensData({ session_input_tokens: 100 })).toBe(false)
  })
})
