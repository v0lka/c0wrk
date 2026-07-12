// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { BaseSelector } from './BaseSelector'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { BranchBase } from '@/types/models'

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn() },
}))

const mockBases: BranchBase[] = [
  { ref: 'develop', label: 'develop', type: 'local', detail: '' },
  { ref: 'feature/x', label: 'feature/x', type: 'local', detail: '' },
  { ref: 'origin/main', label: 'origin/main', type: 'remote', detail: '' },
  { ref: 'v1.0', label: 'v1.0', type: 'tag', detail: '' },
  { ref: 'a3f5c1d', label: 'a3f5c1d', type: 'commit', detail: 'fix: login bug' },
]

let container: HTMLDivElement
let root: Root

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

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )!.set!
  setter.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function allButtons(): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll('button'))
}

function renderSelector(props?: Partial<React.ComponentProps<typeof BaseSelector>>) {
  act(() => {
    root.render(
      <TooltipProvider>
        <BaseSelector
          bases={mockBases}
          currentBranch="main"
          selectedBase=""
          onSelect={() => {}}
          {...props}
        />
      </TooltipProvider>,
    )
  })
}

describe('BaseSelector', () => {
  it('renders grouped bases with group headers', () => {
    renderSelector()
    const text = container.textContent ?? ''
    expect(text).toContain('Local branches')
    expect(text).toContain('Remote branches')
    expect(text).toContain('Tags')
    expect(text).toContain('Recent commits')
    expect(text).toContain('develop')
    expect(text).toContain('origin/main')
    expect(text).toContain('v1.0')
    expect(text).toContain('a3f5c1d')
  })

  it('filters bases by the search query', async () => {
    renderSelector()
    const searchInput = container.querySelector(
      'input[placeholder="Search base..."]',
    ) as HTMLInputElement

    await act(async () => {
      setInputValue(searchInput, 'origin')
    })

    const text = container.textContent ?? ''
    expect(text).toContain('origin/main')
    expect(text).not.toContain('develop')
  })

  it('filters commits by their subject', async () => {
    renderSelector()
    const searchInput = container.querySelector(
      'input[placeholder="Search base..."]',
    ) as HTMLInputElement

    await act(async () => {
      setInputValue(searchInput, 'login')
    })

    const text = container.textContent ?? ''
    expect(text).toContain('a3f5c1d')
    expect(text).not.toContain('develop')
  })

  it('calls onSelect with the ref when a row is clicked', async () => {
    const onSelect = vi.fn()
    renderSelector({ onSelect })

    const devBtn = allButtons().find((b) => b.textContent?.includes('develop'))!
    await act(async () => {
      devBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(onSelect).toHaveBeenCalledWith('develop')
  })

  it('highlights the selected base', () => {
    renderSelector({ selectedBase: 'v1.0' })
    const tagBtn = allButtons().find((b) => b.textContent?.includes('v1.0'))!
    expect(tagBtn.className).toContain('bg-primary')
  })

  it('marks the current branch with (current)', () => {
    renderSelector({ currentBranch: 'develop' })
    const text = container.textContent ?? ''
    expect(text).toContain('(current)')
  })

  it('shows "No bases found" when search has no matches', async () => {
    renderSelector()
    const searchInput = container.querySelector(
      'input[placeholder="Search base..."]',
    ) as HTMLInputElement

    await act(async () => {
      setInputValue(searchInput, 'zzz-nothing')
    })

    expect(container.textContent).toContain('No bases found')
  })

  it('shows the selected base indicator below the list', () => {
    renderSelector({ selectedBase: 'origin/main' })
    const text = container.textContent ?? ''
    expect(text).toContain('Base:')
    expect(text).toContain('origin/main')
  })
})
