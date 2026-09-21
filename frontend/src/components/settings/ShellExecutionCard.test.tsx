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

/** Drive a React-controlled text input by setting the native value and firing
 *  an input event (the value setter React's onChange listens to). */
function setInputValue(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  setter?.call(el, value)
  el.dispatchEvent(new window.Event('input', { bubbles: true }))
}

/** Open the shell combobox with `ariaLabel` and click its `optionLabel` option
 *  (the menu is portaled to document.body; Radix toggles on pointerdown). */
async function pickShell(ariaLabel: string, optionLabel: string) {
  const trigger = container.querySelector<HTMLButtonElement>(`button[aria-label="${ariaLabel}"]`)
  if (!trigger) throw new Error(`${ariaLabel} dropdown not found`)
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

  it('clears an existing override once the binary is emptied and every argument row removed', async () => {
    getShellExecSettingsMock.mockResolvedValue(settings(['/opt/homebrew/bin/zsh', '-c', PLACEHOLDER], 'zsh'))
    await renderCard()
    // Emptying the binary alone still reports an override (argument rows remain).
    await act(async () => {
      setInputValue(q('shell-exec-bash-binary') as HTMLInputElement, '')
    })
    expect(q('shell-exec-save')?.hasAttribute('disabled')).toBe(true)
    // Removing every argument row leaves no override, despite the seeded shell.
    await act(async () => {
      q('shell-exec-bash-arg-remove-1')?.click()
    })
    await act(async () => {
      q('shell-exec-bash-arg-remove-0')?.click()
    })
    expect(q('shell-exec-save')?.hasAttribute('disabled')).toBe(false)
    await act(async () => {
      q('shell-exec-save')?.click()
    })
    expect(updateShellExecSettingsMock).toHaveBeenCalledWith({
      bash_exec: { command: [], shell: '' },
      posh_exec: { command: [], shell: '' },
    })
    await unmountCard()
  })

  it('defaults the shell menu to Default and shows the built-in launch shape', async () => {
    await renderCard()
    expect(
      container.querySelector('button[aria-label="bash_exec declared shell"]')?.textContent,
    ).toContain('Default')
    expect(q('shell-exec-bash-preview')?.textContent).toContain('built-in: bash -c <command>')
    expect(
      container.querySelector('button[aria-label="posh_exec declared shell"]')?.textContent,
    ).toContain('Default')
    expect(q('shell-exec-posh-preview')?.textContent).toContain(
      'built-in: powershell.exe -NoProfile -NonInteractive -Command <command>',
    )
    // The command template controls are inert while Default is selected.
    expect((q('shell-exec-bash-binary') as HTMLInputElement).disabled).toBe(true)
    expect((q('shell-exec-bash-new-arg') as HTMLInputElement).disabled).toBe(true)
    await unmountCard()
  })

  it('shows the declared shell and enables the controls for a loaded override', async () => {
    getShellExecSettingsMock.mockResolvedValue(settings(['/opt/homebrew/bin/zsh', '-c', PLACEHOLDER], 'zsh'))
    await renderCard()
    expect(
      container.querySelector('button[aria-label="bash_exec declared shell"]')?.textContent,
    ).toContain('zsh')
    expect((q('shell-exec-bash-binary') as HTMLInputElement).disabled).toBe(false)
    await unmountCard()
  })

  it('arms the override when a shell is picked and disarms it back to Default', async () => {
    await renderCard()
    expect((q('shell-exec-bash-binary') as HTMLInputElement).disabled).toBe(true)
    await pickShell('bash_exec declared shell', 'zsh')
    expect((q('shell-exec-bash-binary') as HTMLInputElement).disabled).toBe(false)
    await act(async () => {
      setInputValue(q('shell-exec-bash-binary') as HTMLInputElement, '/bin/zsh')
    })
    expect((q('shell-exec-bash-binary') as HTMLInputElement).value).toBe('/bin/zsh')
    // Default clears the template and disarms the override again.
    await pickShell('bash_exec declared shell', 'Default')
    expect((q('shell-exec-bash-binary') as HTMLInputElement).value).toBe('')
    expect((q('shell-exec-bash-binary') as HTMLInputElement).disabled).toBe(true)
    expect(q('shell-exec-bash-preview')?.textContent).toContain('built-in: bash -c <command>')
    await unmountCard()
  })
})
