// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const getShellExecSettingsMock = vi.fn()
const updateShellExecSettingsMock = vi.fn()

vi.mock('@/api/config', () => ({
  getShellExecSettings: (...args: unknown[]) => getShellExecSettingsMock(...args),
  updateShellExecSettings: (...args: unknown[]) => updateShellExecSettingsMock(...args),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { ShellExecutionCard } from './ShellExecutionCard'
import type { ShellExecSettingsResponse } from '@/types/models'

const PLACEHOLDER = '{command}'

function settings(bashCommand: string[], bashShell: string): ShellExecSettingsResponse {
  return {
    bash_exec: { command: bashCommand, shell: bashShell },
    posh_exec: { command: [], shell: '' },
  }
}

let container: HTMLDivElement
let root: Root

async function renderCard() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root.render(<ShellExecutionCard />)
  })
}

async function unmountCard() {
  await act(async () => {
    root.unmount()
  })
  container.remove()
}

const q = (id: string) => container.querySelector(`[data-testid="${id}"]`) as HTMLElement | null

beforeEach(() => {
  getShellExecSettingsMock.mockReset().mockResolvedValue(settings([], ''))
  updateShellExecSettingsMock.mockReset().mockResolvedValue(undefined)
})

describe('ShellExecutionCard', () => {
  it('renders the default load state as a no-op save', async () => {
    await renderCard()
    expect(q('shell-exec-card')).not.toBeNull()
    expect((q('shell-exec-bash-binary') as HTMLInputElement).value).toBe('')
    // An empty section is a valid (no-op) save; the button must be enabled
    // once the load resolved.
    expect(q('shell-exec-save')?.hasAttribute('disabled')).toBe(false)
    expect(updateShellExecSettingsMock).not.toHaveBeenCalled()
    await unmountCard()
  })

  it('shows a load error when the backend rejects the read', async () => {
    getShellExecSettingsMock.mockRejectedValue(new Error('boom'))
    await renderCard()
    expect(q('shell-exec-error')?.textContent).toContain('Failed to load')
    await unmountCard()
  })

  it('pre-fills an existing override, previews the command, and enables save', async () => {
    getShellExecSettingsMock.mockResolvedValue(settings(['/opt/homebrew/bin/zsh', '-c', PLACEHOLDER], 'zsh'))
    await renderCard()
    expect((q('shell-exec-bash-binary') as HTMLInputElement).value).toBe('/opt/homebrew/bin/zsh')
    expect(q('shell-exec-bash-arg-0')?.textContent).toBe('-c')
    expect(q('shell-exec-bash-arg-1')?.textContent).toBe(PLACEHOLDER)
    expect(q('shell-exec-bash-preview')?.textContent).toContain('/opt/homebrew/bin/zsh -c <command>')
    expect(q('shell-exec-save')?.hasAttribute('disabled')).toBe(false)
    await unmountCard()
  })

  it('saves the loaded override through the shell-exec RPC', async () => {
    getShellExecSettingsMock.mockResolvedValue(settings(['/bin/zsh', '-c', PLACEHOLDER], 'zsh'))
    await renderCard()
    await act(async () => {
      q('shell-exec-save')?.click()
    })
    expect(updateShellExecSettingsMock).toHaveBeenCalledTimes(1)
    expect(updateShellExecSettingsMock).toHaveBeenCalledWith({
      bash_exec: { command: ['/bin/zsh', '-c', PLACEHOLDER], shell: 'zsh' },
      posh_exec: { command: [], shell: '' },
    })
    expect(q('shell-exec-saved')?.textContent).toContain('Saved')
    await unmountCard()
  })

  it('surfaces a backend save failure as an inline alert', async () => {
    getShellExecSettingsMock.mockResolvedValue(settings(['/bin/zsh', '-c', PLACEHOLDER], 'zsh'))
    updateShellExecSettingsMock.mockRejectedValue(new Error('re-registration failed'))
    await renderCard()
    await act(async () => {
      q('shell-exec-save')?.click()
    })
    expect(q('shell-exec-error')?.textContent).toContain('re-registration failed')
    await unmountCard()
  })

  it('disables save with a hint when the placeholder row is removed', async () => {
    getShellExecSettingsMock.mockResolvedValue(settings(['/bin/sh', '-c', PLACEHOLDER], 'sh'))
    await renderCard()
    // Remove the "{command}" row (index 1).
    await act(async () => {
      q('shell-exec-bash-arg-remove-1')?.click()
    })
    expect(q('shell-exec-bash-preview')?.textContent).toContain('exactly one')
    expect(q('shell-exec-save')?.hasAttribute('disabled')).toBe(true)
    expect(updateShellExecSettingsMock).not.toHaveBeenCalled()
    await unmountCard()
  })
})
