// Boundary tests for the api/* wrapper validators (mass-bugfix cluster C9):
// array wrappers must validate ELEMENT shape, map wrappers must validate
// per-entry shape, and the research status path must default the required
// nested brief/metrics objects instead of letting a malformed project reach
// an unguarded dereference.

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import { listAgents } from '@/api/agents'
import { listSkills } from '@/api/skills'
import { getMCPServers, getToolList, listProviderModels } from '@/api/mcp'
import { searchVectorStore, listVectorIndexGPUs } from '@/api/vector'
import { getPendingActions } from '@/api/chat'
import { getReview, getReviewDiff } from '@/api/review'
import { getGitStatus } from '@/api/workspace'
import { getModelConfig, createModelProfile } from '@/api/config'
import { getResearchStatus } from '@/api/research'
import { getEmbeddedLLMStatus } from '@/api/embedded'
import { getSessionHistory } from '@/api/chat'
import { logger } from '@/lib/logger'

describe('#11 array wrappers validate element shape', () => {
  beforeEach(() => {
    for (const k of Object.keys(mockApp)) delete mockApp[k]
  })

  it('listAgents degrades to [] when one element is malformed', async () => {
    mockApp.ListAgents = vi.fn(() => Promise.resolve([
      { name: 'reviewer', description: 'd' },
      null,
      { name: 42, description: 'd' },
    ]))
    await expect(listAgents()).resolves.toEqual([])
  })

  it('listAgents passes a fully valid catalog through', async () => {
    mockApp.ListAgents = vi.fn(() => Promise.resolve([{ name: 'a', description: 'd' }]))
    await expect(listAgents()).resolves.toEqual([{ name: 'a', description: 'd' }])
  })

  it('listSkills degrades to [] on a malformed element', async () => {
    mockApp.ListSkills = vi.fn(() => Promise.resolve([{ name: 's', description: 'd' }, { name: 'x' }]))
    await expect(listSkills()).resolves.toEqual([])
  })

  it('getToolList degrades to [] on a malformed element', async () => {
    mockApp.GetToolList = vi.fn(() => Promise.resolve([
      { name: 't', description: 'd', source: 'core', group: 'execute', policy: 'allow' },
      { name: 't2', description: 'd', source: 'core', policy: 'allow' },
    ]))
    await expect(getToolList()).resolves.toEqual([])
  })

  it('listProviderModels drops non-string elements instead of passing them through', async () => {
    mockApp.ListProviderModels = vi.fn(() => Promise.resolve(['m1', 42, null, 'm2']))
    await expect(listProviderModels({ provider: 'p' } as never)).resolves.toEqual(['m1', 'm2'])
  })

  it('searchVectorStore degrades to [] when an entry lacks a string content field', async () => {
    mockApp.SearchVectorStore = vi.fn(() => Promise.resolve([
      { file_path: 'a.ts', file_name: 'a.ts', content: 'code', language: 'ts', score: 1, start_line: 1, end_line: 2 },
      { file_path: 'b.ts', file_name: 'b.ts', language: 'ts', score: 1, start_line: 1, end_line: 2 },
    ]))
    await expect(searchVectorStore({} as never)).resolves.toEqual([])
  })

  it('listVectorIndexGPUs degrades to [] on a malformed device entry', async () => {
    mockApp.ListVectorIndexGPUs = vi.fn(() => Promise.resolve([{ index: 0, name: 'GPU' }, { index: '1', name: 'GPU' }]))
    await expect(listVectorIndexGPUs()).resolves.toEqual([])
  })
})

describe('#88 getMCPServers per-entry object validation', () => {
  beforeEach(() => {
    delete mockApp.GetMCPServers
    vi.mocked(logger.warn).mockClear()
    vi.mocked(logger.error).mockClear()
  })

  it('keeps the valid entries and warns+skips one null entry instead of returning {}', async () => {
    mockApp.GetMCPServers = vi.fn(() => Promise.resolve({
      good: { transport: 'stdio', command: 'c' },
      bad: null,
    }))
    const servers = await getMCPServers()
    expect(Object.keys(servers)).toEqual(['good'])
    expect(servers['good']).toEqual({ transport: 'stdio', command: 'c', timeout: '', call_timeout: '', mode: 'auto' })
    // The skip is warn-level (the load still succeeds — no error banner in
    // MCPSettings) and names the offending key without logging payloads.
    expect(logger.warn).toHaveBeenCalledTimes(1)
    expect(vi.mocked(logger.warn).mock.calls[0]?.[0]).toContain('bad')
    expect(logger.error).not.toHaveBeenCalled()
  })

  it('normalizes a fully-object payload as before', async () => {
    mockApp.GetMCPServers = vi.fn(() => Promise.resolve({ srv: { transport: 'stdio', command: 'c' } }))
    const servers = await getMCPServers()
    expect(servers['srv']!.timeout).toBe('')
    expect(servers['srv']!.mode).toBe('auto')
    expect(logger.warn).not.toHaveBeenCalled()
  })
})

