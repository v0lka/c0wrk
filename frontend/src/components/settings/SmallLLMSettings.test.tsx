// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Mock the API layer: the component must not touch real Wails bindings.
const updateSmallLLMConfigMock = vi.fn()
const getSmallLLMConfigMock = vi.fn()
vi.mock('@/api/config', () => ({
  getSmallLLMConfig: (...args: unknown[]) => getSmallLLMConfigMock(...args),
  updateSmallLLMConfig: (...args: unknown[]) => updateSmallLLMConfigMock(...args),
}))

// Failure-path tests (backend validation rejection) intentionally make
// SmallLLMSettings' logger.error fire; mock the logger so the expected
// errors don't pollute vitest output.
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { SmallLLMSettings } from './SmallLLMSettings'

const baseConfig = {
  enabled: true,
  essential_tools: {
    enabled: true,
    always_present: ['finish', 'read_file'],
    max_tools: 16,
    compact_descriptions: false,
    protected_tools: ['ask_user', 'finish'],
  },
  system_prompt: { lite: false, few_shot: false, reasoning_scaffold: false },
  sampling: {
    enabled: true,
    temperature: 0,
    top_p: 0,
    top_k: 0,
    repetition_penalty: 0,
    presence_penalty: 0,
    reasoning_effort: '',
  },
  loop_hardening: {
    enabled: false,
    repeat_nudge_threshold: 3,
    parse_error_abort_threshold: 3,
    fruitless_nudge_threshold: 3,
    fruitless_abort_threshold: 4,
    same_tool_repeat_nudge_threshold: 5,
  },
  context: {
    enabled: false,
    compaction: { keep_last: 6, block_size: 5, trigger_percent: 80 },
    tool_output_keep_last_n: 2,
    output_token_reserve: 8192,
  },
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  updateSmallLLMConfigMock.mockReset()
  getSmallLLMConfigMock.mockReset().mockResolvedValue(structuredClone(baseConfig))
  updateSmallLLMConfigMock.mockResolvedValue(undefined)
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

const render = () =>
  act(async () => {
    await root.render(<SmallLLMSettings />)
  })

const field = (label: string) => container.querySelector(`input[data-field="${label}"]`) as HTMLInputElement | null

const setField = (input: HTMLInputElement, value: string) =>
  act(async () => {
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set
    setter?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })

// React 17+ delegates onBlur via the native `focusout` event.
const blur = (input: HTMLInputElement) =>
  act(async () => {
    input.dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
  })

/** Find the checkbox that belongs to the Toggle row containing `labelText`. */
const toggleFor = (labelText: string) => {
  const span = Array.from(container.querySelectorAll('span')).find((s) => s.textContent === labelText)
  return span?.closest('div')?.querySelector('input[type="checkbox"]') as HTMLInputElement | null
}

/** Find the tag-list chip rendering the given tool name. */
const chipFor = (name: string) =>
  Array.from(container.querySelectorAll('code')).find((c) => c.textContent === name)?.parentElement ?? null

describe('SmallLLMSettings — sampling inherit semantics', () => {
  it('renders inherit (0) fields as empty inputs with vendor-default placeholder', async () => {
    await render()
    const temp = field('Temperature')
    expect(temp).not.toBeNull()
    expect(temp?.value).toBe('')
    expect(temp?.placeholder).toBe('vendor default')
    expect(container.textContent).toContain('Empty fields inherit the vendor preset')
  })

  it('shows explicit values and sends them through on commit', async () => {
    getSmallLLMConfigMock.mockResolvedValue(
      structuredClone({ ...baseConfig, sampling: { ...baseConfig.sampling, temperature: 0.7, top_k: 40 } }),
    )
    await render()
    expect(field('Temperature')?.value).toBe('0.7')
    expect(field('Top K')?.value).toBe('40')

    await setField(field('Top K')!, '20')
    await blur(field('Top K')!)
    const sent = updateSmallLLMConfigMock.mock.calls[updateSmallLLMConfigMock.mock.calls.length - 1]?.[0]
    expect(sent.sampling.top_k).toBe(20)
  })

  it('clearing an explicit value commits 0 (inherit)', async () => {
    getSmallLLMConfigMock.mockResolvedValue(
      structuredClone({ ...baseConfig, sampling: { ...baseConfig.sampling, temperature: 0.9 } }),
    )
    await render()
    const temp = field('Temperature')!
    expect(temp.value).toBe('0.9')
    await setField(temp, '')
    await blur(temp)
    const sent = updateSmallLLMConfigMock.mock.calls[updateSmallLLMConfigMock.mock.calls.length - 1]?.[0]
    expect(sent.sampling.temperature).toBe(0)
  })

  it('rejects out-of-bounds input and keeps the previous value', async () => {
    getSmallLLMConfigMock.mockResolvedValue(
      structuredClone({ ...baseConfig, sampling: { ...baseConfig.sampling, repetition_penalty: 1.2 } }),
    )
    await render()
    const rep = field('Repetition penalty')!
    await setField(rep, '9')
    await blur(rep)
    expect(rep.value).toBe('1.2')
    expect(updateSmallLLMConfigMock).not.toHaveBeenCalled()
  })

  it('presence penalty: inherit renders empty, explicit value commits, >2 rejected', async () => {
    // Unset (0) renders as an empty input with the inherit placeholder.
    await render()
    const pp = field('Presence penalty')!
    expect(pp).not.toBeNull()
    expect(pp.value).toBe('')
    expect(pp.placeholder).toBe('vendor default')

    // An explicit value (the Qwen instruct default 1.5) is sent through on commit.
    await setField(pp, '1.5')
    await blur(pp)
    const sent = updateSmallLLMConfigMock.mock.calls[updateSmallLLMConfigMock.mock.calls.length - 1]?.[0]
    expect(sent.sampling.presence_penalty).toBe(1.5)

    // Out-of-bounds input (> 2) is rejected and keeps the previous value.
    const before = updateSmallLLMConfigMock.mock.calls.length
    await setField(pp, '2.5')
    await blur(pp)
    expect(pp.value).toBe('1.5')
    expect(updateSmallLLMConfigMock.mock.calls.length).toBe(before)
  })
})

describe('SmallLLMSettings — context section', () => {
  it('renders the context variant with compaction, pruning and reserve fields', async () => {
    await render()
    const text = container.textContent ?? ''
    expect(text).toContain('Context Management')
    expect(text).toContain('Aggressive context management')

    // Fields are hidden until the variant toggle is on.
    expect(field('Keep last')).toBeNull()
  })

  it('exposes compaction fields once the variant is enabled', async () => {
    getSmallLLMConfigMock.mockResolvedValue(
      structuredClone({ ...baseConfig, context: { ...baseConfig.context, enabled: true } }),
    )
    await render()
    expect(field('Keep last')?.value).toBe('6')
    expect(field('Block size')?.value).toBe('5')
    expect(field('Trigger percent')?.value).toBe('80')
    expect(field('Tool output keep N')?.value).toBe('2')
    expect(field('Output token reserve')?.value).toBe('8192')

    await setField(field('Keep last')!, '4')
    await blur(field('Keep last')!)
    const sent = updateSmallLLMConfigMock.mock.calls[updateSmallLLMConfigMock.mock.calls.length - 1]?.[0]
    expect(sent.context.compaction.keep_last).toBe(4)
  })
})

describe('SmallLLMSettings — essential tools budget & locked tools', () => {
  it('documents the budget semantics and renders protected tools as locked', async () => {
    await render()
    const text = container.textContent ?? ''
    expect(text).toContain('router-matched')
    expect(text).toContain('never trimmed')

    // Protected tools render as locked chips: no remove button, lock glyph.
    const lockedChip = chipFor('finish')
    expect(lockedChip).not.toBeNull()
    expect(lockedChip?.querySelector('[aria-label="Remove finish"]')).toBeNull()
    expect(lockedChip?.querySelector('svg')).not.toBeNull()

    // Non-protected tools keep their remove button.
    const openChip = chipFor('read_file')
    expect(openChip?.querySelector('[aria-label="Remove read_file"]')).not.toBeNull()
  })

  it('toggles compact descriptions', async () => {
    await render()
    const toggle = toggleFor('Compact tool descriptions')
    expect(toggle).not.toBeNull()
    expect(toggle?.checked).toBe(false)
    await act(async () => {
      toggle?.click()
      await Promise.resolve()
    })
    const sent = updateSmallLLMConfigMock.mock.calls[updateSmallLLMConfigMock.mock.calls.length - 1]?.[0]
    expect(sent.essential_tools.compact_descriptions).toBe(true)
  })
})

describe('SmallLLMSettings — inline validation errors', () => {
  it('shows the backend validation error next to the form and reverts local state', async () => {
    await render()
    updateSmallLLMConfigMock.mockRejectedValue(new Error('keep_last must be >= 2'))

    await setField(field('Max tools')!, '3')
    await blur(field('Max tools')!)
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })

    const text = container.textContent ?? ''
    expect(text).toContain('keep_last must be >= 2')
    // Config was reloaded from the backend (reverted).
    expect(getSmallLLMConfigMock).toHaveBeenCalledTimes(2)
    expect(field('Max tools')?.value).toBe('16')
  })
})
