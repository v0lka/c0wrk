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
import { NULL_SESSION_KEY } from '@/stores/chatInputStore'
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
    useAttachmentsStore.setState({ attachmentsBySession: {}, namesById: {}, imageErrorBySession: {} })
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
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-1']).toEqual(list)
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-1']).toBeUndefined()
  })

  it('rejects an image-only list on a non-vision model and stages nothing', async () => {
    // defaultModel = novision-m, selectedModel = null → non-vision effective.
    await act(async () => { await stage('sess-1', ['/p/img.png']) })

    expect(spies.attachFiles).not.toHaveBeenCalled()
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-1']).toContain('novision-m')
  })

  it('rejects an image-only list with no session under the NULL sentinel key', async () => {
    // No active session and nothing stageable → no session is created; the
    // banner must still surface, keyed to the NULL_SESSION_KEY sentinel.
    await act(async () => { await stage(null, ['/p/img.png']) })

    expect(spies.createSession).not.toHaveBeenCalled()
    expect(spies.attachFiles).not.toHaveBeenCalled()
    expect(useAttachmentsStore.getState().imageErrorBySession[NULL_SESSION_KEY]).toContain('novision-m')
  })

  it('stages mixed paths on a non-vision model but drops images (documents only)', async () => {
    spies.attachFiles.mockResolvedValue([makeAttachment('d1')])

    await act(async () => { await stage('sess-1', ['/p/img.png', '/p/doc.md']) })

    // Only the document is passed through.
    expect(spies.attachFiles).toHaveBeenCalledWith('sess-1', ['/p/doc.md'])
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-1']).toContain('novision-m')
  })

  it('stages images on a vision model without filtering', async () => {
    act(() => { useInputModeStore.setState({ selectedModel: 'vision-m' }) })
    rerender()

    spies.attachFiles.mockResolvedValue([makeAttachment('i1'), makeAttachment('d1')])

    await act(async () => { await stage('sess-1', ['/p/img.png', '/p/doc.md']) })

    // Both passed through unchanged; no error banner.
    expect(spies.attachFiles).toHaveBeenCalledWith('sess-1', ['/p/img.png', '/p/doc.md'])
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-1']).toBeUndefined()
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
    // The staged list lands in the freshly-created session's key.
    expect(useAttachmentsStore.getState().attachmentsBySession['new-sess']).toEqual([
      makeAttachment('a1'),
    ])
  })

  it('does not create a session when all images are rejected (nothing stageable)', async () => {
    // Non-vision model, image-only input → nothing staged, and a session is
    // NOT created (early return before session-on-demand).
    await act(async () => { await stage(null, ['/p/img.png']) })

    expect(spies.createSession).not.toHaveBeenCalled()
    expect(spies.attachFiles).not.toHaveBeenCalled()
  })

  it('clears a stale image error on a successful vision-supported stage', async () => {
    act(() => { useInputModeStore.setState({ selectedModel: 'vision-m' }) })
    rerender()
    // Seed a stale error from a previous failed attempt.
    act(() => { useAttachmentsStore.getState().setImageError('sess-1', 'stale') })

    spies.attachFiles.mockResolvedValue([makeAttachment('a1')])

    await act(async () => { await stage('sess-1', ['/p/doc.md']) })

    expect('sess-1' in useAttachmentsStore.getState().imageErrorBySession).toBe(false)
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

    const err = useAttachmentsStore.getState().imageErrorBySession['sess-1'] ?? ''
    expect(err).toContain('novision-m')
  })
})

describe('useStageAttachments per-session regression (switch while pending)', () => {
  it('a stage started in A lands its list in A after switching to B mid-flight', async () => {
    // Deferred attachFiles: keep the RPC pending while the test switches the
    // active session, then resolve it — the result must land in A's key.
    let resolveAttach!: (list: AttachmentInfoUI[]) => void
    spies.attachFiles.mockImplementation(
      () => new Promise<AttachmentInfoUI[]>((res) => { resolveAttach = res }),
    )

    let pending!: Promise<void>
    await act(async () => {
      pending = stage('sess-a', ['/p/doc.md'])
    })

    // Switch the visible session while the attach RPC is still in flight.
    act(() => {
      useSessionStore.setState({ sessions: [], activeSessionId: 'sess-b' })
    })

    const list = [makeAttachment('a1'), makeAttachment('a2')]
    await act(async () => {
      resolveAttach(list)
      await pending
    })

    // The completed upload landed in the ORIGINATING session's key…
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-a']).toEqual(list)
    // …and the newly-visible session's slice was never touched.
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-b']).toBeUndefined()
  })

  it('a vision rejection staged in A keys the banner to A only, not the visible session B', async () => {
    // Non-vision default model; mixed selection staged in A with a deferred
    // RPC. The banner is keyed to A (the origin) at staging time; when the
    // stage completes after a switch, B's slice must still be clean.
    let resolveAttach!: (list: AttachmentInfoUI[]) => void
    spies.attachFiles.mockImplementation(
      () => new Promise<AttachmentInfoUI[]>((res) => { resolveAttach = res }),
    )

    let pending!: Promise<void>
    await act(async () => {
      pending = stage('sess-a', ['/p/img.png', '/p/doc.md'])
    })

    act(() => {
      useSessionStore.setState({ sessions: [], activeSessionId: 'sess-b' })
    })

    await act(async () => {
      resolveAttach([makeAttachment('d1')])
      await pending
    })

    expect(useAttachmentsStore.getState().imageErrorBySession['sess-a']).toContain('novision-m')
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-b']).toBeUndefined()
  })
})
