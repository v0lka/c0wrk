// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { AttachmentChips } from './AttachmentChips'
import {
  STUDY_PAPER_SKILL,
  DEFAULT_STUDY_MODE,
  buildStudyAttachmentPrompt,
} from '@/components/papers/paperActions'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { AttachmentInfoUI } from '@/types/models'

// Enable React's act() flushing in this jsdom environment.

// Mock removeAttachment so the chip remove button doesn't call the Wails backend.
vi.mock('@/api/attachments', () => ({
  removeAttachment: vi.fn().mockResolvedValue(undefined),
}))

// Mock the optimistic-upload lifecycle so the cancel button stays unit-level.
vi.mock('@/lib/attachmentUploads', () => ({
  cancelAttachmentUpload: vi.fn(),
}))

// Mock emit so runtime_error events don't touch the Wails runtime.
vi.mock('@/api/runtime', () => ({
  emit: vi.fn(),
}))

// Study-paper dispatch goes through the shared message sender; mock it so the
// PDF chip's dispatch is asserted without a running Wails runtime.
const { sendMock } = vi.hoisted(() => ({ sendMock: vi.fn() }))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendMock, cancel: vi.fn(), isProcessing: false }),
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
  useAttachmentsStore.setState({ attachmentsBySession: {}, uploadsBySession: {}, namesById: {}, imageErrorBySession: {} })
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
    vi.clearAllMocks()
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

  it('renders a spinner chip for in-flight uploads (ahead of staged chips)', () => {
    useAttachmentsStore.getState().setAttachments('s1', [DOC])
    useAttachmentsStore.setState({
      uploadsBySession: {
        s1: [{ id: 'u1', fileName: 'notes.md', path: '/p/notes.md', isImage: false }],
      },
    })

    const container = render(<AttachmentChips />)

    // Spinner chip renders with the file name and a spinning indicator.
    expect(container.textContent).toContain('notes.md')
    expect(container.querySelector('.animate-spin')).not.toBeNull()
    // Staged chips still render alongside.
    expect(container.textContent).toContain('report.pdf')
  })

  it('renders an image icon on image upload placeholders', () => {
    useAttachmentsStore.setState({
      uploadsBySession: {
        s1: [{ id: 'u1', fileName: 'photo.png', path: '/p/photo.png', isImage: true }],
      },
    })
    const container = render(<AttachmentChips />)
    expect(container.textContent).toContain('photo.png')
    expect(container.querySelector('.animate-spin')).not.toBeNull()
  })

  it('cancel X on an upload chip calls cancelAttachmentUpload with the session + upload', async () => {
    const upload = { id: 'u1', fileName: 'notes.md', path: '/p/notes.md', isImage: false }
    useAttachmentsStore.setState({ uploadsBySession: { s1: [upload] } })

    const container = render(<AttachmentChips />)
    const cancelBtn = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Cancel uploading notes.md"]',
    )
    expect(cancelBtn).not.toBeNull()

    await act(async () => {
      cancelBtn!.click()
    })

    const { cancelAttachmentUpload } = await import('@/lib/attachmentUploads')
    expect(cancelAttachmentUpload).toHaveBeenCalledWith('s1', upload)
  })

  it('renders the chips row for uploads only (no staged attachments yet)', () => {
    useAttachmentsStore.setState({
      uploadsBySession: {
        s1: [{ id: 'u1', fileName: 'notes.md', path: '/p/notes.md', isImage: false }],
      },
    })
    const container = render(<AttachmentChips />)
    expect(container.textContent).toContain('notes.md')
  })
})

describe('AttachmentChips — Study (PDF only)', () => {
  beforeEach(() => {
    sendMock.mockReset()
    sendMock.mockResolvedValue(undefined)
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

  it('offers a Study action on a PDF chip and dispatches study-paper', async () => {
    useAttachmentsStore.getState().setAttachments('s1', [DOC])
    const container = render(<AttachmentChips />)

    const study = container.querySelector<HTMLButtonElement>('[data-testid="attachment-study"]')
    expect(study).not.toBeNull()

    await act(async () => {
      study!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    expect(sendMock).toHaveBeenCalledWith(
      buildStudyAttachmentPrompt('report.pdf', DEFAULT_STUDY_MODE),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
  })

  it('offers no Study action on a non-PDF document (no dispatch surface)', () => {
    const md: AttachmentInfoUI = { id: 'm1', originalName: 'notes.md', format: 'md', sizeBytes: 10 }
    useAttachmentsStore.getState().setAttachments('s1', [md])
    const container = render(<AttachmentChips />)

    expect(container.querySelector('[data-testid="attachment-study"]')).toBeNull()
    expect(sendMock).not.toHaveBeenCalled()
  })

  it('offers no Study action on an image attachment', () => {
    useAttachmentsStore.getState().setAttachments('s1', [IMG])
    const container = render(<AttachmentChips />)
    expect(container.querySelector('[data-testid="attachment-study"]')).toBeNull()
  })
})
