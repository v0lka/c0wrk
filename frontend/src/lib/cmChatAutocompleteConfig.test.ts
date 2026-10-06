// @vitest-environment node
import { describe, expect, it, vi } from 'vitest'

const { captured } = vi.hoisted(() => ({
  captured: { config: undefined as Record<string, unknown> | undefined },
}))

// Capture the autocompletion() config itself instead of asserting on source
// text: the upward placement is a CodeMirror option (aboveCursor), not CSS.
vi.mock('@codemirror/autocomplete', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@codemirror/autocomplete')>()
  return {
    ...actual,
    autocompletion: (config: unknown) => {
      captured.config = config as Record<string, unknown>
      return []
    },
  }
})

// createChatAutocomplete() registers the completion caches' invalidation
// subscriptions at construction; the Wails runtime is absent here, so the
// event subscription is stubbed the same way cmChatAutocomplete.test.ts does.
vi.mock('@/api/runtime', () => ({ subscribe: vi.fn(() => () => {}) }))
// The mention pane pulls React + the markdown pipeline — irrelevant here.
vi.mock('./cmMentionTooltip', () => ({ mentionTooltip: () => [] }))

import { createChatAutocomplete } from './cmChatAutocomplete'

describe('createChatAutocomplete config', () => {
  it('opens both lists upward from the trigger line', () => {
    createChatAutocomplete()
    expect(captured.config).toBeDefined()
    // aboveCursor — the supported knob: the completion popup is placed above
    // the trigger line, for BOTH the / and @ sources (one shared config).
    // The view still falls back below only when there is no room above.
    expect(captured.config!.aboveCursor).toBe(true)
  })

  it('keeps the row-kind classes the popup chrome CSS keys on', () => {
    createChatAutocomplete()
    const optionClass = captured.config!.optionClass as (completion: { type?: string }) => string
    expect(optionClass({ type: 'agent' })).toBe('agent-item')
    expect(optionClass({ type: 'skill' })).toBe('skill-item')
    expect(optionClass({ type: 'mcp' })).toBe('mcp-item')
    expect(optionClass({ type: 'file' })).toBe('file-item')
    expect(optionClass({ type: 'folder' })).toBe('file-item')
  })
})
