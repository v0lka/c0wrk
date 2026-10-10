// Boundary-hardening tests for the session-event guards in types/events.ts
// (mass-bugfix cluster C9): every declared-string/number field a consumer
// trims, splits, endsWith-es or renders as a React child must be type-checked
// by its guard; optional fields stay optional; Go nil-slice nulls stay valid
// where the producer legitimately emits them.

import { describe, expect, it } from 'vitest'
import {
  isAskUserData,
  isErrorData,
  isGoalStatusData,
  isPlanData,
  isPlanStepStartData,
  isReflectionData,
  isRoutingData,
  isServiceData,
  isSessionTokensData,
  isStepTodoUpdateData,
  isSubAgentLaunchData,
  isThoughtData,
  isToolCallData,
  isToolJudgeResponseData,
  isToolResultData,
  isVectorIndexPayload,
} from './events'

describe('isToolCallData (#167 tool, #168 args, #180 source)', () => {
  const base = { step: 1, tool: 'read_file', args: '{"path":"a"}' }

  it('accepts a canonical payload (source absent)', () => {
    expect(isToolCallData(base)).toBe(true)
  })

  it('rejects a truthy non-string tool (ToolCard calls toolName.endsWith)', () => {
    expect(isToolCallData({ ...base, tool: 42 })).toBe(false)
    expect(isToolCallData({ ...base, tool: { name: 'x' } })).toBe(false)
  })

  it('rejects a truthy non-string args (GenericBody calls .includes/.slice)', () => {
    expect(isToolCallData({ ...base, args: { path: 'a' } })).toBe(false)
  })

  it('rejects a present non-string source (mcpBadge calls .startsWith/.slice)', () => {
    expect(isToolCallData({ ...base, source: ['mcp:x'] })).toBe(false)
  })

  it('rejects a non-number step and a malformed parsed_args', () => {
    expect(isToolCallData({ ...base, step: '1' })).toBe(false)
    expect(isToolCallData({ ...base, parsed_args: 'not-json' })).toBe(false)
    expect(isToolCallData({ ...base, parsed_args: null })).toBe(true)
  })
})

describe('isToolResultData (#126 result / result_preview)', () => {
  const base = { step: 1, result_len: 3, result: 'abc' }

  it('accepts a canonical payload and an absent result_preview', () => {
    expect(isToolResultData(base)).toBe(true)
  })

  it('rejects a present non-string result (bodies call .match/.split)', () => {
    expect(isToolResultData({ ...base, result: { body: 'x' } })).toBe(false)
    expect(isToolResultData({ ...base, result: 7 })).toBe(false)
  })

  it('rejects a present non-string result_preview', () => {
    expect(isToolResultData({ ...base, result_preview: [] })).toBe(false)
  })

  it('accepts a well-formed result_preview and error flag', () => {
    expect(isToolResultData({ ...base, result_preview: 'ab', error: true })).toBe(true)
  })
})

describe('isThoughtData (#123 content, #183 reasoning)', () => {
  it('accepts a canonical payload', () => {
    expect(isThoughtData({ step_num: 1, content: 'thinking' })).toBe(true)
    expect(isThoughtData({ step_num: 1, content: 'c', reasoning: 'r' })).toBe(true)
  })

  it('rejects a present non-string content (normalizeThoughtContent calls .trim())', () => {
    expect(isThoughtData({ step_num: 1, content: null })).toBe(false)
    expect(isThoughtData({ step_num: 1, content: { body: 'x' } })).toBe(false)
  })

  it('rejects a present non-string reasoning (ThoughtBlock calls reasoning.trim())', () => {
    expect(isThoughtData({ step_num: 1, content: 'c', reasoning: 5 })).toBe(false)
  })

  it('rejects a non-number step_num', () => {
    expect(isThoughtData({ step_num: '1', content: 'c' })).toBe(false)
  })
})

describe('isAskUserData (#32 questions element validation)', () => {
  const question = {
    id: 'q1',
    question: 'Which approach?',
    options: [{ label: 'REST', value: 'rest' }],
  }

  it('accepts a canonical payload', () => {
    expect(isAskUserData({ request_id: 'r1', questions: [question] })).toBe(true)
  })

  it('rejects a null questions array (a Go nil slice is null, but ask_user always has >=1 question)', () => {
    expect(isAskUserData({ request_id: 'r1', questions: null })).toBe(false)
  })

  it('rejects a question with a non-array options field', () => {
    expect(isAskUserData({ request_id: 'r1', questions: [{ ...question, options: null }] })).toBe(false)
  })

  it('rejects a malformed option element (the form renders label/value as keys)', () => {
    expect(isAskUserData({ request_id: 'r1', questions: [{ ...question, options: [{ label: 'A' }] }] })).toBe(false)
    expect(isAskUserData({ request_id: 'r1', questions: [{ ...question, options: [null] }] })).toBe(false)
  })

  it('rejects a non-string request_id', () => {
    expect(isAskUserData({ request_id: 1, questions: [question] })).toBe(false)
  })
})

