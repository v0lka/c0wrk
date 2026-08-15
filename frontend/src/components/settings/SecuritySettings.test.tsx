// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Mock the API layer: the component must not touch real Wails bindings.
const updateSecuritySettingsMock = vi.fn()
vi.mock('@/api/config', () => ({
  getSecuritySettings: vi.fn().mockResolvedValue({
    groups: {
      local_read: { policy: 'allow' },
      remote_read: { policy: 'allow' },
      local_write: { policy: 'user_confirm' },
      execute: { policy: 'user_confirm', blacklist: ['rm\\s+-rf\\s+/'] },
      local_mcp: { policy: 'user_confirm' },
      remote_mcp: { policy: 'user_confirm' },
      remote_write: { policy: 'user_confirm' },
    },
    auto_approve_workspace_writes: false,
    smart_approve: false,
    judge_available: false,
  }),
  updateSecuritySettings: (...args: unknown[]) => updateSecuritySettingsMock(...args),
}))

vi.mock('@/api/mcp', () => ({
  getToolList: vi.fn().mockResolvedValue([
    { name: 'read_file', description: 'Reads a file', source: 'core', group: 'local_read', policy: 'allow' },
    { name: 'bash_exec', description: 'Runs a shell command', source: 'core', group: 'execute', policy: 'user_confirm' },
    { name: 'query_graph', description: 'MCP tool', source: 'mcp:memory', group: 'local_mcp', policy: 'user_confirm' },
  ]),
}))

import { SecuritySettings } from './SecuritySettings'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  updateSecuritySettingsMock.mockReset()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

const render = () =>
  act(async () => {
    await root.render(<SecuritySettings />)
  })

const selects = () => Array.from(container.querySelectorAll('select'))

describe('SecuritySettings — group schema', () => {
  it('renders exactly seven group policy dropdowns', async () => {
    await render()
    // 7 group dropdowns — the reserved system group is never rendered.
    expect(selects()).toHaveLength(7)
    const labels = selects().map((s) => s.getAttribute('aria-label'))
    expect(labels).toContain('Execute policy')
    expect(labels).toContain('Remote Write policy')
    expect(labels.filter((l) => l?.startsWith('System'))).toHaveLength(0)
  })

  it('lists tools under their group and marks the tool-less remote_write group', async () => {
    await render()
    const text = container.textContent ?? ''
    expect(text).toContain('read_file')
    expect(text).toContain('bash_exec')
    expect(text).toContain('mcp:memory')
    expect(text).toContain('No tools currently map to this group.')
  })

  it('changing a group policy saves the full seven-group schema only', async () => {
    await render()
    const execSelect = selects().find((s) => s.getAttribute('aria-label') === 'Execute policy')
    if (!execSelect) throw new Error('Execute policy dropdown not found')

    await act(async () => {
      execSelect.value = 'deny'
      execSelect.dispatchEvent(new Event('change', { bubbles: true }))
    })

    expect(updateSecuritySettingsMock).toHaveBeenCalledTimes(1)
    const payload = updateSecuritySettingsMock.mock.calls[0]![0] as {
      groups: Record<string, { policy: string; blacklist?: string[] }>
      default_policy?: unknown
      tool_policies?: unknown
      auto_approve_workspace_writes: boolean
      smart_approve: boolean
    }
    // Exactly the seven groups, no legacy per-tool keys.
    expect(Object.keys(payload.groups).sort()).toEqual(
      ['execute', 'local_mcp', 'local_read', 'local_write', 'remote_mcp', 'remote_read', 'remote_write'],
    )
    expect(payload.groups['execute']!.policy).toBe('deny')
    expect(payload.groups['execute']!.blacklist).toEqual(['rm\\s+-rf\\s+/'])
    expect(payload.groups['local_read']!.policy).toBe('allow')
    expect(payload.default_policy).toBeUndefined()
    expect(payload.tool_policies).toBeUndefined()
    expect(payload.auto_approve_workspace_writes).toBe(false)
    expect(payload.smart_approve).toBe(false)
  })
})