describe('#32 getPendingActions per-element validation', () => {
  beforeEach(() => {
    delete mockApp.GetPendingActions
  })

  it('drops a malformed tool_confirm element and keeps the well-formed ones', async () => {
    mockApp.GetPendingActions = vi.fn(() => Promise.resolve({
      tool_confirms: [
        { confirm_id: 'c1', tool: 'bash_exec', args: '{}' },
        { confirm_id: 7, tool: 'bash_exec', args: '{}' },
        null,
      ],
      step_limits: [{ request_id: 'r1', current_step: 1, max_steps: 10 }],
      plan_approvals: [{ request_id: 'r2', plan_path: 'p', plan_content: 'c' }],
      ask_user: [{ request_id: 'r3', questions: [{ id: 'q', question: 'Q?', options: [{ label: 'A', value: 'a' }] }] }],
      goal_proposals: [{ request_id: 'r4', condition: 'c', verify: 'v' }],
    }))
    const pending = await getPendingActions('s1')
    expect(pending).not.toBeNull()
    expect(pending!.tool_confirms.map((c) => c.confirm_id)).toEqual(['c1'])
    expect(pending!.step_limits).toHaveLength(1)
    expect(pending!.ask_user).toHaveLength(1)
    expect(pending!.goal_proposals).toHaveLength(1)
  })

  it('normalizes null kinds to [] (Go nil slice) and drops malformed ask_user questions', async () => {
    mockApp.GetPendingActions = vi.fn(() => Promise.resolve({
      tool_confirms: null,
      step_limits: null,
      plan_approvals: null,
      ask_user: [{ request_id: 'r3', questions: null }],
      goal_proposals: null,
    }))
    const pending = await getPendingActions('s1')
    expect(pending).toEqual({
      tool_confirms: [],
      step_limits: [],
      plan_approvals: [],
      ask_user: [],
      goal_proposals: [],
    })
  })
})

describe('#182 isChatMessage content type-check at the history boundary', () => {
  it('getSessionHistory returns [] when a persisted row carries a non-string content', async () => {
    mockApp.GetSessionHistory = vi.fn(() => Promise.resolve([
      { id: 1, session_id: 's1', role: 'user', content: 'ok', metadata: [], created_at: 'x' },
      { id: 2, session_id: 's1', role: 'thought', content: 42, metadata: [], created_at: 'x' },
    ]))
    await expect(getSessionHistory('s1')).resolves.toEqual([])
  })
})

describe('#32/#140 review validators', () => {
  beforeEach(() => {
    for (const k of Object.keys(mockApp)) delete mockApp[k]
  })

  it('getReview throws on a comment element with a non-string body (the header calls .trim() on it)', async () => {
    mockApp.GetReview = vi.fn(() => Promise.resolve({
      session_id: 's1',
      status: 'open',
      general_comment: '',
      hunk_comments: [
        { id: 'h1', session_id: 's1', file_path: 'a.go', hunk_id: 'h', body: 42, created_at: 'x' },
      ],
      file_comments: [],
      updated_at: 'x',
    }))
    await expect(getReview('s1')).rejects.toThrow()
  })

  it('getReview accepts a fully valid payload', async () => {
    mockApp.GetReview = vi.fn(() => Promise.resolve({
      session_id: 's1',
      status: 'open',
      general_comment: 'g',
      hunk_comments: [],
      file_comments: [{ id: 'f1', session_id: 's1', file_path: 'a.go', body: 'b', created_at: 'x' }],
      updated_at: 'x',
    }))
    await expect(getReview('s1')).resolves.toBeDefined()
  })

  it('getReviewDiff returns [] when a hunk element is malformed (render reads hunk.raw)', async () => {
    mockApp.GetReviewDiff = vi.fn(() => Promise.resolve([
      {
        path: 'a.go',
        hunks: [
          { raw: '@@ -1 +1 @@', old_start: 1, old_count: 1, new_start: 1, new_count: 1 },
          { raw: '@@ -2 +2 @@', old_start: 2, old_count: 1, new_start: null, new_count: 1 },
        ],
      },
    ]))
    await expect(getReviewDiff()).resolves.toEqual([])
  })
})

