// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ChatInputToolbar } from './ChatInputToolbar'
import type { ChatInputController } from '@/hooks/useChatInputController'
import { useInputModeStore } from '@/stores/inputModeStore'

// Mock the config hook so the comboboxes render synchronously with a
// reasoning-capable model, without touching the Wails backend.
vi.mock('@/hooks/useConfigData', () => ({
  useConfigData: () => ({
    allModels: [
      {
        name: 'claude-sonnet',
        provider: 'anthropic',
        family: 'anthropic',
        vision: true,
        reasoning: { default: 'high', options: ['low', 'medium', 'high'] },
      },
    ],
    defaultModel: 'claude-sonnet',
    loaded: true,
  }),
  invalidateConfigCache: vi.fn(),
}) as unknown as typeof import('@/hooks/useConfigData'))

// The toolbar wires the attach action through this hook; keep it inert.
vi.mock('@/hooks/useAttachmentsInput', () => ({
  useAttachmentsInput: () => ({ handleAttach: vi.fn() }),
}) as unknown as typeof import('@/hooks/useAttachmentsInput'))

function makeController(overrides: Partial<ChatInputController>): ChatInputController {
  return {
    editor: {} as ChatInputController['editor'],
    hasContent: false,
    isOptimizing: false,
    optimizeError: null,
    sendError: null,
    showCancel: false,
    isInputDisabled: false,
    isNoProject: false,
    taskActive: false,
    paused: false,
    pausing: false,
    compacting: false,
    mode: 'chat',
    setMode: vi.fn(),
    height: 200,
    setHeight: vi.fn(),
    isExpanded: false,
    toggleExpanded: vi.fn(),
    activeSessionId: null,
    handleSend: vi.fn(),
    handleOptimize: vi.fn(),
    handlePause: vi.fn(),
    handleResume: vi.fn(),
    cancel: vi.fn(),
    ...overrides,
  }
}

let container: HTMLDivElement
let root: Root

function renderToolbar(overrides: Partial<ChatInputController> = {}) {
  act(() => {
    root.render(<ChatInputToolbar controller={makeController(overrides)} />)
  })
}

/** The model trigger shows the resolved default model label. */
function modelTrigger(): HTMLButtonElement {
  const btn = Array.from(container.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('Default: claude-sonnet'),
  )
  expect(btn).toBeDefined()
  return btn as HTMLButtonElement
}

/** The reasoning trigger carries a "Reasoning: …" title when unlocked. */
function reasoningTrigger(): HTMLButtonElement {
  const btn = Array.from(container.querySelectorAll('button')).find((b) =>
    (b.getAttribute('title') ?? '').startsWith('Reasoning:'),
  )
  if (!btn) {
    // Locked state replaces the title; fall back to the lock hint.
    const locked = Array.from(container.querySelectorAll('button')).find((b) =>
      b.getAttribute('title') === 'Locked while the session is running',
    )
    expect(locked).toBeDefined()
    return locked as HTMLButtonElement
  }
  return btn
}

function goalTrigger(): HTMLButtonElement {
  const btn = container.querySelector('button[aria-label="Toggle goal mode"]')
  expect(btn).not.toBeNull()
  return btn as HTMLButtonElement
}

beforeEach(() => {
  useInputModeStore.setState({ goalEnabled: false })
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

describe('ChatInputToolbar selector lock', () => {
  it('locks model, reasoning, and goal selectors while the task is running', () => {
    renderToolbar({ taskActive: true })
    expect(modelTrigger().disabled).toBe(true)
    expect(reasoningTrigger().disabled).toBe(true)
    expect(goalTrigger().disabled).toBe(true)
  })

  it('locks the selectors while a cooperative pause is in flight (pausing)', () => {
    renderToolbar({ taskActive: true, pausing: true })
    expect(goalTrigger().disabled).toBe(true)
    expect(modelTrigger().disabled).toBe(true)
  })

  it('locks the selectors while compacting', () => {
    renderToolbar({ compacting: true })
    expect(goalTrigger().disabled).toBe(true)
    expect(modelTrigger().disabled).toBe(true)
  })

  it('unlocks the selectors when the task is cooperatively paused (resume honors overrides)', () => {
    renderToolbar({ taskActive: false, paused: true, showCancel: true })
    expect(modelTrigger().disabled).toBe(false)
    expect(reasoningTrigger().disabled).toBe(false)
    expect(goalTrigger().disabled).toBe(false)
  })

  it('unlocks the selectors when the session is idle (finished/failed)', () => {
    renderToolbar()
    expect(modelTrigger().disabled).toBe(false)
    expect(reasoningTrigger().disabled).toBe(false)
    expect(goalTrigger().disabled).toBe(false)
  })

  it('locks the budget selector with the goal toggle when goal mode is armed mid-run', () => {
    act(() => {
      useInputModeStore.setState({ goalEnabled: true })
    })
    renderToolbar({ taskActive: true })
    const budget = container.querySelector('button[aria-label="Select goal budget"]')
    expect(budget).not.toBeNull()
    expect((budget as HTMLButtonElement).disabled).toBe(true)
    expect(goalTrigger().disabled).toBe(true)
  })
})
