// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ImageErrorBanner } from './ImageErrorBanner'
import { useAttachmentsStore } from '@/stores/attachmentsStore'

// Enable React's act() flushing in this jsdom environment.
;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

function render(el: React.ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
  return container
}

function resetStore() {
  useAttachmentsStore.setState({ attachments: [], namesById: {}, imageError: null })
}

describe('ImageErrorBanner', () => {
  beforeEach(() => {
    resetStore()
    document.body.innerHTML = ''
    root = null
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('renders nothing when imageError is null', () => {
    const container = render(<ImageErrorBanner />)
    expect(container.innerHTML).toBe('')
  })

  it('renders the error message when imageError is set', () => {
    useAttachmentsStore.getState().setImageError('Модель gpt-4 не поддерживает изображения.')
    const container = render(<ImageErrorBanner />)
    expect(container.textContent).toContain('Модель gpt-4 не поддерживает изображения.')
    expect(container.querySelector('button[aria-label="Dismiss image error"]')).toBeTruthy()
  })

  it('dismiss button clears the error', () => {
    useAttachmentsStore.getState().setImageError('Some error')
    const container = render(<ImageErrorBanner />)
    const btn = container.querySelector('button[aria-label="Dismiss image error"]') as HTMLButtonElement
    act(() => {
      btn.click()
    })
    expect(useAttachmentsStore.getState().imageError).toBeNull()
  })
})
