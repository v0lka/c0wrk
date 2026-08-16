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
import { getSecuritySettings } from '@/api/config'

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

  it('a failed save surfaces the backend error and reverts the UI to the enforced settings', async () => {
    updateSecuritySettingsMock.mockRejectedValueOnce(
      new Error('security group "execute" blacklist pattern "(" does not compile'),
    )
    await render()

    const execSelect = selects().find((s) => s.getAttribute('aria-label') === 'Execute policy')
    if (!execSelect) throw new Error('Execute policy dropdown not found')
    // getSecuritySettings has already run once for the initial load; count
    // from here so the assertion is independent of earlier tests.
    // vi.mocked() exposes the mock's call log while keeping the API type.
    const callsBefore = vi.mocked(getSecuritySettings).mock.calls.length

    await act(async () => {
      execSelect.value = 'deny'
      execSelect.dispatchEvent(new Event('change', { bubbles: true }))
    })

    // The backend rejection message is visible to the user...
    const text = container.textContent ?? ''
    expect(text).toContain('blacklist pattern "(" does not compile')
    // ...and the displayed policy re-syncs with the enforced state
    // (execute stays user_confirm from getSecuritySettings, not the
    // optimistic 'deny' that was never persisted).
    expect(execSelect.value).toBe('user_confirm')
    expect(getSecuritySettings).toHaveBeenCalledTimes(callsBefore + 1) // rollback re-fetch
  })

  it('a Wails string rejection (not an Error) still surfaces the backend message', async () => {
    // Wails v2 rejects RPC failures with the Go error text as a plain string.
    updateSecuritySettingsMock.mockRejectedValueOnce(
      'security groups payload is missing: local_read, remote_mcp',
    )
    await render()

    const execSelect = selects().find((s) => s.getAttribute('aria-label') === 'Execute policy')
    if (!execSelect) throw new Error('Execute policy dropdown not found')

    await act(async () => {
      execSelect.value = 'deny'
      execSelect.dispatchEvent(new Event('change', { bubbles: true }))
    })

    const text = container.textContent ?? ''
    expect(text).toContain('missing: local_read, remote_mcp')
  })

  it('a failed initial load renders no editable controls (fail-closed) and retries into the settings', async () => {
    // The initial load fails: local group state would be empty, and any save
    // from it would replace every live policy with defaults. The component
    // must therefore not render the editable surface at all.
    vi.mocked(getSecuritySettings).mockRejectedValueOnce(new Error('config not initialized'))
    await render()

    expect(selects()).toHaveLength(0)
    const text = container.textContent ?? ''
    expect(text).toContain('Failed to load security settings: config not initialized')
    expect(text).toContain('Editing is disabled')
    expect(updateSecuritySettingsMock).not.toHaveBeenCalled()

    // Retry re-loads; the default mock resolves, so the editable surface
    // appears with the enforced policies.
    const retry = container.querySelector('button[type="button"]')
    if (!retry) throw new Error('Retry button not found')
    await act(async () => {
      retry.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(selects()).toHaveLength(7)
    const execSelect = selects().find((s) => s.getAttribute('aria-label') === 'Execute policy')
    expect(execSelect?.value).toBe('user_confirm')
  })
})
