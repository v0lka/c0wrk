// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ModelBadge } from './ModelBadge'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'

describe('ModelBadge', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    useSessionStore.setState({ activeSessionId: 'sess-1' })
    useChatStore.setState({ sessionTokens: {} })
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.replaceChildren()
  })

  const render = (ui: React.ReactElement) =>
    act(() => {
      root.render(ui)
    })

  const badge = () => container.querySelector('[data-slot="badge"]')

  it('renders the outline badge with the bare text and the full composite as the tooltip', () => {
    render(<ModelBadge model="provider/qwen3-27b" sessionModel="bonsai-2-27b" />)
    const el = badge()
    expect(el).not.toBeNull()
    // Status-bar styling: outline variant (the shared status-bar pill).
    expect(el!.getAttribute('data-variant')).toBe('outline')
    // The visible text is the bare model name (the provider prefix is an
    // implementation detail); the tooltip carries the full composite, where
    // the provider is exactly what matters.
    expect(el!.getAttribute('title')).toBe('provider/qwen3-27b')
    expect(el!.textContent).toBe('qwen3-27b')
  })

  it('hides when the bare pin matches the session model (composite vs bare)', () => {
    render(<ModelBadge model="openrouter/qwen3-27b" sessionModel="qwen3-27b" />)
    expect(badge()).toBeNull()
  })

  it('hides on identical models', () => {
    render(<ModelBadge model="qwen3-27b" sessionModel="qwen3-27b" />)
    expect(badge()).toBeNull()
  })

  it('shows when the session model is empty (unknown conductor model)', () => {
    render(<ModelBadge model="qwen3-27b" sessionModel="" />)
    expect(badge()).not.toBeNull()
  })

  it('shows HF-style composite pins whose bare parts differ', () => {
    render(<ModelBadge model="ollama/org/path/model" sessionModel="org/other/model" />)
    const el = badge()
    expect(el).not.toBeNull()
    // Only the FIRST `/` is the separator — the bare text keeps the rest,
    // the tooltip keeps the full composite.
    expect(el!.getAttribute('title')).toBe('ollama/org/path/model')
    expect(el!.textContent).toBe('org/path/model')
  })

  it('renders nothing for a pin that strips to an empty bare name', () => {
    render(<ModelBadge model="provider/" sessionModel="bonsai-2-27b" />)
    // Mis-shaped value the fail-closed launch funnel never emits — no pill.
    expect(badge()).toBeNull()
  })

  it('reads the conductor model from the active session tokens when the prop is omitted', () => {
    useChatStore.setState({
      sessionTokens: {
        'sess-1': { total_input_tokens: 1, total_output_tokens: 1, model: 'qwen3-27b', family: 'qwen' },
      },
    })
    // Bare-equal to the session model → hidden without a sessionModel prop.
    render(<ModelBadge model="openrouter/qwen3-27b" />)
    expect(badge()).toBeNull()
  })

  it('shows when the session tokens carry a different model (prop omitted)', () => {
    useChatStore.setState({
      sessionTokens: {
        'sess-1': { total_input_tokens: 1, total_output_tokens: 1, model: 'bonsai-2-27b', family: 'bonsai' },
      },
    })
    render(<ModelBadge model="qwen3-27b" />)
    expect(badge()).not.toBeNull()
  })

  it('shows when there are no session tokens yet (prop omitted)', () => {
    render(<ModelBadge model="qwen3-27b" />)
    expect(badge()).not.toBeNull()
  })

  it('updates reactively when the session model catches up after mount', () => {
    render(<ModelBadge model="qwen3-27b" />)
    expect(badge()).not.toBeNull()
    // The conductor's session tokens arrive (e.g. session_tokens event) with
    // the same model — the badge disappears without re-rendering a parent.
    act(() => {
      useChatStore.setState({
        sessionTokens: {
          'sess-1': { total_input_tokens: 1, total_output_tokens: 1, model: 'provider/qwen3-27b', family: 'qwen' },
        },
      })
    })
    expect(badge()).toBeNull()
  })
})
