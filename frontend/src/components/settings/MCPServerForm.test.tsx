// @vitest-environment jsdom
//
// The per-server timeout fields (MCP `timeout` / `call_timeout`, Go duration
// strings). Two properties matter:
//
//  - Both inputs are always present (they apply to both transports) and
//    round-trip through the saved MCPServerConfig.
//  - Editing an existing server prefills them from the stored config.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MCPServerForm } from './MCPServerForm'
import type { MCPServerConfig } from '@/types/models'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

interface RenderOpts {
  editingName?: string | null
  serverConfigs?: Record<string, MCPServerConfig>
  editServer?: { name: string; transport: string }
  onSave?: (config: Record<string, MCPServerConfig>, editName: string | null) => Promise<string | null>
}

/** Radix portals dialog content to document.body, so queries target document. */
function render({ editingName = null, serverConfigs = {}, editServer, onSave = async () => null }: RenderOpts = {}) {
  act(() => {
    root.render(
      <MCPServerForm
        open
        onOpenChange={() => {}}
        editingName={editingName}
        serverConfigs={serverConfigs}
        editServer={editServer}
        isSaving={false}
        onSave={onSave}
      />,
    )
  })
}

function timeoutInputs(): HTMLInputElement[] {
  return Array.from(document.querySelectorAll<HTMLInputElement>('input[placeholder="60s"]'))
}

function nameInput(): HTMLInputElement | null {
  return document.querySelector<HTMLInputElement>('input[placeholder="my-mcp-server"]')
}

function saveButton(): HTMLButtonElement | null {
  const buttons = Array.from(document.querySelectorAll('button'))
  return (buttons.find((b) => b.textContent?.trim() === 'Save') as HTMLButtonElement) ?? null
}

function typeInto(input: HTMLInputElement, value: string) {
  const nativeInputSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
  if (!nativeInputSetter) throw new Error('native input setter not found')
  act(() => {
    nativeInputSetter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('MCPServerForm timeout fields', () => {
  it('renders the connect and call timeout inputs when adding a server', () => {
    render()
    const inputs = timeoutInputs()
    expect(inputs).toHaveLength(2)
    expect(inputs[0]!.value).toBe('')
    expect(inputs[1]!.value).toBe('')
    expect(document.body.textContent).toContain('Connect Timeout')
    expect(document.body.textContent).toContain('Call Timeout')
  })

  it('prefills both timeouts from the stored config when editing', () => {
    const cfg: MCPServerConfig = {
      transport: 'http',
      command: '',
      args: [],
      env: {},
      url: 'http://localhost:8080/mcp',
      headers: {},
      timeout: '30s',
      call_timeout: '2m',
    }
    render({ editingName: 'srv', serverConfigs: { srv: cfg }, editServer: { name: 'srv', transport: 'http' } })
    const inputs = timeoutInputs()
    expect(inputs[0]!.value).toBe('30s')
    expect(inputs[1]!.value).toBe('2m')
  })

  it('persists both timeouts into the saved config', async () => {
    const onSave = vi.fn<(config: Record<string, MCPServerConfig>, editName: string | null) => Promise<string | null>>(async () => null)
    render({ onSave })

    typeInto(nameInput()!, 'my-server')
    const inputs = timeoutInputs()
    typeInto(inputs[0]!, '45s')
    typeInto(inputs[1]!, '90s')

    await act(async () => {
      saveButton()!.click()
    })

    expect(onSave).toHaveBeenCalledTimes(1)
    const saved = onSave.mock.calls[0]![0] as Record<string, MCPServerConfig>
    expect(saved['my-server']!.timeout).toBe('45s')
    expect(saved['my-server']!.call_timeout).toBe('90s')
  })

  it('trims whitespace and omits empty timeouts as empty strings', async () => {
    const onSave = vi.fn<(config: Record<string, MCPServerConfig>, editName: string | null) => Promise<string | null>>(async () => null)
    render({ onSave })

    typeInto(nameInput()!, 'my-server')
    typeInto(timeoutInputs()[0]!, '  30s  ')

    await act(async () => {
      saveButton()!.click()
    })

    const saved = onSave.mock.calls[0]![0] as Record<string, MCPServerConfig>
    expect(saved['my-server']!.timeout).toBe('30s')
    expect(saved['my-server']!.call_timeout).toBe('')
  })
})

// Activation is managed on the card; editing transport details must preserve it.
describe('MCPServerForm activation mode preservation', () => {
  it('has no activation controls and creates new servers in auto mode', async () => {
    const onSave = vi.fn(async () => null)
    render({ onSave })
    expect(document.body.textContent).not.toContain('Activation Mode')
    expect(document.querySelector('[aria-label*="activation"]')).toBeNull()
    typeInto(nameInput()!, 'new-server')
    await act(async () => { saveButton()!.click() })
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      'new-server': expect.objectContaining({ mode: 'auto' }),
    }), null)
  })

  it.each(['auto', 'manual', 'disabled', undefined] as const)('preserves stored mode %s when saving an existing server', async (mode) => {
    const cfg: MCPServerConfig = {
      transport: 'stdio', command: 'cmd', args: [], env: {}, url: '', headers: {},
      timeout: '30s', call_timeout: '2m', mode,
    }
    const onSave = vi.fn(async () => null)
    render({ editingName: 'srv', serverConfigs: { srv: cfg }, editServer: { name: 'srv', transport: 'stdio' }, onSave })
    expect(document.body.textContent).not.toContain('Activation Mode')
    typeInto(timeoutInputs()[0]!, '45s')
    await act(async () => { saveButton()!.click() })
    expect(onSave).toHaveBeenCalledWith({ srv: { ...cfg, timeout: '45s', mode: mode ?? 'auto' } }, 'srv')
  })
})