describe('isPlanData (#32 steps element validation)', () => {
  const step = { id: 'step_1', description: 'Do it', status: 'pending' }

  it('accepts a canonical payload and null/absent steps (Go nil slice)', () => {
    expect(isPlanData({ step_count: 1, steps: [step] })).toBe(true)
    expect(isPlanData({ step_count: 0, steps: null })).toBe(true)
    expect(isPlanData({ step_count: 0 })).toBe(true)
  })

  it('rejects a truthy non-array steps (the handler maps over it)', () => {
    expect(isPlanData({ step_count: 1, steps: {} })).toBe(false)
    expect(isPlanData({ step_count: 1, steps: 'x' })).toBe(false)
  })

  it('rejects a malformed step element and accepts a null depends_on', () => {
    expect(isPlanData({ step_count: 1, steps: [null] })).toBe(false)
    expect(isPlanData({ step_count: 1, steps: [{ description: 5, status: 'pending' }] })).toBe(false)
    expect(isPlanData({ step_count: 1, steps: [{ ...step, depends_on: null }] })).toBe(true)
    expect(isPlanData({ step_count: 1, steps: [{ ...step, depends_on: ['a', 2] }] })).toBe(false)
  })
})

describe('isPlanStepStartData (#127 description / summary)', () => {
  it('accepts a canonical payload with absent summary', () => {
    expect(isPlanStepStartData({ step_id: 's1', description: 'd' })).toBe(true)
    expect(isPlanStepStartData({ step_id: 's1', description: 'd', summary: 's' })).toBe(true)
  })

  it('rejects a present non-string summary (handlePlanStepStart calls .trim())', () => {
    expect(isPlanStepStartData({ step_id: 's1', description: 'd', summary: 5 })).toBe(false)
  })

  it('rejects a non-string description and step_id', () => {
    expect(isPlanStepStartData({ step_id: 's1', description: null })).toBe(false)
    expect(isPlanStepStartData({ step_id: 1, description: 'd' })).toBe(false)
  })
})

describe('isSubAgentLaunchData (#169 description)', () => {
  it('accepts a canonical payload', () => {
    expect(isSubAgentLaunchData({ step_id: 's1', description: 'Run tests' })).toBe(true)
  })

  it('rejects a truthy non-string description (rendered via TooltipMarkdown + its fallback)', () => {
    expect(isSubAgentLaunchData({ step_id: 's1', description: { text: 'x' } })).toBe(false)
    expect(isSubAgentLaunchData({ step_id: 's1' })).toBe(false)
  })
})

describe('isReflectionData (#122 insights, #184 rendered string fields)', () => {
  const base = { summary: 's', attempt: 1 }

  it('accepts a canonical payload with string insights', () => {
    expect(isReflectionData({ ...base, insights: ['a', 'b'] })).toBe(true)
  })

  it('accepts null/absent insights (Go nil slice → JSON null)', () => {
    expect(isReflectionData(base)).toBe(true)
    expect(isReflectionData({ ...base, insights: null })).toBe(true)
  })

  it('rejects a truthy non-array insights (ReflectionBlock calls hypotheses.map)', () => {
    expect(isReflectionData({ ...base, insights: 'abc' })).toBe(false)
    expect(isReflectionData({ ...base, insights: {} })).toBe(false)
    expect(isReflectionData({ ...base, insights: ['a', 2] })).toBe(false)
  })

  it('rejects a present non-string rendered field (React child slots)', () => {
    expect(isReflectionData({ ...base, suggested_action: { act: 1 } })).toBe(false)
    expect(isReflectionData({ ...base, root_cause: 3 })).toBe(false)
  })

  it('rejects a non-string summary / non-number attempt', () => {
    expect(isReflectionData({ summary: {}, attempt: 1 })).toBe(false)
    expect(isReflectionData({ summary: 's', attempt: '1' })).toBe(false)
  })
})

describe('isToolJudgeResponseData (#128 reasoning / error)', () => {
  it('accepts confirm_id only and optional strings', () => {
    expect(isToolJudgeResponseData({ confirm_id: 'c1' })).toBe(true)
    expect(isToolJudgeResponseData({ confirm_id: 'c1', reasoning: 'r', error: 'e' })).toBe(true)
  })

  it('rejects a truthy non-string reasoning / error (rendered as React children)', () => {
    expect(isToolJudgeResponseData({ confirm_id: 'c1', reasoning: { text: 'x' } })).toBe(false)
    expect(isToolJudgeResponseData({ confirm_id: 'c1', error: ['boom'] })).toBe(false)
  })

  it('rejects a non-string confirm_id', () => {
    expect(isToolJudgeResponseData({ confirm_id: 1 })).toBe(false)
  })
})

