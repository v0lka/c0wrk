// Tests for usePasteHandler — the async non-fast-path paste body.
//
// These cover the per-kind routing contract:
//   - image accepted on a vision model → attachments staged, no error
//   - image rejected on a non-vision model → nothing staged, error banner set
//   - files (Finder/Explorer copy) → attachments staged
//   - text → editor.insertAtCursor called, no attachment
//   - empty → no-op
//   - no active session → a session is created on demand before pasting
//   - backend paste failure → runtime_error toast
//
// Follows the established createRoot + jsdom harness pattern (see
// useAttachmentsInput.test.tsx). The editor is mocked: only insertAtCursor is
// spied on (the text-kind route).

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { usePasteHandler } from '@/hooks/usePasteHandler'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { SessionInfo, AttachmentInfoUI, ModelInfo, PasteResultUI } from '@/types/models'

const spies = vi.hoisted(() => ({
  pasteFromClipboard: vi.fn<(sessionId: string, supportsVision: boolean) => Promise<PasteResultUI>>(),
  createSession: vi.fn<() => Promise<SessionInfo>>(),
  emit: vi.fn<(event: string, data: unknown) => void>(),
  insertAtCursor: vi.fn<(text: string) => void>(),
}))

vi.mock('@/api/attachments', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/attachments')>()
  return {
    ...actual,
    pasteFromClipboard: spies.pasteFromClipboard,
  }
})

vi.mock('@/api/sessions', () => ({
  createSession: spies.createSession,
}))

vi.mock('@/api/runtime', () => ({
  emit: spies.emit,
}))

const models: ModelInfo[] = [
  { name: 'vision-m', provider: 'p', family: 'f', vision: true },
  { name: 'novision-m', provider: 'p', family: 'f', vision: false },
]
vi.mock('@/hooks/useConfigData', () => ({
  useConfigData: () => ({ allModels: models, defaultModel: 'vision-m', loaded: true }),
}))

const makeSession = (id: string): SessionInfo => ({
  id,
  project_id: 'proj',
  name: 's',
  created_at: '',
  last_active_at: '',
  archived: false,
  pinned: false,
  active: true,
  total_input_tokens: 0,
  total_output_tokens: 0,
  model: '',
  family: '',
  has_unfinished_task: false,
})

const makeAttachment = (id: string): AttachmentInfoUI => ({
  id,
  originalName: id + '.png',
  format: 'png',
  sizeBytes: 1,
  isImage: true,
})

// Harness: capture the latest onPaste so the test can invoke it. The editor is
// mocked — only insertAtCursor is exercised (text route).
let captured: (((data: DataTransfer) => Promise<void>) | null) = null
function Harness() {
  const fakeEditor = { insertAtCursor: spies.insertAtCursor } as unknown as Parameters<typeof usePasteHandler>[0]
  const { onPaste } = usePasteHandler(fakeEditor)
  captured = onPaste
  return null
}

async function runPaste(): Promise<void> {
  if (!captured) throw new Error('harness did not capture onPaste')
  // DataTransfer is not exposed in this jsdom build; the handler ignores the
  // argument (it routes purely on the backend's PasteResult), so a minimal
  // stub object is sufficient.
  await captured({} as DataTransfer)
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  // Store mutations run inside act(): roots from earlier tests may still be
  // mounted and subscribed, so an unwrapped mutation would re-render them
  // outside act().
  act(() => {
    useInputModeStore.setState({ selectedModel: null })
    useAttachmentsStore.getState().clear()
    useSessionStore.setState({ sessions: [], activeSessionId: 'sess-1' })
  })

  spies.pasteFromClipboard.mockReset()
  spies.createSession.mockReset()
  spies.emit.mockReset()
  spies.insertAtCursor.mockReset()

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness />)
  })
})

