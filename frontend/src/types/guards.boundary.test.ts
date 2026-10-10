// Boundary-hardening tests for types/guards.ts (mass-bugfix cluster C9):
// the RPC guards must validate the TYPE of every field their consumers
// read/render — not just key presence — and the new array-element guards
// must reject malformed elements.

import { describe, expect, it } from 'vitest'
import {
  isAgentDescriptor,
  isBlackboardState,
  isChatMessage,
  isFileEntry,
  isGPUDevice,
  isMCPServerStatus,
  isProjectInfo,
  isSessionBookmark,
  isSessionInfo,
  isSkillDescriptor,
  isTokenInfo,
  isToolInfo,
  isVectorStoreEntry,
} from './guards'

describe('isSessionInfo (declared-string identity fields)', () => {
  const base = {
    id: 's1',
    project_id: 'p1',
    name: 'Session 1',
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    archived: false,
    pinned: false,
    active: true,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: '',
    family: '',
    has_unfinished_task: false,
  }

  it('accepts a canonical payload', () => {
    expect(isSessionInfo(base)).toBe(true)
  })

  it('accepts absent optional fields', () => {
    expect(isSessionInfo({ ...base })).toBe(true)
  })

  it('rejects a non-string name (sessionStore calls .trim() on it)', () => {
    expect(isSessionInfo({ ...base, name: 42 })).toBe(false)
    expect(isSessionInfo({ ...base, name: { text: 'x' } })).toBe(false)
  })

  it('rejects non-string id / project_id', () => {
    expect(isSessionInfo({ ...base, id: null })).toBe(false)
    expect(isSessionInfo({ ...base, project_id: 7 })).toBe(false)
  })

  it('rejects a missing name key', () => {
    const missing = { ...base } as Record<string, unknown>
    delete missing.name
    expect(isSessionInfo(missing)).toBe(false)
  })
})

describe('isProjectInfo (selectTitleScope / ProjectSelector render)', () => {
  const base = { id: 'p1', name: 'Project', workspace_path: '/tmp/ws' }

  it('accepts a canonical payload', () => {
    expect(isProjectInfo(base)).toBe(true)
  })

  it('rejects a non-string name (app-root selector calls .trim() on it)', () => {
    expect(isProjectInfo({ ...base, name: ['x'] })).toBe(false)
  })

  it('rejects non-string id / workspace_path', () => {
    expect(isProjectInfo({ ...base, id: 1 })).toBe(false)
    expect(isProjectInfo({ ...base, workspace_path: null })).toBe(false)
  })
})

describe('isChatMessage (history reconstruction)', () => {
  const base = { id: 1, session_id: 's1', role: 'user', content: 'hello', metadata: [], created_at: 'x' }

  it('accepts a canonical payload', () => {
    expect(isChatMessage(base)).toBe(true)
  })

  it('rejects a present non-string content (reconstructContent calls .startsWith on it)', () => {
    expect(isChatMessage({ ...base, content: 42 })).toBe(false)
    expect(isChatMessage({ ...base, content: { body: 'x' } })).toBe(false)
  })

  it('rejects non-string role / session_id', () => {
    expect(isChatMessage({ ...base, role: null })).toBe(false)
    expect(isChatMessage({ ...base, session_id: 9 })).toBe(false)
  })
})

describe('isSessionBookmark (BookmarksPanel render)', () => {
  const base = { id: 'b1', session_id: 's1', event_key: 'k', title: 'T', created_at: 'x' }

  it('accepts a canonical payload', () => {
    expect(isSessionBookmark(base)).toBe(true)
  })

  it('rejects a non-string title (rendered as a React child)', () => {
    expect(isSessionBookmark({ ...base, title: {} })).toBe(false)
  })

  it('rejects non-string id / event_key', () => {
    expect(isSessionBookmark({ ...base, id: 2 })).toBe(false)
    expect(isSessionBookmark({ ...base, event_key: 3 })).toBe(false)
  })
})

describe('isTokenInfo (status bar render)', () => {
  const base = { total_input_tokens: 1, total_output_tokens: 2, model: 'm', family: 'f' }

  it('accepts a canonical payload', () => {
    expect(isTokenInfo(base)).toBe(true)
  })

  it('rejects a present non-string model / family', () => {
    expect(isTokenInfo({ ...base, model: { name: 'm' } })).toBe(false)
    expect(isTokenInfo({ ...base, family: 5 })).toBe(false)
  })

  it('rejects a missing family key', () => {
    const missing = { ...base } as Record<string, unknown>
    delete missing.family
    expect(isTokenInfo(missing)).toBe(false)
  })

  it('rejects non-number token counts', () => {
    expect(isTokenInfo({ ...base, total_input_tokens: '1' })).toBe(false)
  })
})