describe('isServiceData / isErrorData / isRoutingData (#141/#142/#143)', () => {
  it('isServiceData rejects a present non-string content (activity label + status row)', () => {
    expect(isServiceData({ content: 'ok' })).toBe(true)
    expect(isServiceData({ content: '' })).toBe(true)
    expect(isServiceData({ content: { text: 'x' } })).toBe(false)
    expect(isServiceData({ content: ['x'] })).toBe(false)
  })

  it('isErrorData rejects a present non-string error (rendered message content)', () => {
    expect(isErrorData({ error: 'boom' })).toBe(true)
    expect(isErrorData({ error: { message: 'boom' } })).toBe(false)
  })

  it('isRoutingData rejects a present non-string domain / complexity', () => {
    expect(isRoutingData({ domain: 'code', complexity: 'medium' })).toBe(true)
    expect(isRoutingData({ domain: { d: 1 }, complexity: 'medium' })).toBe(false)
    expect(isRoutingData({ domain: 'code', complexity: 3 })).toBe(false)
  })
})

describe('isSessionTokensData (#158 model / family)', () => {
  const base = { session_input_tokens: 1, session_output_tokens: 2, model: 'm', family: 'f' }

  it('accepts a canonical payload', () => {
    expect(isSessionTokensData(base)).toBe(true)
  })

  it('rejects a present non-string model / family (status bar React children)', () => {
    expect(isSessionTokensData({ ...base, model: 42 })).toBe(false)
    expect(isSessionTokensData({ ...base, family: null })).toBe(false)
  })
})

describe('isStepTodoUpdateData (#32 items element validation)', () => {
  it('accepts a canonical payload', () => {
    expect(isStepTodoUpdateData({ step_id: 's1', items: [{ text: 'a', checked: false }] })).toBe(true)
  })

  it('rejects a truthy non-array items', () => {
    expect(isStepTodoUpdateData({ step_id: 's1', items: 'abc' })).toBe(false)
    expect(isStepTodoUpdateData({ step_id: 's1', items: { length: 3 } })).toBe(false)
  })

  it('rejects a null / primitive item element', () => {
    expect(isStepTodoUpdateData({ step_id: 's1', items: [null] })).toBe(false)
    expect(isStepTodoUpdateData({ step_id: 's1', items: [{ text: 'a' }] })).toBe(false)
    expect(isStepTodoUpdateData({ step_id: 's1', items: [{ text: 1, checked: true }] })).toBe(false)
  })
})

describe('isGoalStatusData (#185 verdict / reason / evidence)', () => {
  const base = { status: 'active', turn: 1, condition: 'c', max_turns: 5 }

  it('accepts the required scalars alone and with well-formed optionals', () => {
    expect(isGoalStatusData(base)).toBe(true)
    expect(isGoalStatusData({
      ...base,
      verdict: 'not_met',
      reason: 'tests failed',
      verification: 'rejected',
      verification_reason: 'r',
      verification_mode: 'executable',
      evidence: [{ type: 'file', ref: 'a.go', summary: 's' }],
      verification_evidence: [{ type: 'command', ref: 'go test', summary: 's' }],
      created_at: 1234,
    })).toBe(true)
  })

  it('accepts null evidence arrays (Go nil slice)', () => {
    expect(isGoalStatusData({ ...base, evidence: null })).toBe(true)
  })

  it('rejects a present non-string reason / verdict (goalTransition trims reason)', () => {
    expect(isGoalStatusData({ ...base, reason: { text: 'x' } })).toBe(false)
    expect(isGoalStatusData({ ...base, verdict: 1 })).toBe(false)
  })

  it('rejects a truthy non-array evidence and a malformed evidence element', () => {
    expect(isGoalStatusData({ ...base, evidence: 'everything' })).toBe(false)
    expect(isGoalStatusData({ ...base, evidence: [{ type: 'file', ref: 'a' }] })).toBe(false)
    expect(isGoalStatusData({ ...base, evidence: [null] })).toBe(false)
  })
})

describe('isVectorIndexPayload (#159 provider_fallback_reason)', () => {
  const base = { state: 'idle', progress: 0, files_indexed: 0, total_files: 0 }

  it('accepts a canonical payload with and without the optional reason', () => {
    expect(isVectorIndexPayload(base)).toBe(true)
    expect(isVectorIndexPayload({ ...base, provider_fallback_reason: 'no onnx' })).toBe(true)
  })

  it('rejects a present non-string provider_fallback_reason (VectorIndexDiagnostics calls .trim())', () => {
    expect(isVectorIndexPayload({ ...base, provider_fallback_reason: { why: 'x' } })).toBe(false)
  })
})
