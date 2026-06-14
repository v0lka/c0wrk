import { describe, it, expect } from 'vitest'
import { isTaskCompleteData } from './events'

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