describe('usePasteHandler routing', () => {
  it('stages an accepted image on a vision model (kind=image, no rejected)', async () => {
    const list = [makeAttachment('img1')]
    spies.pasteFromClipboard.mockResolvedValue({ kind: 'image', files: list })

    await act(async () => { await runPaste() })

    // supportsVision forwarded to the backend (default model is vision-m).
    expect(spies.pasteFromClipboard).toHaveBeenCalledWith('sess-1', true)
    expect(useAttachmentsStore.getState().attachments).toEqual(list)
    expect(useAttachmentsStore.getState().imageError).toBeNull()
  })

  it('surfaces a localized banner and stages nothing when the image is rejected (non-vision)', async () => {
    act(() => { useInputModeStore.setState({ selectedModel: 'novision-m' }) })
    act(() => { root.render(<Harness />) })

    // The backend returns the vision sentinel; the frontend synthesizes the
    // rejection banner (the backend never sends human-readable text).
    spies.pasteFromClipboard.mockResolvedValue({
      kind: 'image',
      files: [],
      rejected: 'vision_unsupported',
    })

    await act(async () => { await runPaste() })

    // supportsVision=false forwarded to the backend.
    expect(spies.pasteFromClipboard).toHaveBeenCalledWith('sess-1', false)
    expect(useAttachmentsStore.getState().attachments).toEqual([])
    const err = useAttachmentsStore.getState().imageError ?? ''
    expect(err).toContain('does not support images')
    expect(err).toContain('novision-m')
  })

  it('shows a real backend error verbatim (not masked as a vision rejection)', async () => {
    spies.pasteFromClipboard.mockResolvedValue({
      kind: 'image',
      files: [],
      rejected: 'failed to write temp image: disk full',
    })

    await act(async () => { await runPaste() })

    expect(useAttachmentsStore.getState().imageError).toBe('failed to write temp image: disk full')
  })

  it('surfaces a banner when pasted image files are skipped (non-vision, kind=files)', async () => {
    act(() => { useInputModeStore.setState({ selectedModel: 'novision-m' }) })
    act(() => { root.render(<Harness />) })

    // Clipboard had image files; vision off → backend skipped them and reports
    // the count. No files staged → the localized banner is shown.
    spies.pasteFromClipboard.mockResolvedValue({
      kind: 'files',
      files: [],
      skippedImages: 2,
    })

    await act(async () => { await runPaste() })

    expect(useAttachmentsStore.getState().attachments).toEqual([])
    const err = useAttachmentsStore.getState().imageError ?? ''
    expect(err).toContain('does not support images')
    expect(err).toContain('novision-m')
  })

  it('does not clear an existing banner when nothing was staged and nothing skipped', async () => {
    // A concurrent/empty result with no files and no skips must leave an
    // existing banner (e.g. from a concurrent drop) untouched.
    act(() => { useAttachmentsStore.getState().setImageError('stale banner from a drop') })
    spies.pasteFromClipboard.mockResolvedValue({ kind: 'files', files: [] })

    await act(async () => { await runPaste() })

    expect(useAttachmentsStore.getState().imageError).toBe('stale banner from a drop')
  })

  it('stages copied files (kind=files)', async () => {
    const list = [makeAttachment('f1'), makeAttachment('f2')]
    spies.pasteFromClipboard.mockResolvedValue({ kind: 'files', files: list })

    await act(async () => { await runPaste() })

    expect(useAttachmentsStore.getState().attachments).toEqual(list)
    expect(useAttachmentsStore.getState().imageError).toBeNull()
  })

  it('inserts text at the cursor (kind=text), no attachment', async () => {
    spies.pasteFromClipboard.mockResolvedValue({ kind: 'text', files: [], text: 'hello world' })

    await act(async () => { await runPaste() })

    expect(spies.insertAtCursor).toHaveBeenCalledWith('hello world')
    expect(useAttachmentsStore.getState().attachments).toEqual([])
  })

  it('is a no-op for empty clipboard (kind=empty)', async () => {
    spies.pasteFromClipboard.mockResolvedValue({ kind: 'empty', files: [] })

    await act(async () => { await runPaste() })

    expect(spies.insertAtCursor).not.toHaveBeenCalled()
    expect(useAttachmentsStore.getState().attachments).toEqual([])
  })

  it('creates a session on demand when none is active', async () => {
    const newSession = makeSession('new-sess')
    spies.createSession.mockResolvedValue(newSession)
    spies.pasteFromClipboard.mockResolvedValue({ kind: 'text', files: [], text: 't' })

    act(() => { useSessionStore.setState({ sessions: [], activeSessionId: null }) })
    await act(async () => { await runPaste() })

    expect(spies.createSession).toHaveBeenCalledOnce()
    // paste received the freshly-created session id.
    expect(spies.pasteFromClipboard).toHaveBeenCalledWith('new-sess', expect.any(Boolean))
    expect(useSessionStore.getState().activeSessionId).toBe('new-sess')
  })

  it('emits a runtime_error toast when the backend paste fails', async () => {
    spies.pasteFromClipboard.mockRejectedValue(new Error('boom'))

    await act(async () => { await runPaste() })

    expect(spies.emit).toHaveBeenCalledWith('runtime_error', expect.objectContaining({
      message: 'Failed to paste from clipboard',
    }))
  })
})
