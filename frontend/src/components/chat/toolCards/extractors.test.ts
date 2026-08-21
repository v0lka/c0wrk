import { describe, it, expect } from 'vitest'
import {
  extractBashTitle, extractFileTitle, extractDirTitle,
  extractSearchTitle, extractUrlTitle, extractMemoTitle,
  extractStepOutputTitle, extractFactsTitle,
  extractAttachmentTitle, extractAttachmentId,
  extractDelegationId,
  extractFileHint, extractFileLine, extractBashHint, extractSearchHint,
  extractDelegateTitle, extractReflectTitle, extractDeclarePlanTitle,
  extractExecutePlanTitle, extractProposeGoalTitle,
} from './extractors'

describe('extractBashTitle', () => {
  it('extracts command from parsedArgs', () => {
    expect(extractBashTitle({ command: 'go test ./...' }, '')).toBe('go test ./...')
  })
  it('returns long commands in full', () => {
    const long = 'a'.repeat(100)
    expect(extractBashTitle({ command: long }, '')).toBe(long)
  })
  it('falls back to raw args', () => {
    expect(extractBashTitle(undefined, '{"command":"npm run build"}')).toBe('npm run build')
  })
  it('returns empty when no command is present', () => {
    expect(extractBashTitle(undefined, '')).toBe('')
  })
})

describe('extractFileTitle', () => {
  it('extracts basename from path', () => {
    expect(extractFileTitle({ path: '/src/lib/utils.ts' }, '')).toBe('utils.ts')
  })
  it('appends line range', () => {
    expect(extractFileTitle({ path: '/a/b.ts', start_line: 10, end_line: 20 }, '')).toBe('b.ts L10-20')
  })
  it('appends start_line only', () => {
    expect(extractFileTitle({ path: '/a/b.ts', start_line: 5 }, '')).toBe('b.ts L5')
  })
  it('falls back to raw args', () => {
    expect(extractFileTitle(undefined, '{"path":"/x/y.go"}')).toBe('y.go')
  })
  it('returns fallback when empty', () => {
    expect(extractFileTitle(undefined, '{}')).toBe('file')
  })
})

describe('extractDirTitle', () => {
  it('extracts basename', () => {
    expect(extractDirTitle({ path: '/src/components' }, '')).toBe('components')
  })
  it('returns fallback', () => {
    expect(extractDirTitle(undefined, '')).toBe('directory')
  })
})

describe('extractSearchTitle', () => {
  it('extracts pattern', () => {
    expect(extractSearchTitle({ pattern: '**/*.tsx' }, '')).toBe('**/*.tsx')
  })
  it('extracts query', () => {
    expect(extractSearchTitle({ query: 'handle error' }, '')).toBe('handle error')
  })
  it('extracts keywords array', () => {
    expect(extractSearchTitle({ keywords: ['auth', 'login'] }, '')).toBe('auth, login')
  })
  it('returns long patterns in full', () => {
    const long = 'x'.repeat(80)
    expect(extractSearchTitle({ pattern: long }, '')).toBe(long)
  })
})

describe('extractUrlTitle', () => {
  it('extracts url', () => {
    expect(extractUrlTitle({ url: 'https://example.com/api' }, '')).toBe('https://example.com/api')
  })
  it('returns long urls in full', () => {
    const long = 'https://' + 'a'.repeat(100)
    expect(extractUrlTitle({ url: long }, '')).toBe(long)
  })
})

describe('extractMemoTitle', () => {
  it('returns checklist for update_checklist', () => {
    expect(extractMemoTitle('update_checklist', {}, '')).toBe('checklist')
  })
  it('returns step complete for declare_step_complete', () => {
    expect(extractMemoTitle('declare_step_complete', {}, '')).toBe('step complete')
  })
  it('returns fact with keywords for store_fact', () => {
    expect(extractMemoTitle('store_fact', { keywords: ['auth', 'jwt'] }, '')).toBe('fact: auth, jwt')
  })
  it('returns fact fallback when no keywords', () => {
    expect(extractMemoTitle('store_fact', {}, '')).toBe('fact')
  })
})

describe('extractFileHint', () => {
  it('returns full path', () => {
    expect(extractFileHint({ path: '/home/user/project/src/main.ts' }, '')).toBe('/home/user/project/src/main.ts')
  })
  it('returns undefined when no path', () => {
    expect(extractFileHint({}, '')).toBeUndefined()
  })
})

describe('extractFileLine', () => {
  it('returns start_line from parsedArgs', () => {
    expect(extractFileLine({ path: '/a/b.ts', start_line: 10, end_line: 20 }, '')).toBe(10)
  })
  it('returns start_line without end_line', () => {
    expect(extractFileLine({ path: '/a/b.ts', start_line: 5 }, '')).toBe(5)
  })
  it('falls back to raw args', () => {
    expect(extractFileLine(undefined, '{"path":"/x/y.go","start_line":42}')).toBe(42)
  })
  it('returns undefined when no start_line', () => {
    expect(extractFileLine({ path: '/a/b.ts' }, '')).toBeUndefined()
  })
  it('ignores non-positive start_line', () => {
    expect(extractFileLine({ path: '/a/b.ts', start_line: 0 }, '')).toBeUndefined()
  })
})

describe('extractBashHint', () => {
  it('returns full command when truncated', () => {
    const long = 'a'.repeat(100)
    expect(extractBashHint({ command: long }, '')).toBe(long)
  })
  it('returns undefined for short commands', () => {
    expect(extractBashHint({ command: 'ls' }, '')).toBeUndefined()
  })
})