describe('isFileEntry (file tree string methods + render)', () => {
  const base = { name: 'a.md', path: '/ws/a.md', is_dir: false }

  it('accepts a canonical payload', () => {
    expect(isFileEntry(base)).toBe(true)
  })

  it('rejects a non-string name (consumers call .toLowerCase/.localeCompare on it)', () => {
    expect(isFileEntry({ ...base, name: 42 })).toBe(false)
  })

  it('rejects a non-string path and a non-boolean is_dir', () => {
    expect(isFileEntry({ ...base, path: null })).toBe(false)
    expect(isFileEntry({ ...base, is_dir: 'false' })).toBe(false)
  })
})

describe('isMCPServerStatus', () => {
  const base = { name: 'srv', transport: 'stdio', connected: true, starting: false, tool_count: 0, tools: [] }

  it('accepts a canonical payload', () => {
    expect(isMCPServerStatus(base)).toBe(true)
  })

  it('accepts a null tools array (Go nil slice → JSON null)', () => {
    expect(isMCPServerStatus({ ...base, tools: null })).toBe(true)
  })

  it('rejects a non-string name and a non-boolean connected', () => {
    expect(isMCPServerStatus({ ...base, name: { x: 1 } })).toBe(false)
    expect(isMCPServerStatus({ ...base, connected: 'yes' })).toBe(false)
  })
})

describe('isBlackboardState (BlackboardPanel dereferences all four collections)', () => {
  const base = {
    task_id: 't1',
    session_id: 's1',
    status: 'in_progress',
    original_request: 'r',
    step_results: {},
    reflections: [],
    facts: [],
    attachments: [],
  }

  it('accepts a canonical payload (empty collections included)', () => {
    expect(isBlackboardState(base)).toBe(true)
  })

  it('rejects a missing facts array (panel reads state.facts.length)', () => {
    const missing = { ...base } as Record<string, unknown>
    delete missing.facts
    expect(isBlackboardState(missing)).toBe(false)
  })

  it('rejects a null reflections array', () => {
    expect(isBlackboardState({ ...base, reflections: null })).toBe(false)
  })

  it('rejects a null step_results object (panel calls Object.keys on it)', () => {
    expect(isBlackboardState({ ...base, step_results: null })).toBe(false)
  })
})

describe('array-element guards for the #11 wrappers', () => {
  it('isAgentDescriptor / isSkillDescriptor require string name + description', () => {
    expect(isAgentDescriptor({ name: 'a', description: 'd' })).toBe(true)
    expect(isSkillDescriptor({ name: 'a', description: 'd' })).toBe(true)
    expect(isAgentDescriptor({ name: 'a' })).toBe(false)
    expect(isSkillDescriptor({ name: 1, description: 'd' })).toBe(false)
    expect(isAgentDescriptor(null)).toBe(false)
  })

  it('isToolInfo requires all five string fields', () => {
    expect(isToolInfo({ name: 't', description: 'd', source: 'core', group: 'execute', policy: 'allow' })).toBe(true)
    expect(isToolInfo({ name: 't', description: 'd', source: 'core', group: 1, policy: 'allow' })).toBe(false)
    expect(isToolInfo({ name: 't', description: 'd', source: 'core', policy: 'allow' })).toBe(false)
  })

  it('isGPUDevice requires a numeric index and a string name', () => {
    expect(isGPUDevice({ index: 0, name: 'GPU 0' })).toBe(true)
    expect(isGPUDevice({ index: '0', name: 'GPU 0' })).toBe(false)
    expect(isGPUDevice({ index: 0 })).toBe(false)
  })

  it('isVectorStoreEntry requires the rendered strings/numbers and tolerates absent optional scores', () => {
    const base = {
      file_path: 'a/b.ts',
      file_name: 'b.ts',
      content: 'code',
      language: 'ts',
      score: 0.9,
      start_line: 1,
      end_line: 2,
    }
    expect(isVectorStoreEntry(base)).toBe(true)
    expect(isVectorStoreEntry({ ...base, vector_score: 0.5 })).toBe(true)
    expect(isVectorStoreEntry({ ...base, content: 42 })).toBe(false)
    expect(isVectorStoreEntry({ ...base, score: 'high' })).toBe(false)
    expect(isVectorStoreEntry({ ...base, vector_rank: '1' })).toBe(false)
  })
})
