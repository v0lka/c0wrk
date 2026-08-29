// Regression tests for useAttachmentsInput — the 📎 button entry point.
//
// These verify the behavior contract is preserved after the refactor that
// extracted the staging pipeline (vision gating, session-on-demand, attach +
// store sync) into useStageAttachments.handleAttach must remain a thin
// wrapper: pickAttachmentFiles() → stageAttachmentPaths(). The staging logic
// is exercised transitively, so these tests also lock down the delegation.
//
// No @testing-library/react in this repo; we follow the established
// createRoot + jsdom pattern (see ModelCombobox.test.tsx).

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { useAttachmentsInput } from '@/hooks/useAttachmentsInput'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { SessionInfo, AttachmentInfoUI, ModelInfo } from '@/types/models'

// Spies exist before vi.mock factories run so they can be referenced there
// and re-asserted/reset in test bodies.
const spies = vi.hoisted(() => ({
  pickAttachmentFiles: vi.fn<() => Promise<string[]>>(),
  attachFiles: vi.fn<(sessionId: string, paths: string[]) => Promise<AttachmentInfoUI[]>>(),
  createSession: vi.fn<() => Promise<SessionInfo>>(),
  emit: vi.fn<(event: string, data: unknown) => void>(),
}))

// Mock the attachment API: keep isImagePath real (pure), spy on the RPCs.
vi.mock('@/api/attachments', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/attachments')>()
  return {
    ...actual,
    pickAttachmentFiles: spies.pickAttachmentFiles,
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
  useConfigData: () => ({ allModels: models, defaultModel: 'vision-m', loaded: true }),
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

// Harness: capture the latest handleAttach so the test can invoke it.
let captured: (() => Promise<void>) | null = null
function Harness({ activeSessionId }: { activeSessionId: string | null }) {
  const { handleAttach } = useAttachmentsInput(activeSessionId)
  captured = handleAttach
  return null
}

/** Invoke the captured handleAttach, failing loudly if the harness never ran. */
async function runAttach(): Promise<void> {
  if (!captured) throw new Error('harness did not capture handleAttach')
  await captured()
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  // Reset stores to a clean baseline. Mutations run inside act(): roots from
  // earlier tests may still be mounted and subscribed (they are replaced,
  // never unmounted), so an unwrapped mutation would re-render them outside
  // act().
  act(() => {
    useInputModeStore.setState({ selectedModel: null })
    useAttachmentsStore.setState({ attachmentsBySession: {}, uploadsBySession: {}, namesById: {}, imageErrorBySession: {} })
    useSessionStore.setState({ sessions: [], activeSessionId: null })
  })

  spies.pickAttachmentFiles.mockReset()
  spies.attachFiles.mockReset()
  spies.createSession.mockReset()
  spies.emit.mockReset()

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness activeSessionId={'sess-1'} />)
  })
})

function setActiveSession(id: string | null) {
  act(() => {
    root.render(<Harness activeSessionId={id} />)
  })
}

describe('useAttachmentsInput.handleAttach (📎 button)', () => {
  it('is a no-op when the user cancels the picker (empty selection)', async () => {
    spies.pickAttachmentFiles.mockResolvedValue([])
    await act(async () => { await runAttach() })
    expect(spies.attachFiles).not.toHaveBeenCalled()
    expect(spies.createSession).not.toHaveBeenCalled()
  })

  it('stages files into an existing session without creating one', async () => {
    const list = [makeAttachment('a1')]
    spies.pickAttachmentFiles.mockResolvedValue(['/x/doc.md'])
    spies.attachFiles.mockResolvedValue(list)

    await act(async () => { await runAttach() })

    expect(spies.createSession).not.toHaveBeenCalled()
    expect(spies.attachFiles).toHaveBeenCalledWith('sess-1', ['/x/doc.md'])
    expect(useAttachmentsStore.getState().attachmentsBySession['sess-1']).toEqual(list)
    // Successful attach clears any stale image error.
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-1']).toBeUndefined()
  })

  it('creates a session on demand when none is active', async () => {
    const newSession = makeSession('new-sess')
    spies.pickAttachmentFiles.mockResolvedValue(['/x/doc.md'])
    spies.createSession.mockResolvedValue(newSession)
    spies.attachFiles.mockResolvedValue([makeAttachment('a1')])

    setActiveSession(null)
    await act(async () => { await runAttach() })

    expect(spies.createSession).toHaveBeenCalledOnce()
    // attachFiles received the freshly-created session id.
    expect(spies.attachFiles).toHaveBeenCalledWith('new-sess', ['/x/doc.md'])
    // The new session was registered + activated.
    expect(useSessionStore.getState().sessions?.map((s) => s.id)).toContain('new-sess')
    expect(useSessionStore.getState().activeSessionId).toBe('new-sess')
  })

  it('rejects images on a non-vision model, staging documents only', async () => {
    // Select a non-vision model as the effective model.
    act(() => { useInputModeStore.setState({ selectedModel: 'novision-m' }) })
    // Re-render so the hook re-binds to the new selectedModel.
    setActiveSession('sess-1')

    spies.pickAttachmentFiles.mockResolvedValue(['/p/img.png', '/p/doc.md'])
    spies.attachFiles.mockResolvedValue([makeAttachment('d1')])

    await act(async () => { await runAttach() })

    // Image rejected, document staged.
    expect(spies.attachFiles).toHaveBeenCalledWith('sess-1', ['/p/doc.md'])
    // Error banner surfaced naming the (bare) model.
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-1']).toContain('novision-m')
  })

  it('rejects an image-only selection on a non-vision model and stages nothing', async () => {
    act(() => { useInputModeStore.setState({ selectedModel: 'novision-m' }) })
    setActiveSession('sess-1')

    spies.pickAttachmentFiles.mockResolvedValue(['/p/img.png'])

    await act(async () => { await runAttach() })

    // Nothing to stage → attachFiles never called.
    expect(spies.attachFiles).not.toHaveBeenCalled()
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-1']).toContain('novision-m')
  })

  it('stages images normally when the model supports vision', async () => {
    spies.pickAttachmentFiles.mockResolvedValue(['/p/img.png', '/p/doc.md'])
    spies.attachFiles.mockResolvedValue([makeAttachment('a1'), makeAttachment('a2')])

    await act(async () => { await runAttach() })

    // Both image and document passed through unchanged.
    expect(spies.attachFiles).toHaveBeenCalledWith('sess-1', ['/p/img.png', '/p/doc.md'])
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-1']).toBeUndefined()
  })

  it('surfaces a runtime_error toast when attachFiles fails', async () => {
    spies.pickAttachmentFiles.mockResolvedValue(['/x/doc.md'])
    spies.attachFiles.mockRejectedValue(new Error('boom'))

    await act(async () => { await runAttach() })

    expect(spies.emit).toHaveBeenCalledWith('runtime_error', expect.objectContaining({
      message: 'Failed to attach files',
    }))
  })
})
