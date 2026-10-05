// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MCPSettings } from './MCPSettings'
import { getMCPServers, getMCPStatus, getToolList, updateMCPServers } from '@/api/mcp'
import type { MCPServerConfig } from '@/types/models'

vi.mock('@/api/mcp', () => ({
  getMCPServers: vi.fn(), getMCPStatus: vi.fn(), getToolList: vi.fn(), updateMCPServers: vi.fn(),
}))
vi.mock('@/api/runtime', () => ({ subscribe: vi.fn(() => () => {}) }))

const config: MCPServerConfig = {
  transport: 'http', command: 'preserved', args: ['arg'], env: { KEY: '${ENV_VALUE}' },
  url: 'http://localhost:8080/mcp', headers: { Header: 'value' }, timeout: '30s', call_timeout: '2m', mode: 'auto',
}
let configs: Record<string, MCPServerConfig>
let container: HTMLDivElement
let root: Root

beforeEach(async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  configs = { srv: { ...config }, other: { ...config, mode: 'disabled' } }
  vi.mocked(getMCPServers).mockImplementation(async () => configs)
  vi.mocked(getMCPStatus).mockImplementation(async () => Object.entries(configs).map(([name, cfg]) => ({
    name, transport: cfg.transport, mode: cfg.mode, connected: true, starting: false, tool_count: 0, tools: [],
  })))
  vi.mocked(getToolList).mockResolvedValue([])
  vi.mocked(updateMCPServers).mockImplementation(async (next) => { configs = next })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => { root.render(<MCPSettings />) })
  act(() => { container.querySelector<HTMLElement>('[data-slot="collapsible-trigger"]')!.click() })
})

afterEach(() => {
  act(() => { root.unmount() })
  container.remove()
  document.body.innerHTML = ''
  vi.resetAllMocks()
  vi.unstubAllGlobals()
})

function trigger(): HTMLButtonElement {
  return container.querySelector<HTMLButtonElement>('[aria-label="srv activation mode"]')!
}

async function chooseMode(mode: string) {
  await act(async () => { trigger().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true })) })
  const item = Array.from(document.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    .find((el) => el.textContent === mode)
  expect(item).toBeDefined()
  await act(async () => { item!.click() })
}

describe('MCPSettings card activation mode', () => {
  it.each(['manual', 'disabled'] as const)('persists %s while preserving the full config map and refreshes status', async (mode) => {
    const original = configs
    await chooseMode(mode)
    expect(updateMCPServers).toHaveBeenCalledExactlyOnceWith({ ...original, srv: { ...config, mode } })
    expect(original.srv!.mode).toBe('auto')
    expect(getMCPServers).toHaveBeenCalledTimes(2)
    expect(trigger().textContent).toContain(mode)
  })

  it('does not save when reselecting the current mode', async () => {
    await chooseMode('auto')
    expect(updateMCPServers).not.toHaveBeenCalled()
  })

  it('disables mutation controls during a pending save and recovers after completion', async () => {
    let complete!: () => void
    vi.mocked(updateMCPServers).mockImplementation((next) => new Promise<void>((resolve) => {
      complete = () => { configs = next; resolve() }
    }))
    await chooseMode('manual')
    expect(trigger().disabled).toBe(true)
    for (const label of ['Edit', 'Delete', 'Add Server']) {
      const button = Array.from(container.querySelectorAll('button')).find((el) => el.textContent?.trim() === label)!
      expect(button.disabled).toBe(true)
    }
    expect(document.querySelector('[role="menu"]')).toBeNull()
    await act(async () => { complete() })
    expect(trigger().disabled).toBe(false)
    expect(trigger().textContent).toContain('manual')
    expect(updateMCPServers).toHaveBeenCalledTimes(1)
  })

  it('shows a failed update and retains the previous mode, allowing retry', async () => {
    vi.mocked(updateMCPServers).mockRejectedValueOnce(new Error('Unable to save mode'))
    await chooseMode('manual')
    expect(container.textContent).toContain('Unable to save mode')
    expect(trigger().textContent).toContain('auto')
    expect(trigger().disabled).toBe(false)
    await chooseMode('manual')
    expect(container.textContent).not.toContain('Unable to save mode')
    expect(trigger().textContent).toContain('manual')
  })
})
