// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom in this environment does not always expose `window.localStorage`,
// which zustand's `persist` middleware captures at store-creation time
// (via createJSONStorage(() => window.localStorage)). Polyfill it before any
// store module is imported so the real inputModeStore works in tests.
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  // Enable React's act() flushing in this jsdom environment.
  g.IS_REACT_ACT_ENVIRONMENT = true
  win.IS_REACT_ACT_ENVIRONMENT = true
  // jsdom in this environment does not expose `window.localStorage`, which
  // zustand's `persist` middleware captures at store-creation time (via
  // createJSONStorage(() => window.localStorage)). Install an in-memory
  // polyfill before any store module is imported so the real inputModeStore
  // works in tests. (Assign directly — reading it first would touch Node's
  // experimental global localStorage accessor and log a warning.)
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() { return map.size },
  }
})

import { ModelCombobox } from './ModelCombobox'
import { useInputModeStore } from '@/stores/inputModeStore'

// Mock the config hook so the combobox renders synchronously with canned
// models, without touching the Wails backend.
vi.mock('@/hooks/useConfigData', () => ({
  useConfigData: () => ({
    allModels: [
      { name: 'claude-sonnet', provider: 'anthropic', family: 'anthropic' },
      { name: 'gpt-4o', provider: 'chatgpt', family: 'chatgpt' },
    ],
    defaultModel: 'claude-sonnet',
    loaded: true,
  }),
  invalidateConfigCache: () => {},
}) as typeof import('@/hooks/useConfigData'))

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  useInputModeStore.setState({ selectedModel: null })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<ModelCombobox />)
  })
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

function openDropdown(): HTMLButtonElement {
  const trigger = container.querySelector('button')
  expect(trigger).not.toBeNull()
  act(() => {
    trigger!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  return trigger!
}

describe('ModelCombobox portal', () => {
  it('renders the dropdown into document.body (not the component subtree) when open', () => {
    // Closed initially: no listbox anywhere.
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()

    openDropdown()

    const listbox = document.body.querySelector('[role="listbox"]') as HTMLDivElement | null
    expect(listbox).not.toBeNull()
    // The menu lives in a portal under <body>, separate from the trigger's DOM.
    expect(container.contains(listbox)).toBe(false)
    expect(document.body.contains(listbox)).toBe(true)
  })

  it('positions the portaled menu fixed with a high z-index', () => {
    openDropdown()
    const listbox = document.body.querySelector('[role="listbox"]') as HTMLDivElement | null
    expect(listbox).not.toBeNull()
    expect(listbox!.style.position).toBe('fixed')
    // z-50 exceeds the message input area (auto), chat area (z-10/z-20) and
    // pending actions bar (auto).
    expect(listbox!.style.zIndex).toBe('50')
  })

  it('keeps the dropdown open when clicking inside the portaled menu', () => {
    openDropdown()
    const listbox = document.body.querySelector('[role="listbox"]') as HTMLDivElement | null
    expect(listbox).not.toBeNull()

    // A mousedown inside the portal menu must NOT be treated as an outside click.
    act(() => {
      listbox!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })
    expect(document.body.querySelector('[role="listbox"]')).not.toBeNull()
  })

  it('dismisses the dropdown on an outside click (outside trigger + portal)', () => {
    openDropdown()
    expect(document.body.querySelector('[role="listbox"]')).not.toBeNull()

    // A mousedown on <body> (outside both the trigger wrapper and the portal menu).
    act(() => {
      document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()
  })

  it('selects a model and closes when a portaled option is clicked', () => {
    openDropdown()
    const options = document.body.querySelectorAll('[role="listbox"] button')
    // Default option + two models = 3 buttons.
    expect(options.length).toBe(3)

    act(() => {
      // Last option button is the 'gpt-4o' model.
      options[options.length - 1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // The selected value is the composite selector "provider/name".
    expect(useInputModeStore.getState().selectedModel).toBe('chatgpt/gpt-4o')
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()
  })
})