describe('extractSearchHint', () => {
  it('returns search path', () => {
    expect(extractSearchHint({ path: '/src' }, '')).toBe('/src')
  })
  it('returns undefined when no path', () => {
    expect(extractSearchHint({ pattern: '*.ts' }, '')).toBeUndefined()
  })
})

describe('extractStepOutputTitle', () => {
  it('extracts step_id from parsedArgs', () => {
    expect(extractStepOutputTitle({ step_id: 'step_1' }, '')).toBe('step_1')
  })
  it('falls back to raw args', () => {
    expect(extractStepOutputTitle(undefined, '{"step_id":"del_1"}')).toBe('del_1')
  })
  it('returns fallback when empty', () => {
    expect(extractStepOutputTitle(undefined, '{}')).toBe('step output')
  })
})

describe('extractFactsTitle', () => {
  it('joins keywords array', () => {
    expect(extractFactsTitle({ keywords: ['auth', 'jwt'] }, '')).toBe('auth, jwt')
  })
  it('falls back to raw args', () => {
    expect(extractFactsTitle(undefined, '{"keywords":["login"]}')).toBe('login')
  })
  it('returns fallback when no keywords', () => {
    expect(extractFactsTitle({}, '')).toBe('facts')
  })
})

describe('extractAttachmentTitle', () => {
  it('extracts attachment_id from parsedArgs', () => {
    expect(extractAttachmentTitle({ attachment_id: 'att-42' }, '')).toBe('att-42')
  })
  it('falls back to raw args', () => {
    expect(extractAttachmentTitle(undefined, '{"attachment_id":"att-7"}')).toBe('att-7')
  })
  it('returns fallback when empty', () => {
    expect(extractAttachmentTitle(undefined, '{}')).toBe('attachment')
  })
})

describe('extractAttachmentId', () => {
  it('extracts attachment_id from parsedArgs', () => {
    expect(extractAttachmentId({ attachment_id: 'att-42' }, '')).toBe('att-42')
  })
  it('falls back to raw args', () => {
    expect(extractAttachmentId(undefined, '{"attachment_id":"att-7"}')).toBe('att-7')
  })
  it('returns undefined when absent (no fallback label)', () => {
    expect(extractAttachmentId(undefined, '{}')).toBeUndefined()
  })
})

describe('extractDelegationId', () => {
  it('extracts id from parsedArgs', () => {
    expect(extractDelegationId({ id: 'del_1' }, '')).toBe('del_1')
  })
  it('falls back to raw args', () => {
    expect(extractDelegationId(undefined, '{"id":"del_2"}')).toBe('del_2')
  })
  it('returns fallback when empty', () => {
    expect(extractDelegationId(undefined, '{}')).toBe('delegation')
  })
})

describe('extractDelegateTitle', () => {
  it('returns task count for multiple tasks', () => {
    expect(extractDelegateTitle({ tasks: [{ id: 'del_1' }, { id: 'del_2' }, { id: 'del_3' }] }, '')).toBe('3 tasks')
  })
  it('uses singular for a single task', () => {
    expect(extractDelegateTitle({ tasks: [{ id: 'del_1' }] }, '')).toBe('1 task')
  })
  it('falls back to raw args', () => {
    expect(extractDelegateTitle(undefined, '{"tasks":[{"id":"del_1"}]}')).toBe('1 task')
  })
  it('returns fallback when no tasks', () => {
    expect(extractDelegateTitle(undefined, '{}')).toBe('tasks')
  })
})

describe('extractReflectTitle', () => {
  it('extracts scope from parsedArgs', () => {
    expect(extractReflectTitle({ scope: 'delegation' }, '')).toBe('delegation')
  })
  it('defaults to trajectory when omitted', () => {
    expect(extractReflectTitle({}, '')).toBe('trajectory')
  })
  it('falls back to raw args', () => {
    expect(extractReflectTitle(undefined, '{"scope":"delegation"}')).toBe('delegation')
  })
  it('defaults to trajectory when empty', () => {
    expect(extractReflectTitle(undefined, '{}')).toBe('trajectory')
  })
})

describe('extractDeclarePlanTitle', () => {
  it('renders mode and task count', () => {
    expect(extractDeclarePlanTitle({ mode: 'await_approval', tasks: [{ id: 'step_1' }, { id: 'step_2' }] }, '')).toBe('await_approval · 2 tasks')
  })
  it('defaults mode to present', () => {
    expect(extractDeclarePlanTitle({ tasks: [{ id: 'step_1' }] }, '')).toBe('present · 1 task')
  })
  it('falls back to raw args', () => {
    expect(extractDeclarePlanTitle(undefined, '{"tasks":[{"id":"s1"},{"id":"s2"}]}')).toBe('present · 2 tasks')
  })
  it('returns bare label when no tasks', () => {
    expect(extractDeclarePlanTitle(undefined, '{}')).toBe('present · tasks')
  })
})

describe('extractExecutePlanTitle', () => {
  it('returns static label', () => {
    expect(extractExecutePlanTitle()).toBe('plan')
  })
})

describe('extractProposeGoalTitle', () => {
  it('extracts condition from parsedArgs', () => {
    expect(extractProposeGoalTitle({ condition: 'all tests pass' }, '')).toBe('all tests pass')
  })
  it('falls back to raw args', () => {
    expect(extractProposeGoalTitle(undefined, '{"condition":"build green"}')).toBe('build green')
  })
  it('returns fallback when empty', () => {
    expect(extractProposeGoalTitle(undefined, '{}')).toBe('goal')
  })
})
