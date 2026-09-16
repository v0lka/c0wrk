import { describe, it, expect } from 'vitest'
import { isAgentMetricsData, normalizeAgentMetricsData, isTaskCompleteData, isCompactionFinishedData, isPlanStepCompleteData, isPlanStepPausedData, isSubAgentPausedData, isSubAgentCompleteData, isGitConfigRiskData, isE2SStateData, isE2SSigma } from './events'

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
        model_profiles: { enabled: true, variants: ['essential_tools', 'sampling'] },
    }

    it('accepts a valid payload', () => {
        expect(isAgentMetricsData(valid)).toBe(true)
    })

    it('accepts a payload with an empty variants array (profile off)', () => {
        expect(isAgentMetricsData({ ...valid, model_profiles: { enabled: false, variants: [] } })).toBe(true)
    })

    it('accepts a payload carrying the active profile identity', () => {
        expect(isAgentMetricsData({ ...valid, model_profiles: { enabled: true, profile: 'qwen3.8-27b', profile_kind: 'predefined', variants: [] } })).toBe(true)
        expect(isAgentMetricsData({ ...valid, model_profiles: { enabled: true, profile: 'my-tuned', profile_kind: 'custom', variants: ['sampling'] } })).toBe(true)
    })

    it('rejects malformed profile identity fields', () => {
        expect(isAgentMetricsData({ ...valid, model_profiles: { ...valid.model_profiles, profile: 42 } })).toBe(false)
        expect(isAgentMetricsData({ ...valid, model_profiles: { ...valid.model_profiles, profile_kind: 'builtin' } })).toBe(false)
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

    it('rejects malformed model_profiles block', () => {
        expect(isAgentMetricsData({ ...valid, model_profiles: { enabled: 'yes' } })).toBe(false)
        expect(isAgentMetricsData({ ...valid, model_profiles: undefined })).toBe(false)
    })

    it('accepts the pre-rename small_llm container key (legacy persisted rows)', () => {
        const { model_profiles, ...rest } = valid
        expect(isAgentMetricsData({ ...rest, small_llm: model_profiles })).toBe(true)
    })

    it('prefers the current model_profiles key when both keys are present', () => {
        const { model_profiles, ...rest } = valid
        expect(isAgentMetricsData({ ...rest, model_profiles, small_llm: { enabled: 'yes' } })).toBe(true)
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
        model_profiles: { enabled: true, variants: ['essential_tools', 'sampling'] },
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

    it('carries well-formed profile identity fields through', () => {
        const withProfile = { ...full, model_profiles: { enabled: true, profile: 'my-tuned', profile_kind: 'custom', variants: full.model_profiles.variants } }
        expect(normalizeAgentMetricsData(withProfile)).toEqual(withProfile)
    })

    it('drops malformed profile identity fields instead of failing the row', () => {
        const malformed = { ...full, model_profiles: { enabled: true, profile: 42, profile_kind: 'builtin', variants: full.model_profiles.variants } }
        const got = normalizeAgentMetricsData(malformed)
        expect(got?.model_profiles.profile).toBeUndefined()
        expect(got?.model_profiles.profile_kind).toBeUndefined()
        expect(got?.model_profiles.enabled).toBe(true)
        expect(got?.steps).toBe(full.steps)
    })

    it('returns undefined for non-metrics payloads', () => {
        expect(normalizeAgentMetricsData(null)).toBeUndefined()
        expect(normalizeAgentMetricsData(undefined)).toBeUndefined()
        expect(normalizeAgentMetricsData({ skills: ['x'] })).toBeUndefined()
        expect(normalizeAgentMetricsData({ ...full, steps: true })).toBeUndefined()
    })

    it('normalizes a legacy row that still uses the small_llm container key', () => {
        const { model_profiles, ...rest } = full
        const legacy = { ...rest, small_llm: model_profiles }
        const got = normalizeAgentMetricsData(legacy)
        expect(got).toBeDefined()
        expect(got?.model_profiles).toEqual({ enabled: true, variants: full.model_profiles.variants })
        expect(got?.steps).toBe(full.steps)
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

    it('accepts compaction_availability as a valid list and as explicitly undefined', () => {
        const avail = [
            { strategy: 'sliding_window', available: true, reclaim_tokens: 100, exact: true },
            { strategy: 'summarization', available: false, reclaim_tokens: 0, exact: false },
        ]
        expect(isCompactionFinishedData({ ...valid, compaction_availability: avail })).toBe(true)
        expect(isCompactionFinishedData({ ...valid, compaction_availability: [] })).toBe(true)
        expect(isCompactionFinishedData({ ...valid, compaction_availability: undefined })).toBe(true)
    })

    it('rejects compaction_availability with a malformed entry or non-array', () => {
        expect(isCompactionFinishedData({ ...valid, compaction_availability: 'yes' })).toBe(false)
        expect(isCompactionFinishedData({ ...valid, compaction_availability: 1 })).toBe(false)
        expect(isCompactionFinishedData({ ...valid, compaction_availability: [{ strategy: 'x' }] })).toBe(false)
        expect(isCompactionFinishedData({ ...valid, compaction_availability: [{ strategy: 'x', available: 'yes', reclaim_tokens: 0, exact: true }] })).toBe(false)
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

describe('isPlanStepCompleteData', () => {
    it('accepts the payload shape the backend emits, with and without an error', () => {
        expect(isPlanStepCompleteData({ step_id: 'step_1', success: true, duration: 900, progress: 1, current_step_index: -1, completed_count: 1, total_count: 3 })).toBe(true)
        expect(isPlanStepCompleteData({ step_id: 'step_1', success: false, duration: 900, error: 'boom' })).toBe(true)
    })

    it('rejects a malformed optional error without dropping the rest', () => {
        expect(isPlanStepCompleteData({ step_id: 'step_1', success: false, duration: 900, error: 42 })).toBe(false)
        expect(isPlanStepCompleteData({ step_id: 'step_1', success: false, duration: 900, error: { nested: true } })).toBe(false)
        expect(isPlanStepCompleteData({ step_id: 'step_1' })).toBe(false)
    })

    it('rejects a missing or non-numeric required duration', () => {
        expect(isPlanStepCompleteData({ step_id: 'step_1', success: true })).toBe(false)
        expect(isPlanStepCompleteData({ step_id: 'step_1', success: true, duration: '900' })).toBe(false)
    })
})

describe('isSubAgentCompleteData', () => {
    it('accepts the payload shape the backend emits', () => {
        expect(isSubAgentCompleteData({ step_id: 'delegate-1', success: true, duration: 900 })).toBe(true)
        // The optional failure reason is attached only on failure.
        expect(isSubAgentCompleteData({ step_id: 'delegate-1', success: false, duration: 900, error: 'boom' })).toBe(true)
    })

    it('rejects wrong-typed or missing fields', () => {
        expect(isSubAgentCompleteData({ step_id: 'delegate-1' })).toBe(false)
        expect(isSubAgentCompleteData({ success: true, duration: 900 })).toBe(false)
        expect(isSubAgentCompleteData({ step_id: 'delegate-1', success: false, duration: 900, error: 42 })).toBe(false)
        // The required `duration` is validated symmetrically with the optional
        // `error` above (fail-closed for both).
        expect(isSubAgentCompleteData({ step_id: 'delegate-1', success: true })).toBe(false)
        expect(isSubAgentCompleteData({ step_id: 'delegate-1', success: true, duration: '900' })).toBe(false)
        expect(isSubAgentCompleteData(null)).toBe(false)
        expect(isSubAgentCompleteData(undefined)).toBe(false)
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

describe('isE2SStateData', () => {
    const valid = {
        state: {
            objective: 'Ship the E2S panel',
            status: 'in progress',
            files_touched: ['frontend/src/stores/e2sStore.ts'],
            findings: ['guard pattern mirrors goal events'],
            decisions: ['store owns the patch merge'],
            next_steps: ['write tests'],
            checklist: [
                { text: 'types + guard', checked: true },
                { text: 'panel', checked: false },
            ],
        },
        turn: 3,
        max_turns: 10,
        status: 'running',
    }

    it('accepts a full valid snapshot', () => {
        expect(isE2SStateData(valid)).toBe(true)
    })

    it('accepts a minimal snapshot (empty Σ slice, no patch flag)', () => {
        expect(isE2SStateData({ state: {}, turn: 0, max_turns: 10, status: 'running' })).toBe(true)
    })

    it('accepts a patch payload (patch: true, partial Σ)', () => {
        expect(isE2SStateData({ state: { checklist: [{ text: 'x', checked: true }] }, turn: 4, max_turns: 10, status: 'running', patch: true })).toBe(true)
    })

    it('accepts patch: false as an explicit full snapshot', () => {
        expect(isE2SStateData({ ...valid, patch: false })).toBe(true)
    })

    it('requires a state object', () => {
        expect(isE2SStateData({ ...valid, state: undefined })).toBe(false)
        expect(isE2SStateData({ ...valid, state: 'running' })).toBe(false)
        expect(isE2SStateData({ ...valid, state: null })).toBe(false)
        expect(isE2SStateData({ ...valid, state: [] })).toBe(false)
    })

    it('rejects wrong-typed Σ fields', () => {
        expect(isE2SStateData({ ...valid, state: { ...valid.state, objective: 7 } })).toBe(false)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, status: true } })).toBe(false)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, files_touched: 'a.go' } })).toBe(false)
        // A JSON null against a CORE key is a wrong-typed field, not a
        // tombstone — the whole payload is dropped at the boundary.
        expect(isE2SStateData({ ...valid, state: { ...valid.state, files_touched: null } })).toBe(false)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, findings: [1, 2] } })).toBe(false)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, decisions: null } })).toBe(false)
    })

    it('rejects a malformed checklist item', () => {
        expect(isE2SStateData({ ...valid, state: { ...valid.state, checklist: [{ text: 'no flag' }] } })).toBe(false)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, checklist: [{ text: 3, checked: true }] } })).toBe(false)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, checklist: [{ checked: true }] } })).toBe(false)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, checklist: 'not-a-list' } })).toBe(false)
    })

    it('accepts done_criteria and ignores unknown extension keys', () => {
        expect(isE2SStateData({ ...valid, state: { ...valid.state, done_criteria: ['all tests green'] } })).toBe(true)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, done_criteria: 'green' } })).toBe(false)
        expect(isE2SStateData({ ...valid, state: { ...valid.state, custom_extension: { any: 'shape' } } })).toBe(true)
    })

    it('accepts the minimal real-backend payload ({state, turn} only)', () => {
        expect(isE2SStateData({ state: { objective: 'o' }, turn: 1 })).toBe(true)
        expect(isE2SStateData({ state: {}, turn: 0 })).toBe(true)
    })

    it('requires turn to be a number', () => {
        expect(isE2SStateData({ ...valid, turn: '3' })).toBe(false)
        expect(isE2SStateData({ ...valid, turn: undefined })).toBe(false)
        expect(isE2SStateData({ state: {} })).toBe(false)
    })

    it('accepts absent max_turns/status but rejects wrong types', () => {
        const { max_turns, status, ...minimal } = valid
        expect(isE2SStateData({ ...minimal, max_turns, status })).toBe(true)
        expect(isE2SStateData({ ...valid, max_turns: '10' })).toBe(false)
        expect(isE2SStateData({ ...valid, status: 3 })).toBe(false)
    })

    it('accepts a numeric total_turns (present or absent) and rejects wrong types', () => {
        expect(isE2SStateData({ ...valid, total_turns: 7 })).toBe(true)
        expect(isE2SStateData({ ...valid, total_turns: undefined })).toBe(true)
        expect(isE2SStateData({ ...valid, total_turns: '7' })).toBe(false)
        expect(isE2SStateData({ ...valid, total_turns: null })).toBe(false)
    })

    it('rejects a wrong-typed patch flag', () => {
        expect(isE2SStateData({ ...valid, patch: 'yes' })).toBe(false)
        expect(isE2SStateData({ ...valid, patch: 1 })).toBe(false)
    })

    it('rejects non-objects', () => {
        expect(isE2SStateData(null)).toBe(false)
        expect(isE2SStateData(undefined)).toBe(false)
        expect(isE2SStateData('e2s_state')).toBe(false)
        expect(isE2SStateData([])).toBe(false)
        expect(isE2SStateData({})).toBe(false)
    })
})

describe('isE2SSigma', () => {
    it('accepts an empty slice (a patch may carry nothing new)', () => {
        expect(isE2SSigma({})).toBe(true)
    })

    it('accepts fully-typed sigma', () => {
        expect(isE2SSigma({
            objective: 'o', status: 's',
            files_touched: [], findings: [], decisions: [], next_steps: [],
            checklist: [],
        })).toBe(true)
    })

    it('rejects non-objects and wrong types', () => {
        expect(isE2SSigma(null)).toBe(false)
        expect(isE2SSigma('sigma')).toBe(false)
        expect(isE2SSigma([])).toBe(false)
        expect(isE2SSigma({ next_steps: [null] })).toBe(false)
        expect(isE2SSigma({ checklist: [{ text: 't', checked: 'yes' }] })).toBe(false)
    })
})
