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
      execute: { policy: 'user_confirm', blocklist: ['rm\\s+-rf\\s+/'] },
      local_mcp: { policy: 'user_confirm' },
      remote_mcp: { policy: 'user_confirm' },
      remote_write: { policy: 'user_confirm' },
    },
    auto_approve_workspace_writes: false,
    autonomy_mode: 'standard',
    judge_available: false,
  }),
  updateSecuritySettings: (...args: unknown[]) => updateSecuritySettingsMock(...args),
}))

// Failure-path tests (backend rejections on load/save) intentionally make
// SecuritySettings' logger.error fire; mock the logger so the expected
// errors don't pollute vitest output.
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

vi.mock('@/api/mcp', () => ({
  getToolList: vi.fn().mockResolvedValue([
    { name: 'read_file', description: 'Reads a file', source: 'core', group: 'local_read', policy: 'allow' },
    { name: 'bash_exec', description: 'Runs a shell command', source: 'core', group: 'execute', policy: 'user_confirm' },
    { name: 'query_graph', description: 'MCP tool', source: 'mcp:memory', group: 'local_mcp', policy: 'user_confirm' },
  ]),
}))

// The Trusted/Hardened repos dialogs (rendered by SecuritySettings) talk to
// the git-config-risk API; mocked so the button tests never touch Wails.
vi.mock('@/api/gitConfigRisk', () => ({
  getTrustedGitRepos: vi.fn().mockResolvedValue(['/Users/dev/repo-a']),
  removeTrustedGitRepo: vi.fn().mockResolvedValue(undefined),
  getHardenGitRepos: vi.fn().mockResolvedValue(['/srv/hardened-repo']),
  removeHardenGitRepo: vi.fn().mockResolvedValue(undefined),
}))

import { SecuritySettings } from './SecuritySettings'
import { getSecuritySettings } from '@/api/config'
import type { SecuritySettingsResponse, SilentModeSettings } from '@/types/models'

// Radix dropdown-menu popper positioning (autoUpdate) observes elements with
// ResizeObserver, which jsdom does not provide.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

let container: HTMLDivElement
let root: Root
let previousRoot: Root | null = null

beforeEach(() => {
  updateSecuritySettingsMock.mockReset()
  // Unmount the previous render BEFORE wiping document.body: the dialog tests
  // portal Radix layers into body, and detaching those nodes out from under a
  // live React root leaves Radix's layer stack holding stale references (a
  // later removeChild then throws "node to be removed is not a child").
  if (previousRoot) {
    act(() => {
      previousRoot?.unmount()
    })
    previousRoot = null
  }
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
  previousRoot = root
})

const render = () =>
  act(async () => {
    await root.render(<SecuritySettings />)
  })

// The policy dropdowns are custom comboboxes built on the Radix
// dropdown-menu primitives (button trigger + portaled menu), not native
// <select>, so their colors are themable on Windows too. The trigger carries
// the aria-label; picking a value happens through the menu portaled to
// document.body.
const selects = () =>
  Array.from(container.querySelectorAll<HTMLButtonElement>('button[aria-haspopup="menu"]'))

const findSelect = (ariaLabel: string) =>
  selects().find((s) => s.getAttribute('aria-label') === ariaLabel)

