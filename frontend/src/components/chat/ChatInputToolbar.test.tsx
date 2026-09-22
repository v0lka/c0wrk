// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ChatInputToolbar } from './ChatInputToolbar'
import { GOAL_BLOCKED_BY_MODEL_PROFILES_REASON } from '@/lib/goalGate'
import type { ChatInputController } from '@/hooks/useChatInputController'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useChatStore } from '@/stores/chatStore'

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

// E2SToggle (rendered by the toolbar) consults the experimental gate through
// this hook, whose real implementation fetches the config via the Wails
// bindings — unavailable in jsdom, so every mount logged two backend errors.
// The gate is irrelevant to most lock behaviour but must be flippable for the
// E2S-toggle lock cases; pin it off by default (the same default the store
// latches in these tests).
const experimentalGate = vi.hoisted(() => ({ enabled: false }))
vi.mock('@/hooks/useExperimentalFeatures', () => ({
  useExperimentalFeatures: () => experimentalGate.enabled,
}))

// The toolbar consults the Model Profiles goal gate through this hook, whose real
// implementation also fetches the config via the Wails bindings (unavailable in
// jsdom). Expose a mutable flag so each test drives the blocked/unblocked state.
const modelProfilesGate = vi.hoisted(() => ({ blocked: false }))
vi.mock('@/hooks/useModelProfilesGate', () => ({
  useModelProfilesGate: () => modelProfilesGate.blocked,
}))

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
    attachmentsUploading: false,
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

function e2sTrigger(): HTMLButtonElement {
  const btn = container.querySelector('button[aria-label="Toggle E2S mode"]')
  expect(btn).not.toBeNull()
  return btn as HTMLButtonElement
}

beforeEach(() => {
  useInputModeStore.setState({ goalEnabled: false, e2sEnabled: false })
  experimentalGate.enabled = false
  // The toolbar reads the active session's live unfinished-task overlay (the
  // goal/E2S mode-toggle lock) straight from chatStore — reset it so tests
  // seed exactly the state they assert on.
  useChatStore.setState({ unfinishedTaskStatus: {} })
  modelProfilesGate.blocked = false
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

  it('unlocks model/reasoning when the task is cooperatively paused (resume honors overrides)', () => {
    renderToolbar({ taskActive: false, paused: true, showCancel: true })
    expect(modelTrigger().disabled).toBe(false)
    expect(reasoningTrigger().disabled).toBe(false)
  })

  it('keeps goal and E2S locked while the task is cooperatively paused (mode-armed sends abandon the task)', () => {
    experimentalGate.enabled = true
    renderToolbar({ taskActive: false, paused: true, showCancel: true })
    expect(goalTrigger().disabled).toBe(true)
    expect(goalTrigger().getAttribute('title')).toBe('Locked while the task is paused — resume or cancel it first')
    expect(e2sTrigger().disabled).toBe(true)
    expect(e2sTrigger().getAttribute('title')).toBe('Locked while the task is paused — resume or cancel it first')
    // Model/reasoning stay unlocked (resume honors those overrides).
    expect(modelTrigger().disabled).toBe(false)
    expect(reasoningTrigger().disabled).toBe(false)
  })

  it('keeps goal and E2S locked while a failed task awaits resume or cancel', () => {
    experimentalGate.enabled = true
    act(() => {
      useChatStore.setState({ unfinishedTaskStatus: { s1: 'failed' } })
    })
    renderToolbar({ activeSessionId: 's1' })
    expect(goalTrigger().disabled).toBe(true)
    expect(goalTrigger().getAttribute('title')).toBe('Locked while a failed task awaits resume or cancel')
    expect(e2sTrigger().disabled).toBe(true)
    expect(e2sTrigger().getAttribute('title')).toBe('Locked while a failed task awaits resume or cancel')
    // Model/reasoning still unlock — the failed task is resumable with fresh
    // model/reasoning overrides.
    expect(modelTrigger().disabled).toBe(false)
    expect(reasoningTrigger().disabled).toBe(false)
  })

  it('releases goal and E2S after the failed task is cancelled (overlay cleared)', () => {
    act(() => {
      useChatStore.setState({ unfinishedTaskStatus: { s1: '' } })
    })
    renderToolbar({ activeSessionId: 's1' })
    expect(goalTrigger().disabled).toBe(false)
    expect(goalTrigger().getAttribute('title')).toBe('Goal mode off — click to turn on')
  })

  it('keeps goal and E2S unlocked for a continuation after a settled task', () => {
    experimentalGate.enabled = true
    act(() => {
      useChatStore.setState({ unfinishedTaskStatus: { s1: '' } })
    })
    renderToolbar({ activeSessionId: 's1' })
    expect(goalTrigger().disabled).toBe(false)
    expect(e2sTrigger().disabled).toBe(false)
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

describe('ChatInputToolbar resume lock during attachment uploads', () => {
  const resumeButton = (): HTMLButtonElement =>
    container.querySelector<HTMLButtonElement>('button[aria-label="Resume task"]')!

  it('disables the Resume button while attachments are uploading (mirroring Send)', () => {
    renderToolbar({ paused: true, showCancel: true, attachmentsUploading: true })
    expect(resumeButton().disabled).toBe(true)
    expect(resumeButton().title).toContain('Processing attachments')
  })

  it('keeps Resume enabled when no uploads are in flight', () => {
    renderToolbar({ paused: true, showCancel: true })
    expect(resumeButton().disabled).toBe(false)
    expect(resumeButton().title).toBe('Resume task')
  })
})

describe('ChatInputToolbar goal block under the Model Profiles gate', () => {
  it('disables the goal toggle and shows the reason hint while blocked', () => {
    modelProfilesGate.blocked = true
    renderToolbar()
    expect(goalTrigger().disabled).toBe(true)
    expect(goalTrigger().getAttribute('title')).toBe(GOAL_BLOCKED_BY_MODEL_PROFILES_REASON)
    const hint = container.querySelector('[data-testid="goal-blocked-hint"]')
    expect(hint).not.toBeNull()
    expect(hint?.textContent).toBe(GOAL_BLOCKED_BY_MODEL_PROFILES_REASON)
  })

  it('auto-resets an armed goal toggle when the gate blocks', () => {
    act(() => {
      useInputModeStore.setState({ goalEnabled: true })
    })
    modelProfilesGate.blocked = true
    renderToolbar()
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
  })

  it('leaves the goal toggle enabled and hides the hint when not blocked', () => {
    renderToolbar()
    expect(goalTrigger().disabled).toBe(false)
    expect(container.querySelector('[data-testid="goal-blocked-hint"]')).toBeNull()
  })
})
