// Unit tests for useStageAttachments — the reusable staging pipeline shared by
// the 📎 picker, drag-and-drop, and paste-file entry points.
//
// These exercise stageAttachmentPaths directly (rather than transitively via
// useAttachmentsInput) so the vision-gate, session-on-demand, and store-sync
// behaviour is locked down at its source. The picker wrapper test covers the
// delegation; this file covers the shared logic itself.
//
// No @testing-library/react in this repo; we follow the established
// createRoot + jsdom pattern (see useAttachmentsInput.test.tsx).

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { useStageAttachments } from '@/hooks/useStageAttachments'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { SessionInfo, AttachmentInfoUI, ModelInfo } from '@/types/models'

// Spies exist before vi.mock factories run so they can be referenced there
// and re-asserted/reset in test bodies.
const spies = vi.hoisted(() => ({
  attachFiles: vi.fn<(sessionId: string, paths: string[]) => Promise<AttachmentInfoUI[]>>(),
  createSession: vi.fn<() => Promise<SessionInfo>>(),
  emit: vi.fn<(event: string, data: unknown) => void>(),
}))

// Mock the attachment API: keep isImagePath real (pure), spy on the RPC.
vi.mock('@/api/attachments', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/attachments')>()
  return {
    ...actual,
    attachFiles: spies.attachFiles,
  }
})

vi.mock('@/api/sessions', () => ({
  createSession: spies.createSession,
}))

vi.mock('@/api/runtime', () => ({
  emit: spies.emit,
}))

// Mock useConfigData with one vision + one non-vision model; the effective
// model is chosen per-test via useInputModeStore.setState({ selectedModel }).
const models: ModelInfo[] = [
  { name: 'vision-m', provider: 'p', family: 'f', vision: true },
  { name: 'novision-m', provider: 'p', family: 'f', vision: false },
]
vi.mock('@/hooks/useConfigData', () => ({
  useConfigData: () => ({ allModels: models, defaultModel: 'novision-m', loaded: true }),
}))

// Failure-path tests (attachFiles rejection) intentionally make the hook's
// logger.error fire; mock the logger so the expected errors don't pollute
// vitest output.
vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
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
  originalName: id + '.txt',
  format: 'txt',
  sizeBytes: 1,
})

// Harness: capture the latest stageAttachmentPaths so the test can invoke it.
let captured: ((sessionId: string | null, paths: string[]) => Promise<void>) | null = null
function Harness() {
  const { stageAttachmentPaths } = useStageAttachments()
  captured = stageAttachmentPaths
  return null
}

/** Invoke the captured stageAttachmentPaths, failing loudly if the harness never ran. */
async function stage(sessionId: string | null, paths: string[]): Promise<void> {
  if (!captured) throw new Error('harness did not capture stageAttachmentPaths')
  await captured(sessionId, paths)
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  // Reset stores to a clean baseline. Default effective model is non-vision
  // (defaultModel='novision-m'); per-test setState overrides selectedModel.
  // Mutations run inside act(): roots from earlier tests may still be mounted
  // and subscribed (they are replaced, never unmounted), so an unwrapped
  // mutation would re-render them outside act().
  act(() => {
    useInputModeStore.setState({ selectedModel: null })
    useAttachmentsStore.getState().clear()
    useSessionStore.setState({ sessions: [], activeSessionId: null })
  })

  spies.attachFiles.mockReset()
  spies.createSession.mockReset()
  spies.emit.mockReset()

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness />)
  })
})

// Force the harness to re-render so the useCallback closure inside
// useStageAttachments re-binds to the latest selectedModel (mirrors the
// setActiveSession re-render pattern in useAttachmentsInput.test.tsx).
function rerender() {
  act(() => {
    root.render(<Harness />)
  })
}

