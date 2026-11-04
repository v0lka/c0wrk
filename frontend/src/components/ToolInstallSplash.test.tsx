// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ToolInstallSplash } from './ToolInstallSplash'
import type { ToolManagerProgressData, ToolManagerToolInfo } from '@/types/events'

// The splash is a pure view over (tools, progressMap, toolsTotal): these tests
// pin the four row states, the ready-counter semantics (only a terminal `done`
// counts) and the denominator fallback for a lost tool_manager:start.

const tools: readonly ToolManagerToolInfo[] = [
  { name: 'uv', version: '0.9.0' },
  { name: 'rg', version: '15.2.0' },
  { name: 'markitdown', version: '0.1.9' },
]

function prog(tool: string, overrides: Partial<ToolManagerProgressData>): ToolManagerProgressData {
  return { tool, stage: 'download', bytes_done: 0, bytes_total: 0, ...overrides }
}

let root: Root
let container: HTMLDivElement

function renderSplash(props: {
  tools?: readonly ToolManagerToolInfo[]
  progressMap?: Map<string, ToolManagerProgressData>
  toolsTotal?: number
}) {
  const {
    tools: list = tools,
    progressMap = new Map(),
    toolsTotal = 0,
  } = props
  act(() => {
    root.render(<ToolInstallSplash tools={list} progressMap={progressMap} toolsTotal={toolsTotal} />)
  })
}

function rowText(name: string): string {
  const row = document.querySelector(`[data-testid="tool-row-${name}"]`)
  if (!row) throw new Error(`row "${name}" not found`)
  return row.textContent ?? ''
}

function bodyText(): string {
  return document.body.textContent ?? ''
}

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

describe('ToolInstallSplash', () => {
  it('shows pending rows and 0 of N before any progress event', () => {
    renderSplash({})

    expect(rowText('uv')).toContain('Pending')
    expect(bodyText()).toContain('0 of 3 tools ready')
    expect(bodyText()).toContain('Installing Tools')
  })

  it('a started row is not ready: extract with no bytes is an active indeterminate bar, never "Starting…"', () => {
    renderSplash({
      progressMap: new Map([
        ['uv', prog('uv', { stage: 'extract' })],
        ['rg', prog('rg', { stage: 'download', bytes_done: 50, bytes_total: 100 })],
      ]),
    })

    expect(rowText('uv')).toContain('Extracting')
    expect(rowText('rg')).toContain('Downloading')
    expect(rowText('rg')).toContain('50%')
    expect(bodyText()).not.toContain('Starting')
    expect(bodyText()).toContain('0 of 3 tools ready')
  })

  it('only a terminal done counts as ready and shows the success verdict', () => {
    renderSplash({
      progressMap: new Map([
        ['uv', prog('uv', { stage: 'done' })],
        ['rg', prog('rg', { stage: 'verify' })],
      ]),
    })

    expect(rowText('uv')).toContain('Done')
    expect(bodyText()).toContain('1 of 3 tools ready')
    expect(bodyText()).toContain('Installing Tools')
  })

  it('a failed row shows the failure verdict and the cause, and never counts as ready', () => {
    renderSplash({
      progressMap: new Map([
        ['uv', prog('uv', { stage: 'failed', error: 'checksum verification failed after download' })],
        ['rg', prog('rg', { stage: 'done' })],
      ]),
    })

    expect(rowText('uv')).toContain('Failed')
    expect(rowText('uv')).toContain('checksum verification failed after download')
    expect(rowText('rg')).toContain('Done')
    expect(bodyText()).toContain('1 of 3 tools ready')
  })

  it('the backend-stamped tools_total wins over the derived list length', () => {
    // tool_manager:start was lost — the list is derived from progress events
    // and still only knows one tool, while the events carry the real total.
    renderSplash({
      tools: [{ name: 'uv', version: '' }],
      progressMap: new Map([['uv', prog('uv', { stage: 'done' })]]),
      toolsTotal: 3,
    })

    expect(bodyText()).toContain('1 of 3 tools ready')
  })

  it('the python bootstrap announces its sub-step', () => {
    renderSplash({
      progressMap: new Map([
        ['markitdown', prog('markitdown', { stage: 'python_bootstrap', detail: 'packages' })],
      ]),
    })

    expect(rowText('markitdown')).toContain('Bootstrapping Python')
    expect(rowText('markitdown')).toContain('Installing packages')
  })

  it('all tools done flips the header to Tools Ready and drops the counter', () => {
    renderSplash({
      progressMap: new Map([
        ['uv', prog('uv', { stage: 'done' })],
        ['rg', prog('rg', { stage: 'done' })],
        ['markitdown', prog('markitdown', { stage: 'done' })],
      ]),
    })

    expect(bodyText()).toContain('Tools Ready')
    expect(bodyText()).toContain('All required tools are installed')
    expect(bodyText()).not.toContain('tools ready')
  })
})
