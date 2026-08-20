import { describe, it, expect } from 'vitest'
import { isAgentMetricsData, normalizeAgentMetricsData, isTaskCompleteData } from './events'

describe('isTaskCompleteData', () => {
    it('accepts valid data with string output', () => {
        expect(isTaskCompleteData({ output: 'done' })).toBe(true)
    })

    it('accepts valid data with attempt_count', () => {
        expect(isTaskCompleteData({ attempt_count: 3 })).toBe(true)
    })

    it('accepts valid data with routing_decision object', () => {
        expect(isTaskCompleteData({ routing_decision: { domain: 'code' } })).toBe(true)
    })

    it('accepts data with multiple valid fields', () => {
        expect(isTaskCompleteData({ output: 'ok', attempt_count: 1, routing_decision: {} })).toBe(true)
    })

    it('rejects data with undefined output (field present but undefined)', () => {
        expect(isTaskCompleteData({ output: undefined })).toBe(false)
    })

    it('rejects output with wrong type (number)', () => {
        expect(isTaskCompleteData({ output: 123 })).toBe(false)
    })

    it('rejects attempt_count with wrong type (string)', () => {
        expect(isTaskCompleteData({ attempt_count: '3' })).toBe(false)
    })

    it('rejects routing_decision with wrong type (string)', () => {
        expect(isTaskCompleteData({ routing_decision: 'invalid' })).toBe(false)
    })

    it('rejects empty object', () => {
        expect(isTaskCompleteData({})).toBe(false)
    })

    it('rejects null', () => {
        expect(isTaskCompleteData(null)).toBe(false)
    })

    it('rejects undefined', () => {
        expect(isTaskCompleteData(undefined)).toBe(false)
    })

    it('rejects string', () => {
        expect(isTaskCompleteData('task_complete')).toBe(false)
    })

    it('rejects object with unrelated fields only', () => {
        expect(isTaskCompleteData({ foo: 'bar' })).toBe(false)
    })
})

describe('isAgentMetricsData', () => {
    const valid = {
        finish: 'full',
        parse_errors: 2,
        nudges: { repeat: 1, same_tool: 1, fruitless: 0, parse: 1, truncation: 0 },
        aborts: { repeat: 0, same_tool: 0, fruitless: 1, parse: 0, truncation: 0 },
        steps: 12,
        output_tokens: 3400,
        invalid_tool_calls: 2,
        small_llm: { enabled: true, variants: ['essential_tools', 'sampling'] },
    }

    it('accepts a valid payload', () => {
        expect(isAgentMetricsData(valid)).toBe(true)
    })

    it('accepts a payload with an empty variants array (profile off)', () => {
        expect(isAgentMetricsData({ ...valid, small_llm: { enabled: false, variants: [] } })).toBe(true)
    })

    it('rejects null/undefined/string', () => {
        expect(isAgentMetricsData(null)).toBe(false)
        expect(isAgentMetricsData(undefined)).toBe(false)
        expect(isAgentMetricsData('agent_metrics')).toBe(false)
    })

    it('rejects missing counter blocks', () => {
        expect(isAgentMetricsData({ ...valid, nudges: undefined })).toBe(false)
        expect(isAgentMetricsData({ ...valid, aborts: undefined })).toBe(false)
    })

    it('rejects malformed counter blocks', () => {
        expect(isAgentMetricsData({ ...valid, nudges: { repeat: 1 } })).toBe(false)
        expect(isAgentMetricsData({ ...valid, aborts: { repeat: 'x', same_tool: 1, fruitless: 1, parse: 1 } })).toBe(false)
        expect(isAgentMetricsData({ ...valid, nudges: { repeat: 1, same_tool: 1, fruitless: 0, parse: 1 } })).toBe(false)
        expect(isAgentMetricsData({ ...valid, aborts: { repeat: 0, same_tool: 0, fruitless: 1, parse: 0, truncation: '0' } })).toBe(false)
    })

    it('rejects wrong scalar types', () => {
        expect(isAgentMetricsData({ ...valid, parse_errors: '2' })).toBe(false)
        expect(isAgentMetricsData({ ...valid, steps: true })).toBe(false)
        expect(isAgentMetricsData({ ...valid, finish: 42 })).toBe(false)
        expect(isAgentMetricsData({ ...valid, invalid_tool_calls: '2' })).toBe(false)
        expect(isAgentMetricsData({ ...valid, invalid_tool_calls: undefined })).toBe(false)
    })

    it('rejects malformed small_llm block', () => {
        expect(isAgentMetricsData({ ...valid, small_llm: { enabled: 'yes' } })).toBe(false)
        expect(isAgentMetricsData({ ...valid, small_llm: undefined })).toBe(false)
    })
})

describe('normalizeAgentMetricsData', () => {
    const full = {
        finish: 'full',
        parse_errors: 2,
        nudges: { repeat: 1, same_tool: 1, fruitless: 0, parse: 1, truncation: 0 },
        aborts: { repeat: 0, same_tool: 0, fruitless: 1, parse: 0, truncation: 1 },
        steps: 12,
        output_tokens: 3400,
        invalid_tool_calls: 2,
        small_llm: { enabled: true, variants: ['essential_tools', 'sampling'] },
    }

    it('returns the payload unchanged when all fields are present', () => {
        expect(normalizeAgentMetricsData(full)).toEqual(full)
    })

    it('defaults invalid_tool_calls to 0 for legacy rows', () => {
        const legacy = { ...full }
        delete (legacy as { invalid_tool_calls?: number }).invalid_tool_calls
        const got = normalizeAgentMetricsData(legacy)
        expect(got?.invalid_tool_calls).toBe(0)
    })

    it('defaults the truncation counter to 0 for legacy rows', () => {
        const legacy = { ...full, nudges: { repeat: 1, same_tool: 1, fruitless: 0, parse: 1 }, aborts: { repeat: 0, same_tool: 0, fruitless: 1, parse: 0 } }
        const got = normalizeAgentMetricsData(legacy)
        expect(got?.nudges.truncation).toBe(0)
        expect(got?.aborts.truncation).toBe(0)
    })

    it('preserves non-zero legacy-adjacent values', () => {
        const legacy = { ...full, invalid_tool_calls: 3, aborts: { repeat: 0, same_tool: 0, fruitless: 1, parse: 0, truncation: 2 } }
        const got = normalizeAgentMetricsData(legacy)
        expect(got?.invalid_tool_calls).toBe(3)
        expect(got?.aborts.truncation).toBe(2)
    })

    it('returns undefined for non-metrics payloads', () => {
        expect(normalizeAgentMetricsData(null)).toBeUndefined()
        expect(normalizeAgentMetricsData(undefined)).toBeUndefined()
        expect(normalizeAgentMetricsData({ skills: ['x'] })).toBeUndefined()
        expect(normalizeAgentMetricsData({ ...full, steps: true })).toBeUndefined()
    })
})
