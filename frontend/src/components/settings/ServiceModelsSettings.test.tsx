// @vitest-environment jsdom
//
// ServiceModelsSettings — the collapsed service-model override shell (ADR-131).
//
// ModelPickerMenu is mocked at the component boundary: these tests pin OUR
// logic — the collapsed-by-default shell, the fixed ten-row catalog in the
// backend's canonical key order, and the value / onSelect wiring into the
// useLLMConfig draft — not the Radix menu internals (covered by
// ModelPickerMenu's own tests).

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/ui/ModelPickerMenu', () => ({
  ModelPickerMenu: ({
    ariaLabel,
    value,
    onSelect,
    unsetLabel,
  }: {
    ariaLabel: string
    value: string | null
    onSelect: (id: string | null) => void
    unsetLabel?: string
  }) => (
    <button
      type="button"
      data-testid={`picker-${ariaLabel}`}
      data-unset-label={unsetLabel ?? ''}
      onClick={() => onSelect(value === null ? 'lmstudio/glm-5.3' : null)}
    >
      {value === null ? (unsetLabel ?? 'default') : value}
    </button>
  ),
}))

import { ServiceModelsSettings } from './ServiceModelsSettings'

let container: HTMLDivElement
let root: Root

const MODELS = [
  { name: 'glm-5.3', provider: 'lmstudio' },
  { name: 'claude-sonnet', provider: 'anthropic' },
]

const ALL_KEYS = [
  'judge',
  'router',
  'reflector',
  'title',
  'commit_message',
  'optimize_extract',
  'optimize_rewrite',
  'compaction_summarize',
  'goal_suggest',
  'prompt_suggest',
] as const

function renderBlock(
  value: Record<string, string>,
  onChange: (key: string, model: string | null) => void,
): void {
  act(() => {
    root.render(
      <ServiceModelsSettings
        models={MODELS}
        defaultModel="anthropic/claude-sonnet"
        value={value}
        onChange={onChange}
      />,
    )
  })
}

function toggle(): void {
  const btn = container.querySelector<HTMLButtonElement>('[data-testid="service-models-toggle"]')
  if (!btn) throw new Error('service-models toggle not found')
  act(() => {
    btn.click()
  })
}

describe('ServiceModelsSettings', () => {
  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
  })

  it('renders collapsed by default and expands to the fixed ten-row catalog', () => {
    renderBlock({}, () => {})

    expect(container.querySelector('[data-testid="service-models-settings"]')).not.toBeNull()
    // Collapsed: the toggle is present, the rows are not.
    expect(container.querySelector('[data-testid="service-model-row-judge"]')).toBeNull()

    toggle()

    for (const key of ALL_KEYS) {
      expect(container.querySelector(`[data-testid="service-model-row-${key}"]`)).not.toBeNull()
    }
    // Exactly the ten fixed keys — no extras.
    expect(container.querySelectorAll('[data-testid^="service-model-row-"]').length).toBe(10)
  })

  it('wires each row: an unpinned picker sets the pin, a pinned picker clears it', () => {
    const onChange = vi.fn()
    renderBlock({ judge: 'anthropic/claude-sonnet' }, onChange)
    toggle()

    // judge starts pinned → the mocked picker's click routes to onSelect(null),
    // i.e. clearing back to the session default.
    const judge = container.querySelector<HTMLButtonElement>('[data-testid="picker-Tool-call judge model"]')
    expect(judge).not.toBeNull()
    act(() => {
      judge!.click()
    })
    expect(onChange).toHaveBeenCalledWith('judge', null)

    // title starts unpinned → the click routes to onSelect(composite).
    const title = container.querySelector<HTMLButtonElement>('[data-testid="picker-Session titles model"]')
    expect(title).not.toBeNull()
    act(() => {
      title!.click()
    })
    expect(onChange).toHaveBeenCalledWith('title', 'lmstudio/glm-5.3')

    // No other row fired.
    expect(onChange).toHaveBeenCalledTimes(2)
  })

  it('labels unset rows by their fallback class: session-model vs global-default consumers', () => {
    renderBlock({ judge: 'lmstudio/glm-5.3' }, () => {})
    toggle()

    // Session-class consumer (runs inside a session): "same as session".
    const judge = container.querySelector<HTMLButtonElement>('[data-testid="picker-Tool-call judge model"]')
    expect(judge!.textContent).toBe('lmstudio/glm-5.3')
    expect(judge!.dataset.unsetLabel).toBe('Not set (same as session)')

    // Default-class consumers (run outside any session on the app-level
    // builder): "same as default". The pinned judge row carries its class's
    // wording too — the unset label rides every row via unsetLabel, never the
    // misleading "Default: <model>" global-default label.
    const title = container.querySelector<HTMLButtonElement>('[data-testid="picker-Session titles model"]')
    expect(title!.textContent).toBe('Not set (same as default)')
    expect(title!.dataset.unsetLabel).toBe('Not set (same as default)')
    const commit = container.querySelector<HTMLButtonElement>('[data-testid="picker-Commit messages model"]')
    expect(commit!.textContent).toBe('Not set (same as default)')
    expect(commit!.dataset.unsetLabel).toBe('Not set (same as default)')

    // The remaining session-class rows keep the session wording.
    const compaction = container.querySelector<HTMLButtonElement>('[data-testid="picker-Compaction summarizer model"]')
    expect(compaction!.dataset.unsetLabel).toBe('Not set (same as session)')
    // …and the remaining default-class rows keep the default wording.
    const suggest = container.querySelector<HTMLButtonElement>('[data-testid="picker-Follow-up suggestions model"]')
    expect(suggest!.dataset.unsetLabel).toBe('Not set (same as default)')
  })
})
