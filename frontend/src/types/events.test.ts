import { describe, it, expect } from 'vitest'
import { isAgentMetricsData, normalizeAgentMetricsData, isTaskCompleteData, isCompactionFinishedData, isPlanStepPausedData, isSubAgentPausedData, isGitConfigRiskData } from './events'

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

describe('isCompactionFinishedData', () => {
    const valid = { strategy: 'sliding_window', success: true, before_percent: 80.5, after_percent: 42 }

    it('accepts a minimal valid payload (legacy shape without the no-op flags)', () => {
        expect(isCompactionFinishedData(valid)).toBe(true)
    })

    it('accepts a payload with the no-op flags set', () => {
        expect(isCompactionFinishedData({ ...valid, nothing_compacted: true, deferred_to_resume: false })).toBe(true)
        expect(isCompactionFinishedData({ ...valid, nothing_compacted: true, deferred_to_resume: true, resumed: true })).toBe(true)
    })

    it('accepts explicitly-undefined no-op flags (field present but unset)', () => {
        expect(isCompactionFinishedData({ ...valid, nothing_compacted: undefined })).toBe(true)
        expect(isCompactionFinishedData({ ...valid, deferred_to_resume: undefined })).toBe(true)
    })

    it('rejects nothing_compacted with a non-boolean value', () => {
        expect(isCompactionFinishedData({ ...valid, nothing_compacted: 'yes' })).toBe(false)
        expect(isCompactionFinishedData({ ...valid, nothing_compacted: 1 })).toBe(false)
    })

    it('rejects deferred_to_resume with a non-boolean value', () => {
        expect(isCompactionFinishedData({ ...valid, deferred_to_resume: 'true' })).toBe(false)
        expect(isCompactionFinishedData({ ...valid, deferred_to_resume: 0 })).toBe(false)
    })

    it('accepts compaction_noop as a boolean and as explicitly undefined', () => {
        expect(isCompactionFinishedData({ ...valid, compaction_noop: true })).toBe(true)
        expect(isCompactionFinishedData({ ...valid, compaction_noop: false })).toBe(true)
        expect(isCompactionFinishedData({ ...valid, compaction_noop: undefined })).toBe(true)
    })

    it('rejects compaction_noop with a non-boolean value', () => {
        expect(isCompactionFinishedData({ ...valid, compaction_noop: 'yes' })).toBe(false)
        expect(isCompactionFinishedData({ ...valid, compaction_noop: 1 })).toBe(false)
    })

    it('still rejects payloads missing required fields', () => {
        expect(isCompactionFinishedData({ success: true, before_percent: 1, after_percent: 1 })).toBe(false)
        expect(isCompactionFinishedData(null)).toBe(false)
        expect(isCompactionFinishedData('compaction_finished')).toBe(false)
    })
})

describe('isPlanStepPausedData', () => {
    const valid = { step_id: 'step_1', duration: 4200, progress: 0.25, current_step_index: -1, completed_count: 1, total_count: 4 }

    it('accepts a full backend payload', () => {
        expect(isPlanStepPausedData(valid)).toBe(true)
    })

    it('accepts a minimal payload (step_id + duration only)', () => {
        expect(isPlanStepPausedData({ step_id: 'step_1', duration: 1 })).toBe(true)
    })

    it('accepts an optional error string (pause reason)', () => {
        expect(isPlanStepPausedData({ step_id: 'step_1', duration: 1, error: 'user pause' })).toBe(true)
    })

    it('rejects a success field intrusion (a pause is not a completion — but wrong-typed optionals still fail)', () => {
        expect(isPlanStepPausedData({ step_id: 'step_1', duration: 1, error: 42 })).toBe(false)
        expect(isPlanStepPausedData({ step_id: 'step_1', duration: 1, progress: 'x' })).toBe(false)
        expect(isPlanStepPausedData({ step_id: 'step_1', duration: 1, total_count: '4' })).toBe(false)
    })

    it('rejects missing required fields', () => {
        expect(isPlanStepPausedData({ step_id: 'step_1' })).toBe(false)
        expect(isPlanStepPausedData({ duration: 100 })).toBe(false)
        expect(isPlanStepPausedData({})).toBe(false)
        expect(isPlanStepPausedData(null)).toBe(false)
        expect(isPlanStepPausedData(undefined)).toBe(false)
    })
})

describe('isSubAgentPausedData', () => {
    it('accepts the payload shape the backend emits', () => {
        expect(isSubAgentPausedData({ step_id: 'delegate-1', duration: 900 })).toBe(true)
    })

    it('rejects wrong-typed or missing fields', () => {
        expect(isSubAgentPausedData({ step_id: 'delegate-1' })).toBe(false)
        expect(isSubAgentPausedData({ step_id: 'delegate-1', duration: '900' })).toBe(false)
        expect(isSubAgentPausedData({ duration: 900 })).toBe(false)
        expect(isSubAgentPausedData(null)).toBe(false)
        expect(isSubAgentPausedData(undefined)).toBe(false)
    })
})

describe('isGitConfigRiskData', () => {
    const valid = {
        path: '/repo',
        source: 'project',
        notice: 'Repository-defined git hooks do not run inside c0wrk.',
        findings: [{ key: 'core.fsmonitor', description: 'runs a monitor command' }],
    }

    it('accepts the payload shape the backend emits', () => {
        expect(isGitConfigRiskData(valid)).toBe(true)
    })

    it('accepts the workdir source and multiple findings', () => {
        expect(
            isGitConfigRiskData({
                ...valid,
                source: 'workdir',
                findings: [
                    { key: 'core.hookspath', description: 'redirects hooks' },
                    { key: 'filter.lfs.process', description: 'runs a filter' },
                ],
            }),
        ).toBe(true)
    })

    it('rejects an empty findings list (event never fires without findings)', () => {
        expect(isGitConfigRiskData({ ...valid, findings: [] })).toBe(false)
    })

    it('rejects wrong-typed findings entries', () => {
        expect(isGitConfigRiskData({ ...valid, findings: [{ key: 1, description: 'x' }] })).toBe(false)
        expect(isGitConfigRiskData({ ...valid, findings: [{ key: 'k' }] })).toBe(false)
        expect(isGitConfigRiskData({ ...valid, findings: 'core.fsmonitor' })).toBe(false)
    })

    it('rejects an unknown source', () => {
        expect(isGitConfigRiskData({ ...valid, source: 'session' })).toBe(false)
    })

    it('accepts optional reason and diff (drift payload)', () => {
        expect(
            isGitConfigRiskData({
                ...valid,
                reason: 'This repository was previously trusted, but its git configuration changed.',
                diff: '@@ -1 +1 @@\n- old\n+ new',
            }),
        ).toBe(true)
    })

    it('rejects wrong-typed reason or diff', () => {
        expect(isGitConfigRiskData({ ...valid, reason: 42 })).toBe(false)
        expect(isGitConfigRiskData({ ...valid, diff: ['not', 'a', 'string'] })).toBe(false)
        expect(isGitConfigRiskData({ ...valid, diff: null })).toBe(false)
    })

    it('rejects wrong-typed or missing fields', () => {
        expect(isGitConfigRiskData({ ...valid, path: 42 })).toBe(false)
        expect(isGitConfigRiskData({ ...valid, notice: undefined })).toBe(false)
        expect(isGitConfigRiskData({ path: '/repo', source: 'project' })).toBe(false)
        expect(isGitConfigRiskData(null)).toBe(false)
        expect(isGitConfigRiskData(undefined)).toBe(false)
    })
})
