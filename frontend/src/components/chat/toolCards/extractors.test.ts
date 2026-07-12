import { describe, it, expect } from 'vitest'
import {
  extractBashTitle, extractFileTitle, extractDirTitle,
  extractSearchTitle, extractUrlTitle, extractMemoTitle,
  extractStepOutputTitle, extractFactsTitle,
  extractFileHint, extractFileLine, extractBashHint, extractSearchHint,
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
  it('returns fallback when empty', () => {
    expect(extractBashTitle(undefined, '')).toBe('command')
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
