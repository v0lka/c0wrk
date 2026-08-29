// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { AttachmentChips } from './AttachmentChips'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { AttachmentInfoUI } from '@/types/models'

// Enable React's act() flushing in this jsdom environment.

// Mock removeAttachment so the chip remove button doesn't call the Wails backend.
vi.mock('@/api/attachments', () => ({
  removeAttachment: vi.fn().mockResolvedValue(undefined),
}))

// Mock emit so runtime_error events don't touch the Wails runtime.
vi.mock('@/api/runtime', () => ({
  emit: vi.fn(),
}))

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

function resetStores() {
  useAttachmentsStore.setState({ attachmentsBySession: {}, namesById: {}, imageErrorBySession: {} })
  useSessionStore.setState({ activeSessionId: 's1' })
}

const DOC: AttachmentInfoUI = { id: 'd1', originalName: 'report.pdf', format: 'pdf', sizeBytes: 1024 }
const IMG: AttachmentInfoUI = {
  id: 'i1',
  originalName: 'screenshot.png',
  format: 'png',
  sizeBytes: 2048,
  isImage: true,
  thumbnail: 'data:image/jpeg;base64,abc123',
}

describe('AttachmentChips', () => {
  beforeEach(() => {
    resetStores()
    document.body.innerHTML = ''
    root = null
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('renders nothing when there are no attachments', () => {
    const container = render(<AttachmentChips />)
    expect(container.innerHTML).toBe('')
  })

  it('renders a FileText icon for document attachments', () => {
    useAttachmentsStore.getState().setAttachments('s1', [DOC])
    const container = render(<AttachmentChips />)
    // Document chip has no <img> element.
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toContain('report.pdf')
  })

  it('renders a thumbnail <img> for image attachments', () => {
    useAttachmentsStore.getState().setAttachments('s1', [IMG])
    const container = render(<AttachmentChips />)
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img?.getAttribute('src')).toBe('data:image/jpeg;base64,abc123')
    expect(img?.getAttribute('alt')).toBe('screenshot.png')
    expect(img?.className).toContain('object-cover')
  })

  it('renders both document and image chips together', () => {
    useAttachmentsStore.getState().setAttachments('s1', [DOC, IMG])
    const container = render(<AttachmentChips />)
    expect(container.textContent).toContain('report.pdf')
    expect(container.textContent).toContain('screenshot.png')
    expect(container.querySelector('img')).not.toBeNull()
  })

  it('renders only the active session list (switching sessions swaps the chips)', () => {
    // Two sessions with different pending lists; s1 is active.
    useAttachmentsStore.getState().setAttachments('s1', [DOC])
    useAttachmentsStore.getState().setAttachments('s2', [IMG])

    const container = render(<AttachmentChips />)
    expect(container.textContent).toContain('report.pdf')
    expect(container.textContent).not.toContain('screenshot.png')

    // Switch the active session — the same mounted chips now show s2's list.
    act(() => {
      useSessionStore.setState({ activeSessionId: 's2' })
    })
    expect(container.textContent).toContain('screenshot.png')
    expect(container.textContent).not.toContain('report.pdf')
  })

  it('renders nothing when only ANOTHER session has pending attachments', () => {
    useAttachmentsStore.getState().setAttachments('s2', [DOC])
    const container = render(<AttachmentChips />)
    expect(container.innerHTML).toBe('')
  })
})
