// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MCPServerCard } from './MCPServerCard'
import type { MCPServerStatus } from '@/types/models'

let container: HTMLDivElement
let root: Root

function setup(server: MCPServerStatus, expanded = false) {
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

  it('renders a destructive state with the error message', () => {
    setup({ name: 'broken', transport: 'http', connected: false, starting: false, tool_count: 0, tools: [], error: 'connection refused' }, true)
    expect(container.textContent).toContain('broken')
    expect(container.textContent).toContain('connection refused')
    teardown()
  })
})