/** Open the combobox with `ariaLabel` and click its `optionLabel` option. */
async function pickOption(ariaLabel: string, optionLabel: string): Promise<void> {
  const trigger = findSelect(ariaLabel)
  if (!trigger) throw new Error(`${ariaLabel} dropdown not found`)
  // Radix's DropdownMenuTrigger toggles on `pointerdown`, not `click`; the
  // awaited act also lets Radix install its document-level listeners.
  await act(async () => {
    trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  const option = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    .find((o) => o.textContent?.includes(optionLabel))
  if (!option) throw new Error(`Option "${optionLabel}" not found in ${ariaLabel} menu`)
  await act(async () => {
    option.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** The segmented control's radio input for one autonomy mode. */
const autonomyRadio = (mode: string) =>
  container.querySelector<HTMLInputElement>(`input[name="autonomy-mode"][value="${mode}"]`)

/** Switch the autonomy mode through the segmented control (a save fires). */
async function switchMode(mode: string): Promise<void> {
  const radio = autonomyRadio(mode)
  if (!radio) throw new Error(`autonomy mode radio "${mode}" not found`)
  await act(async () => {
    radio.click()
  })
}

/** The payload of the most recent UpdateSecuritySettings call. */
const lastPayload = () => {
  const calls = updateSecuritySettingsMock.mock.calls
  return calls[calls.length - 1]![0] as {
    autonomy_mode: string
    silent_mode: unknown
    groups: Record<string, unknown>
  }
}

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
    await pickOption('Execute policy', 'Deny')

    expect(updateSecuritySettingsMock).toHaveBeenCalledTimes(1)
    const payload = updateSecuritySettingsMock.mock.calls[0]![0] as {
      groups: Record<string, { policy: string; blocklist?: string[] }>
      default_policy?: unknown
      tool_policies?: unknown
      auto_approve_workspace_writes: boolean
      autonomy_mode: string
    }
    // Exactly the seven groups, no legacy per-tool keys.
    expect(Object.keys(payload.groups).sort()).toEqual(
      ['execute', 'local_mcp', 'local_read', 'local_write', 'remote_mcp', 'remote_read', 'remote_write'],
    )
    expect(payload.groups['execute']!.policy).toBe('deny')
    expect(payload.groups['execute']!.blocklist).toEqual(['rm\\s+-rf\\s+/'])
    expect(payload.groups['local_read']!.policy).toBe('allow')
    expect(payload.default_policy).toBeUndefined()
    expect(payload.tool_policies).toBeUndefined()
    expect(payload.auto_approve_workspace_writes).toBe(false)
    expect(payload.autonomy_mode).toBe('standard')
  })

  it('a failed save surfaces the backend error and reverts the UI to the enforced settings', async () => {
    updateSecuritySettingsMock.mockRejectedValueOnce(
      new Error('security group "execute" blocklist pattern "(" does not compile'),
    )
    await render()

    const execSelect = findSelect('Execute policy')
    if (!execSelect) throw new Error('Execute policy dropdown not found')
    // getSecuritySettings has already run once for the initial load; count
    // from here so the assertion is independent of earlier tests.
    // vi.mocked() exposes the mock's call log while keeping the API type.
    const callsBefore = vi.mocked(getSecuritySettings).mock.calls.length

    await pickOption('Execute policy', 'Deny')

    // The backend rejection message is visible to the user...
    const text = container.textContent ?? ''
    expect(text).toContain('blocklist pattern "(" does not compile')
    // ...and the displayed policy re-syncs with the enforced state
    // (execute stays user_confirm from getSecuritySettings, not the
    // optimistic 'deny' that was never persisted). The combobox trigger
    // shows the effective option's label.
    expect(execSelect.textContent).toContain('User Confirm')
    expect(getSecuritySettings).toHaveBeenCalledTimes(callsBefore + 1) // rollback re-fetch
  })

  it('a Wails string rejection (not an Error) still surfaces the backend message', async () => {
    // Wails v2 rejects RPC failures with the Go error text as a plain string.
    updateSecuritySettingsMock.mockRejectedValueOnce(
      'security groups payload is missing: local_read, remote_mcp',
    )
    await render()

    await pickOption('Execute policy', 'Deny')

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
    const execSelect = findSelect('Execute policy')
    // The combobox trigger shows the enforced policy's option label.
    expect(execSelect?.textContent).toContain('User Confirm')
  })

  it('opens the trusted repositories dialog from the Trusted repos button', async () => {
    await render()

    const btn = container.querySelector<HTMLButtonElement>('[data-testid="trusted-repos-open"]')
    expect(btn).not.toBeNull()
    await act(async () => {
      btn?.click()
    })

    // Radix portals the dialog content to document.body.
    const dlg = document.querySelector('[data-testid="trusted-repos-dialog"]')
    expect(dlg?.textContent).toContain('/Users/dev/repo-a')
  })

  it('opens the hardened repositories dialog from the Hardened repos button', async () => {
    await render()

    const btn = container.querySelector<HTMLButtonElement>('[data-testid="harden-repos-open"]')
    expect(btn).not.toBeNull()
    await act(async () => {
      btn?.click()
    })

    // Radix portals the dialog content to document.body.
    const dlg = document.querySelector('[data-testid="harden-repos-dialog"]')
    expect(dlg?.textContent).toContain('/srv/hardened-repo')
  })
})

describe('SecuritySettings — autonomy mode', () => {
  it('renders the segmented control with the three modes, standard checked by default', async () => {
    await render()

    expect(autonomyRadio('standard')).not.toBeNull()
    expect(autonomyRadio('assisted')).not.toBeNull()
    expect(autonomyRadio('silent')).not.toBeNull()
    expect(autonomyRadio('standard')?.checked).toBe(true)
    expect(autonomyRadio('assisted')?.checked).toBe(false)

    // The selected mode's description is shown and states the terminal
    // difference honestly.
    const description = container.querySelector('[data-testid="autonomy-mode-description"]')
    expect(description?.textContent).toContain('Every gated decision stops at your terminal')
  })

  it('has no Smart Approve control anywhere (the flag is gone, not just unchecked)', async () => {
    await render()
    expect(container.textContent ?? '').not.toContain('Smart Approve')
    // Only the seven group dropdowns — no autonomy-era leftover toggle.
    expect(selects()).toHaveLength(7)
  })

  it('switching the mode persists it through UpdateSecuritySettings (Set round-trip)', async () => {
    await render()

    await switchMode('assisted')
    expect(lastPayload().autonomy_mode).toBe('assisted')
    expect(autonomyRadio('assisted')?.checked).toBe(true)

    await switchMode('silent')
    expect(lastPayload().autonomy_mode).toBe('silent')

    await switchMode('standard')
    expect(lastPayload().autonomy_mode).toBe('standard')
  })

  it('restores the enforced mode from GetSecuritySettings (Get round-trip)', async () => {
    vi.mocked(getSecuritySettings).mockResolvedValueOnce(
      securityResponse({
        autonomy_mode: 'silent',
        silent_mode: {
          tool_confirm: { mode: 'judge' },
          step_limit: { mode: 'auto' },
          ask_user: { mode: 'disable' },
          review_prompt: { mode: 'allow' },
        },
      }),
    )
    await render()

    expect(autonomyRadio('silent')?.checked).toBe(true)
    // The silent policy card is visible and carries the enforced sub-policy.
    expect(container.querySelector('[data-testid="silent-mode-card"]')).not.toBeNull()
    expect(findSelect('Review prompt mode')?.textContent).toContain('Allow')
  })

  it('in standard and assisted the policy card is hidden', async () => {
    await render()
    expect(container.querySelector('[data-testid="silent-mode-card"]')).toBeNull()
    expect(container.querySelector('[data-testid="silent-mode-warning"]')).toBeNull()
    expect(container.querySelector('[data-testid="silent-mode-subpolicies"]')).toBeNull()
    // Only the seven group dropdowns.
    expect(selects()).toHaveLength(7)

    await switchMode('assisted')
    expect(container.querySelector('[data-testid="silent-mode-card"]')).toBeNull()
    expect(selects()).toHaveLength(7)
  })

  it('switching to silent persists the mode and reveals the four sub-policy editors', async () => {
    await render()
    await switchMode('silent')

    // 7 group dropdowns + 4 silent-mode sub-policy dropdowns.
    expect(selects()).toHaveLength(11)
    expect(findSelect('Tool confirmations mode')).toBeTruthy()
    expect(findSelect('Step limit mode')).toBeTruthy()
    expect(findSelect('Ask user mode')).toBeTruthy()
    expect(findSelect('Review prompt mode')).toBeTruthy()

    // The autonomy warning states the risk inside the policy card.
    const warning = container.querySelector('[data-testid="silent-mode-warning"]')
    expect(warning?.textContent).toContain('Autonomy warning')

    expect(lastPayload().autonomy_mode).toBe('silent')
    expect(lastPayload().silent_mode).toEqual({
      tool_confirm: { mode: 'judge' },
      step_limit: { mode: 'auto' },
      ask_user: { mode: 'disable' },
      review_prompt: { mode: 'suppress' },
    })
  })

  it('echoes silent_mode back on an unrelated save so it is never reset', async () => {
    await render()
    await pickOption('Execute policy', 'Deny')

    expect(lastPayload().autonomy_mode).toBe('standard')
    expect(lastPayload().silent_mode).toEqual({
      tool_confirm: { mode: 'judge' },
      step_limit: { mode: 'auto' },
      ask_user: { mode: 'disable' },
      review_prompt: { mode: 'suppress' },
    })
  })

  it('changing a sub-policy persists the new mode', async () => {
    await render()
    await switchMode('silent')

    await pickOption('Review prompt mode', 'Allow')

    expect(lastPayload().autonomy_mode).toBe('silent')
    expect(lastPayload().silent_mode).toEqual({
      tool_confirm: { mode: 'judge' },
      step_limit: { mode: 'auto' },
      ask_user: { mode: 'disable' },
      review_prompt: { mode: 'allow' },
    })
  })

  it('assisted without a judge warns about the degraded behavior without blocking the control', async () => {
    // Default mock: judge_available false, mode standard.
    await render()

    expect(container.querySelector('[data-testid="autonomy-judge-warning"]')).toBeNull()

    await switchMode('assisted')

    const warning = container.querySelector('[data-testid="autonomy-judge-warning"]')
    expect(warning?.textContent).toContain('Judge unavailable')
    expect(warning?.textContent).toContain('degrades to a confirmation card')
    // Non-blocking: every mode stays selectable.
    expect(autonomyRadio('silent')?.disabled).toBe(false)
    expect(autonomyRadio('standard')?.disabled).toBe(false)
  })

  it('assisted with a configured judge shows no warning', async () => {
    vi.mocked(getSecuritySettings).mockResolvedValueOnce(
      securityResponse({ autonomy_mode: 'assisted', judge_available: true }),
    )
    await render()

    expect(autonomyRadio('assisted')?.checked).toBe(true)
    expect(container.querySelector('[data-testid="autonomy-judge-warning"]')).toBeNull()
  })
})

describe('SecuritySettings — silent mode judge gating', () => {
  /** Open the combobox menu; returns its rendered (portaled) menu items. */
  const openMenu = async (ariaLabel: string) => {
    const trigger = findSelect(ariaLabel)
    if (!trigger) throw new Error(`${ariaLabel} dropdown not found`)
    await act(async () => {
      trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    return Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]'))
  }

  const menuItem = (items: HTMLElement[], label: string) => {
    const item = items.find((o) => o.textContent?.includes(label))
    if (!item) throw new Error(`Option "${label}" not found`)
    return item
  }

  it('warns (without blocking) for the selected judge-dependent modes while the judge is unavailable', async () => {
    // Judge unavailable, silent mode with the judge-dependent defaults
    // (step_limit: auto selected; tool_confirm starts on a fixed mode) — the
    // stored judge-dependent posture must be called out, and picking another
    // judge-dependent mode must stay possible (warning, not a block).
    vi.mocked(getSecuritySettings).mockResolvedValueOnce(
      securityResponse({
        autonomy_mode: 'silent',
        silent_mode: {
          tool_confirm: { mode: 'deny' },
          step_limit: { mode: 'auto' },
          ask_user: { mode: 'disable' },
          review_prompt: { mode: 'suppress' },
        },
      }),
    )
    await render()

    const warning = container.querySelector('[data-testid="silent-mode-judge-warning"]')
    expect(warning?.textContent).toContain('Judge unavailable')
    expect(warning?.textContent).toContain('Step limit (Auto (judge decides))')
    expect(warning?.textContent).not.toContain('Tool confirmations')

    // The judge-dependent options are NOT disabled — and actually picking one
    // saves it (fail-closed is the documented behavior of the mode, so the
    // posture remains the operator's decision).
    const stepItems = await openMenu('Step limit mode')
    expect(menuItem(stepItems, 'Auto (judge decides)').matches('[data-disabled]')).toBe(false)
    await pickOption('Tool confirmations mode', 'Judge')
    expect(lastPayload().silent_mode).toMatchObject({ tool_confirm: { mode: 'judge' } })

    // With the now-selected judge-dependent tool_confirm the warning names it too.
    const updated = container.querySelector('[data-testid="silent-mode-judge-warning"]')
    expect(updated?.textContent).toContain('Tool confirmations (Judge)')
  })

  it('keeps the judge-dependent modes available when the judge is configured', async () => {
    vi.mocked(getSecuritySettings).mockResolvedValueOnce(
      securityResponse({
        autonomy_mode: 'silent',
        judge_available: true,
      }),
    )
    await render()

    expect(container.querySelector('[data-testid="silent-mode-judge-warning"]')).toBeNull()
    const toolItems = await openMenu('Tool confirmations mode')
    expect(menuItem(toolItems, 'Judge').matches('[data-disabled]')).toBe(false)
    const stepItems = await openMenu('Step limit mode')
    expect(menuItem(stepItems, 'Auto (judge decides)').matches('[data-disabled]')).toBe(false)
  })

  it('does not warn for a judge-independent posture without a judge', async () => {
    vi.mocked(getSecuritySettings).mockResolvedValueOnce(
      securityResponse({
        autonomy_mode: 'silent',
        silent_mode: {
          tool_confirm: { mode: 'deny' },
          step_limit: { mode: 'stop' },
          ask_user: { mode: 'disable' },
          review_prompt: { mode: 'suppress' },
        },
      }),
    )
    await render()

    expect(container.querySelector('[data-testid="silent-mode-subpolicies"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="silent-mode-judge-warning"]')).toBeNull()

    // The fixed modes stay selectable without a judge.
    await pickOption('Tool confirmations mode', 'Allow')
    expect(lastPayload().silent_mode).toMatchObject({ tool_confirm: { mode: 'allow' } })
  })
})

/** A full security-settings response for one-shot overrides of the default
 *  load mock (the default reports judge_available: false, standard mode). */
function securityResponse(over: {
  autonomy_mode?: SecuritySettingsResponse['autonomy_mode']
  judge_available?: boolean
  silent_mode?: SilentModeSettings
} = {}): SecuritySettingsResponse {
  return {
    groups: {
      local_read: { policy: 'allow' },
      remote_read: { policy: 'allow' },
      local_write: { policy: 'user_confirm' },
      execute: { policy: 'user_confirm' },
      local_mcp: { policy: 'user_confirm' },
      remote_mcp: { policy: 'user_confirm' },
      remote_write: { policy: 'user_confirm' },
    },
    auto_approve_workspace_writes: false,
    autonomy_mode: 'standard',
    judge_available: false,
    ...over,
  }
}