describe('useStageAttachments.stageAttachmentPaths (vision gate)', () => {
  it('stages a document on the default non-vision model', async () => {
    const list = [makeAttachment('d1')]
    spies.attachFiles.mockResolvedValue(list)

    await act(async () => { await stage('sess-1', ['/p/doc.md']) })

    expect(spies.attachFiles).toHaveBeenCalledWith('sess-1', ['/p/doc.md'])
    expect(useAttachmentsStore.getState().attachments).toEqual(list)
    expect(useAttachmentsStore.getState().imageError).toBeNull()
  })

  it('rejects an image-only list on a non-vision model and stages nothing', async () => {
    // defaultModel = novision-m, selectedModel = null → non-vision effective.
    await act(async () => { await stage('sess-1', ['/p/img.png']) })

    expect(spies.attachFiles).not.toHaveBeenCalled()
    expect(useAttachmentsStore.getState().imageError).toContain('novision-m')
  })

  it('stages mixed paths on a non-vision model but drops images (documents only)', async () => {
    spies.attachFiles.mockResolvedValue([makeAttachment('d1')])

    await act(async () => { await stage('sess-1', ['/p/img.png', '/p/doc.md']) })

    // Only the document is passed through.
    expect(spies.attachFiles).toHaveBeenCalledWith('sess-1', ['/p/doc.md'])
    expect(useAttachmentsStore.getState().imageError).toContain('novision-m')
  })

  it('stages images on a vision model without filtering', async () => {
    act(() => { useInputModeStore.setState({ selectedModel: 'vision-m' }) })
    rerender()

    spies.attachFiles.mockResolvedValue([makeAttachment('i1'), makeAttachment('d1')])

    await act(async () => { await stage('sess-1', ['/p/img.png', '/p/doc.md']) })

    // Both passed through unchanged; no error banner.
    expect(spies.attachFiles).toHaveBeenCalledWith('sess-1', ['/p/img.png', '/p/doc.md'])
    expect(useAttachmentsStore.getState().imageError).toBeNull()
  })

  it('creates a session on demand when none is active (null sessionId)', async () => {
    const newSession = makeSession('new-sess')
    spies.createSession.mockResolvedValue(newSession)
    spies.attachFiles.mockResolvedValue([makeAttachment('a1')])

    await act(async () => { await stage(null, ['/p/doc.md']) })

    expect(spies.createSession).toHaveBeenCalledOnce()
    expect(spies.attachFiles).toHaveBeenCalledWith('new-sess', ['/p/doc.md'])
    expect(useSessionStore.getState().sessions?.map((s) => s.id)).toContain('new-sess')
    expect(useSessionStore.getState().activeSessionId).toBe('new-sess')
  })

  it('creates a session even when all images are rejected (document fallback absent)', async () => {
    // Non-vision model, image-only input → nothing staged, but a session is
    // still NOT created (early return before session-on-demand).
    await act(async () => { await stage(null, ['/p/img.png']) })

    expect(spies.createSession).not.toHaveBeenCalled()
    expect(spies.attachFiles).not.toHaveBeenCalled()
  })

  it('clears a stale image error on a successful vision-supported stage', async () => {
    act(() => { useInputModeStore.setState({ selectedModel: 'vision-m' }) })
    rerender()
    // Seed a stale error from a previous failed attempt.
    act(() => { useAttachmentsStore.getState().setImageError('stale') })

    spies.attachFiles.mockResolvedValue([makeAttachment('a1')])

    await act(async () => { await stage('sess-1', ['/p/doc.md']) })

    expect(useAttachmentsStore.getState().imageError).toBeNull()
  })

  it('surfaces a runtime_error toast when attachFiles fails', async () => {
    spies.attachFiles.mockRejectedValue(new Error('boom'))

    await act(async () => { await stage('sess-1', ['/p/doc.md']) })

    expect(spies.emit).toHaveBeenCalledWith(
      'runtime_error',
      expect.objectContaining({ message: 'Failed to attach files' }),
    )
  })

  it('names the bare model in the rejection banner when model is composite', async () => {
    // selectedModel is "provider/name"; banner should show the bare "name".
    act(() => { useInputModeStore.setState({ selectedModel: 'novision-m' }) })
    rerender()

    await act(async () => { await stage('sess-1', ['/p/img.png']) })

    const err = useAttachmentsStore.getState().imageError ?? ''
    expect(err).toContain('novision-m')
  })
})