describe('#34 getGitStatus per-entry validation', () => {
  it('degrades to {} when one map value is null (toEntries dereferences each entry)', async () => {
    mockApp.GetGitStatus = vi.fn(() => Promise.resolve({
      'a.go': { status: 'M', staged: false, index_status: 'M', worktree_status: 'M' },
      'b.go': null,
    }))
    await expect(getGitStatus('/ws')).resolves.toEqual({})
  })

  it('passes a fully-typed map through', async () => {
    const entry = { status: 'M', staged: false, index_status: 'M', worktree_status: 'M' }
    mockApp.GetGitStatus = vi.fn(() => Promise.resolve({ 'a.go': entry }))
    await expect(getGitStatus('/ws')).resolves.toEqual({ 'a.go': entry })
  })
})

describe('#139 getModelConfig / createModelProfile', () => {
  const baseConfig = {
    model: 'm',
    context_window: 128,
    output_limit: 4096,
    tokenizer_type: 'o200k',
    family: 'gpt',
    protocol: 'openai',
    capabilities: { attachment: true, reasoning: false, temperature: true, tool_call: true },
    default_context_window: 128,
    default_output_limit: 4096,
    default_tokenizer_type: 'o200k',
    default_family: 'gpt',
    default_protocol: 'openai',
    default_capabilities: { attachment: true, reasoning: false, temperature: true, tool_call: true },
    has_override: false,
  }

  beforeEach(() => {
    for (const k of Object.keys(mockApp)) delete mockApp[k]
  })

  it('passes a canonical payload through', async () => {
    mockApp.GetModelConfig = vi.fn(() => Promise.resolve(structuredClone(baseConfig)))
    await expect(getModelConfig('m')).resolves.toBeDefined()
  })

  it('throws when capabilities is missing (the dialog dereferences it in render)', async () => {
    const payload = structuredClone(baseConfig) as Record<string, unknown>
    delete payload.capabilities
    mockApp.GetModelConfig = vi.fn(() => Promise.resolve(payload))
    await expect(getModelConfig('m')).rejects.toThrow()
  })

  it('throws when a capability flag is not a boolean', async () => {
    const payload = structuredClone(baseConfig) as Record<string, unknown>
    ;(payload.capabilities as Record<string, unknown>).tool_call = 'yes'
    mockApp.GetModelConfig = vi.fn(() => Promise.resolve(payload))
    await expect(getModelConfig('m')).rejects.toThrow()
  })

  it('createModelProfile rejects a non-string id', async () => {
    mockApp.CreateModelProfile = vi.fn(() => Promise.resolve(42))
    await expect(createModelProfile('generic', 'x')).rejects.toThrow()
  })

  it('createModelProfile returns a valid id', async () => {
    mockApp.CreateModelProfile = vi.fn(() => Promise.resolve('my-profile'))
    await expect(createModelProfile('generic', 'x')).resolves.toBe('my-profile')
  })
})

