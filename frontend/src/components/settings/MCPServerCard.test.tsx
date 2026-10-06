// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MCPServerCard } from './MCPServerCard'
import type { MCPServerStatus } from '@/types/models'

let container: HTMLDivElement
let root: Root

function setup(server: MCPServerStatus, expanded = false, props: Partial<Parameters<typeof MCPServerCard>[0]> = {}) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(
      <MCPServerCard
        server={server}
        tools={[]}
        expanded={expanded}
        onToggleExpand={() => {}}
        onEdit={() => {}}
        onDelete={() => {}}
        mode={server.mode ?? 'auto'}
        isSaving={false}
        onModeChange={() => {}}
        {...props}
      />,
    )
  })
}

function teardown() {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
}

describe('MCPServerCard', () => {
  it('renders a neutral "Starting…" state for the gateway placeholder', () => {
    setup({ name: '_gateway', transport: '', connected: false, starting: true, tool_count: 0, tools: [] })
    expect(container.textContent).toContain('Starting…')
    // Friendly label instead of the raw sentinel name.
    expect(container.textContent).toContain('MCP Gateway')
    expect(container.textContent).not.toContain('_gateway')
    // Not editable/deletable while starting.
    expect(container.querySelectorAll('button')).toHaveLength(0)
    teardown()
  })

  it('renders a friendly error state for the failed gateway placeholder', () => {
    setup({ name: '_gateway', transport: '', connected: false, starting: false, tool_count: 0, tools: [], error: 'gateway startup failed: connection refused' })
    // Friendly label instead of the raw sentinel name.
    expect(container.textContent).toContain('MCP Gateway')
    expect(container.textContent).not.toContain('_gateway')
    // The error message is surfaced.
    expect(container.textContent).toContain('gateway startup failed: connection refused')
    // Not editable/deletable (no Edit/Delete buttons).
    expect(container.querySelectorAll('button')).toHaveLength(0)
    teardown()
  })

  it('renders a connected state with a green check icon', () => {
    setup({ name: 'context7', transport: 'http', connected: true, starting: false, tool_count: 2, tools: ['a', 'b'] })
    expect(container.textContent).toContain('context7')
    expect(container.textContent).toContain('2 tools')
    // svg icons rendered
    expect(container.querySelectorAll('svg').length).toBeGreaterThan(0)
    teardown()
  })

  it('renders a distinct unhealthy indicator for a reachable-but-degraded server', () => {
    setup({ name: 'flaky', transport: 'http', connected: true, unhealthy: true, starting: false, tool_count: 1, tools: [] })
    expect(container.textContent).toContain('flaky')
    // A distinct warning label (not the plain green connected state).
    expect(container.textContent).toContain('Unhealthy')
    teardown()
  })

  it('renders a healthy connected server without the unhealthy indicator', () => {
    setup({ name: 'context7', transport: 'http', connected: true, unhealthy: false, starting: false, tool_count: 2, tools: [] })
    expect(container.textContent).not.toContain('Unhealthy')
    teardown()
  })

  it('renders a destructive state with the error message', () => {
    setup({ name: 'broken', transport: 'http', connected: false, starting: false, tool_count: 0, tools: [], error: 'connection refused' }, true)
    expect(container.textContent).toContain('broken')
    expect(container.textContent).toContain('connection refused')
    teardown()
  })

  it('renders a manual mode badge next to the transport badge', () => {
    setup({ name: 'context7', transport: 'http', connected: false, starting: false, tool_count: 0, tools: [], error: 'unavailable', mode: 'manual' })
    expect(container.textContent).toContain('manual')
    teardown()
  })

  it('renders a disabled server neutrally: mode badge, no error, no red icon', () => {
    setup({ name: 'off-srv', transport: 'stdio', connected: false, starting: false, tool_count: 0, tools: [], mode: 'disabled' })
    expect(container.textContent).toContain('disabled')
    // Intentionally not dialed, never broken: no failure text anywhere.
    expect(container.textContent).not.toContain('unavailable')
    // The status icon is the neutral CircleOff, not the destructive alert.
    const alertIcons = container.querySelectorAll('.text-destructive')
    expect(alertIcons).toHaveLength(0)
    teardown()
  })

  it.each([
    ['auto', 'tools are always available'],
    ['manual', 'only after a /server-name or /mcp: server-name mention'],
    ['disabled', 'Never connects or starts a process'],
  ] as const)('shows %s and its explanation beside Edit/Delete', (mode, explanation) => {
    setup({ name: 'srv', transport: 'http', connected: false, starting: false, tool_count: 0, tools: [], mode }, true)
    const trigger = container.querySelector<HTMLButtonElement>('[aria-label="srv activation mode"]')!
    expect(trigger.textContent).toContain(mode)
    expect(trigger.classList.contains('w-max')).toBe(true)
    expect(trigger.classList.contains('shrink-0')).toBe(true)
    expect(trigger.className).not.toContain('min-w-')
    expect(Array.from(trigger.querySelectorAll('[aria-hidden="true"].invisible')).map((label) => label.textContent))
      .toEqual(['auto', 'manual', 'disabled'])
    expect(trigger.querySelector('span.grid')?.classList.contains('text-left')).toBe(true)
    expect(trigger.parentElement?.textContent).toContain('Edit')
    expect(trigger.parentElement?.textContent).toContain('Delete')
    expect(trigger.parentElement?.textContent).toContain(explanation)
    if (mode === 'manual') expect(trigger.parentElement?.textContent).toContain('Connects at startup')
    teardown()
  })

  it('disables the mode selector and edit/delete while saving', () => {
    const onModeChange = vi.fn()
    setup({ name: 'srv', transport: 'http', connected: true, starting: false, tool_count: 0, tools: [] }, true, { isSaving: true, onModeChange })
    const trigger = container.querySelector<HTMLButtonElement>('[aria-label="srv activation mode"]')!
    expect(trigger.disabled).toBe(true)
    act(() => { trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true })) })
    expect(document.querySelector('[role="menu"]')).toBeNull()
    expect(onModeChange).not.toHaveBeenCalled()
    expect(Array.from(trigger.parentElement!.querySelectorAll('button')).every((button) => button.disabled)).toBe(true)
    teardown()
  })

  it('renders no mode badge for the implicit auto default', () => {
    setup({ name: 'auto-srv', transport: 'http', connected: true, starting: false, tool_count: 1, tools: ['a'], mode: 'auto' })
    expect(container.textContent).not.toContain('manual')
    expect(container.textContent).not.toContain('disabled')
    teardown()
  })
})
