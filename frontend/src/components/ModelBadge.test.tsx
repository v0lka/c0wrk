// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ModelBadge } from './ModelBadge'

describe('ModelBadge (shared status-bar pill)', () => {
  let container: HTMLElement
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
    document.body.replaceChildren()
  })

  const render = (ui: React.ReactElement) =>
    act(() => {
      root.render(ui)
    })

  const badge = () => container.querySelector('[data-slot="badge"]')

  it('renders a bare model verbatim as both text and tooltip (status-bar input)', () => {
    // session_tokens.model arrives bare, so the status bar's usage renders
    // the pill exactly as the pre-extraction inline badge did.
    render(<ModelBadge model="qwen3-27b" />)
    const el = badge()
    expect(el).not.toBeNull()
    expect(el!.getAttribute('data-variant')).toBe('outline')
    expect(el!.getAttribute('title')).toBe('qwen3-27b')
    expect(el!.textContent).toBe('qwen3-27b')
  })

  it('strips the first provider segment for the text and keeps the full composite as the tooltip', () => {
    render(<ModelBadge model="openrouter/qwen3-27b" />)
    const el = badge()
    expect(el).not.toBeNull()
    expect(el!.getAttribute('title')).toBe('openrouter/qwen3-27b')
    expect(el!.textContent).toBe('qwen3-27b')
  })

  it('keeps the bare name intact beyond the first slash (HF-style ids)', () => {
    render(<ModelBadge model="ollama/org/path/model" />)
    const el = badge()
    expect(el).not.toBeNull()
    expect(el!.getAttribute('title')).toBe('ollama/org/path/model')
    expect(el!.textContent).toBe('org/path/model')
  })

  it('merges the hosting context className', () => {
    render(<ModelBadge model="qwen3-27b" className="max-w-full" />)
    expect(badge()!.className).toContain('max-w-full')
    // The shared base classes stay present alongside the caller's extras.
    expect(badge()!.className).toContain('truncate')
  })

  it('renders nothing for an empty model', () => {
    render(<ModelBadge model="" />)
    expect(badge()).toBeNull()
  })

  it('renders nothing for a value that strips to an empty bare name', () => {
    // Mis-shaped value the fail-closed launch funnel never emits — no pill.
    render(<ModelBadge model="provider/" />)
    expect(badge()).toBeNull()
  })
})