describe('#34/#121/#125 research status normalizes brief/metrics per project', () => {
  const statusWith = (project: unknown) => {
    mockApp.GetResearchStatus = vi.fn(() => Promise.resolve({
      enabled: true,
      project_id: 'R-001',
      research_root: '/ws/.research',
      root: { path: '/ws/.research', index: [], projects: [project], active_project_id: 'R-001' },
      pinned_research: [],
      pinned_hypotheses: {},
    }))
  }

  beforeEach(() => {
    delete mockApp.GetResearchStatus
  })

  it('defaults a missing brief and metrics instead of letting the render deref undefined', async () => {
    statusWith({ id: 'R-001', graph: { nodes: null, edges: null }, log: null })
    const status = await getResearchStatus('R-001')
    const project = status.root!.projects[0]!
    expect(project.brief).toEqual({ id: 'R-001', title: '' })
    expect(project.metrics).toEqual({
      total: 0, by_status: {}, confirmation_rate: 0, depth: 0, breadth: 0,
    })
    expect(project.graph.nodes).toEqual([])
    expect(project.graph.edges).toEqual([])
    expect(project.log).toEqual([])
  })

  it('drops a project without a string id while keeping the well-formed ones', async () => {
    mockApp.GetResearchStatus = vi.fn(() => Promise.resolve({
      enabled: true,
      project_id: 'R-001',
      research_root: '/ws/.research',
      root: {
        path: '/ws/.research',
        index: [],
        projects: [
          { id: 42, graph: { nodes: [], edges: [] }, log: [] },
          { id: 'R-002', graph: { nodes: [], edges: [] }, log: [] },
        ],
        active_project_id: 'R-002',
      },
      pinned_research: [],
      pinned_hypotheses: {},
    }))
    const status = await getResearchStatus('R-001')
    expect(status.root!.projects.map((p) => p.id)).toEqual(['R-002'])
  })

  it('keeps well-typed brief fields and filters a malformed active_front', async () => {
    statusWith({
      id: 'R-001',
      brief: { id: 'R-001', title: 'T', status: 'active', quarter: 5 },
      metrics: { total: 3, by_status: {}, confirmation_rate: 0.5, depth: 1, breadth: 2, active_front: ['H-001', 7] },
      graph: { nodes: [], edges: [] },
      log: [],
    })
    const status = await getResearchStatus('R-001')
    const project = status.root!.projects[0]!
    expect(project.brief.title).toBe('T')
    expect(project.brief.status).toBe('active')
    expect(project.brief.quarter).toBeUndefined()
    expect(project.metrics.total).toBe(3)
    expect(project.metrics.active_front).toEqual(['H-001'])
  })
})

describe('#157 isEmbeddedLLMStatus fit_warning', () => {
  const base = {
    state: 'installed',
    installed: true,
    installing: false,
    loading: false,
    loaded: false,
    packing: 'PQ2_0',
    backend: 'cpu',
    port: 43211,
    context_size: 131072,
    auto_unload_enabled: true,
    auto_unload_minutes: 60,
    idle_remaining_seconds: 0,
    base_url: 'http://127.0.0.1:43211/v1',
    model_id: 'embedded/Bonsai 2 27B',
    model_name: 'Bonsai 2 27B',
    runtime_version: 'prism-b10735',
    installed_at: '2026-01-01T00:00:00Z',
    model_file: '/x.gguf',
    pid: 4242,
    error: '',
    install_error: '',
    available: true,
    guards: [],
    leftover_runtime: false,
    leftover_weights: false,
    leftover_projection: false,
    devices: [],
    unified: true,
    host_ram_gib: 128,
    device_budget_mib: 24576,
    host_budget_mib: 8192,
    topology_probed_at: '',
    plan: {
      recorded: false,
      packing: '',
      kv_type: '',
      context_size: 0,
      fit: false,
      fit_arg: '',
      fit_target_mib: 0,
      fit_min_context: 0,
      offload_mode: 'auto',
      layers: -1,
      kv_offload: false,
      mmproj_offload: false,
      parallel: 0,
      cache_ram_mib: -1,
      gpu_family: '',
      device_budget_mib: 0,
      host_budget_mib: 0,
      expected_device_mib: 0,
      expected_host_mib: 0,
      notes: [],
    },
    reload_required: false,
  }

  beforeEach(() => {
    delete mockApp.GetEmbeddedLLMStatus
  })

  it('rejects a present non-string fit_warning (rendered as a React child)', async () => {
    mockApp.GetEmbeddedLLMStatus = vi.fn(() => Promise.resolve({ ...base, fit_warning: { w: 1 } }))
    await expect(getEmbeddedLLMStatus()).rejects.toThrow()
  })

  it('accepts an absent or string fit_warning', async () => {
    mockApp.GetEmbeddedLLMStatus = vi.fn(() => Promise.resolve({ ...base }))
    await expect(getEmbeddedLLMStatus()).resolves.toBeDefined()
    mockApp.GetEmbeddedLLMStatus = vi.fn(() => Promise.resolve({ ...base, fit_warning: 'did not fit' }))
    await expect(getEmbeddedLLMStatus()).resolves.toBeDefined()
  })
})
