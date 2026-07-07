import { describe, it, expect } from 'vitest'
import { parseAskUserQuestions, getAskUserResolution } from './messages'

describe('parseAskUserQuestions', () => {
    it('returns empty array when metadata is missing questions', () => {
        expect(parseAskUserQuestions(undefined)).toEqual([])
        expect(parseAskUserQuestions({})).toEqual([])
        expect(parseAskUserQuestions({ questions: 'nope' })).toEqual([])
    })

    it('passes through well-formed questions untouched', () => {
        const meta = {
            questions: [
                {
                    id: 'q1',
                    question: 'Pick one',
                    options: [
                        { label: 'A', value: 'a' },
                        { label: 'B', value: 'b' },
                    ],
                    multi_select: false,
                    recommended: ['a'],
                },
            ],
        }
        expect(parseAskUserQuestions(meta)).toEqual([
            {
                id: 'q1',
                question: 'Pick one',
                options: [
                    { label: 'A', value: 'a' },
                    { label: 'B', value: 'b' },
                ],
                multi_select: false,
                recommended: ['a'],
            },
        ])
    })

    it('falls back value -> label when value is empty (legacy defense)', () => {
        // The bug scenario: model omitted value, so every option had value="".
        // Without normalization, clicking any option selects ALL because the
        // Set is keyed by value and all share "".
        const meta = {
            questions: [
                {
                    id: 'q1',
                    question: 'Pick one',
                    options: [
                        { label: 'Option A', value: '' },
                        { label: 'Option B', value: '' },
                    ],
                },
            ],
        }
        const parsed = parseAskUserQuestions(meta)
        expect(parsed).toHaveLength(1)
        expect(parsed[0]!.options).toEqual([
            { label: 'Option A', value: 'Option A' },
            { label: 'Option B', value: 'Option B' },
        ])
    })

    it('falls back value -> label when value field is missing entirely', () => {
        const meta = {
            questions: [
                {
                    id: 'q1',
                    question: 'Pick one',
                    options: [{ label: 'Only label' }, { label: 'Other' }],
                },
            ],
        }
        const parsed = parseAskUserQuestions(meta)
        expect(parsed[0]!.options).toEqual([
            { label: 'Only label', value: 'Only label' },
            { label: 'Other', value: 'Other' },
        ])
    })

    it('falls back label -> value when label is empty but value is present', () => {
        const meta = {
            questions: [
                {
                    id: 'q1',
                    question: 'Pick one',
                    options: [{ label: '', value: 'v1' }],
                },
            ],
        }
        expect(parseAskUserQuestions(meta)[0]!.options).toEqual([
            { label: 'v1', value: 'v1' },
        ])
    })

    it('drops options with neither label nor value', () => {
        const meta = {
            questions: [
                {
                    id: 'q1',
                    question: 'Pick one',
                    options: [
                        { label: '', value: '' },
                        { label: 'Good', value: 'g' },
                        { foo: 'bar' },
                    ],
                },
            ],
        }
        expect(parseAskUserQuestions(meta)[0]!.options).toEqual([
            { label: 'Good', value: 'g' },
        ])
    })

    it('skips questions with missing id or question text', () => {
        const meta = {
            questions: [
                { question: 'No id', options: [{ label: 'A', value: 'a' }] },
                { id: 'q2', options: [{ label: 'A', value: 'a' }] },
                { id: 'q3', question: 'Good', options: [{ label: 'A', value: 'a' }] },
            ],
        }
        expect(parseAskUserQuestions(meta)).toHaveLength(1)
        expect(parseAskUserQuestions(meta)[0]!.id).toBe('q3')
    })

    it('preserves multi_select and recommended when present', () => {
        const parsed = parseAskUserQuestions({
            questions: [
                {
                    id: 'q1',
                    question: 'Pick many',
                    options: [{ label: 'A', value: 'a' }],
                    multi_select: true,
                    recommended: ['a', 9, 'b'],
                },
            ],
        })
        expect(parsed[0]!.multi_select).toBe(true)
        expect(parsed[0]!.recommended).toEqual(['a', 'b'])
    })

    it('does not add multi_select/recommended when absent', () => {
        const parsed = parseAskUserQuestions({
            questions: [
                { id: 'q1', question: 'Pick', options: [{ label: 'A', value: 'a' }] },
            ],
        })
        expect(parsed[0]!.multi_select).toBeUndefined()
        expect(parsed[0]!.recommended).toBeUndefined()
    })
})

describe('getAskUserResolution', () => {
    it('returns null when not resolved', () => {
        expect(getAskUserResolution(undefined)).toBeNull()
        expect(getAskUserResolution({})).toBeNull()
        expect(getAskUserResolution({ resolved: false })).toBeNull()
    })

    it('returns the answer string when resolved', () => {
        expect(getAskUserResolution({ resolved: true, answer: 'Option A' })).toBe('Option A')
    })

    it('returns empty string when resolved but answer missing', () => {
        expect(getAskUserResolution({ resolved: true })).toBe('')
    })
})
