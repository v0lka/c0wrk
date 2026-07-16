// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'

// jsdom lacks window.localStorage, which zustand's persist middleware
// captures at store-creation time. Install an in-memory polyfill before any
// store module is imported (mirrors GitHistoryTab.test.tsx).
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  g.IS_REACT_ACT_ENVIRONMENT = true
  win.IS_REACT_ACT_ENVIRONMENT = true
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

// --- Mock the attachment API wrapper so tests never touch the Wails backend ---
const { apiMocks } = vi.hoisted(() => ({
  apiMocks: {
    getBlackboardAttachmentMarkdown: vi.fn(),
  },
}))
vi.mock('@/api/attachments', () => apiMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

// --- Mock the file viewer store so we can assert the double-click opens a
// virtual (non-disk-backed) tab and fills it with the fetched markdown. ---
const { fvMocks } = vi.hoisted(() => ({
  fvMocks: {
    openVirtualFile: vi.fn(),
    setCollapsed: vi.fn(),
    setFileContent: vi.fn(),
    setFileError: vi.fn(),
    collapsed: false,
  },
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: Object.assign(
    (selector: (s: typeof fvMocks) => unknown) => selector(fvMocks),
    { getState: () => fvMocks },
  ),
}))

vi.mock('@/stores/sessionStore', () => ({
  useSessionStore: (selector: (s: { activeSessionId: string | null }) => unknown) =>
    selector({ activeSessionId: 'sess-1' }),
}))

vi.mock('@/stores/uiStore', () => ({
  useUIStore: (selector: (s: { sidebarCollapsed: boolean }) => unknown) =>
    selector({ sidebarCollapsed: false }),
}))

// --- Mock the blackboard store with a controllable state handle ---
const { bbMocks } = vi.hoisted(() => ({
  bbMocks: {
    state: null as { attachments: unknown[] } | null,
  },
}))
vi.mock('@/stores/blackboardStore', () => ({
  useBlackboardState: () => bbMocks.state,
  useHasBlackboardState: () => bbMocks.state !== null,
}))

import { BlackboardPanel } from './BlackboardPanel'

const ATTACHMENT = {
  id: 'att-1',
  original_name: 'report.pdf',
  format: 'pdf',
  size_bytes: 1024,
  attached_at: '2026-01-01T00:00:00Z',
}

function makeState(attachments: unknown[]) {
  return {
    task_id: 'task-1',
    session_id: 'sess-1',
    status: 'completed',
    step_results: {},
    reflections: [],
    facts: [],
    attachments,
    final_output: '',
  }
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  bbMocks.state = makeState([ATTACHMENT])
  apiMocks.getBlackboardAttachmentMarkdown.mockReset()
  apiMocks.getBlackboardAttachmentMarkdown.mockResolvedValue('# Report\n\ncontent')
  fvMocks.openVirtualFile.mockReset()
  fvMocks.setCollapsed.mockReset()
  fvMocks.setFileContent.mockReset()
  fvMocks.setFileError.mockReset()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => { root.unmount() })
  container.remove()
  document.body.innerHTML = ''
})

/** Flush microtasks so the async markdown fetch resolves. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

function renderPanel() {
  act(() => {
    root.render(
      <TooltipProvider>
        <BlackboardPanel />
      </TooltipProvider>,
    )
  })
}

/** Open the collapsible Blackboard panel (collapsed by default). */
function openPanel() {
  const header = Array.from(container.querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').includes('Blackboard'),
  ) as HTMLButtonElement | undefined
  expect(header).toBeTruthy()
  act(() => { header!.click() })
}

describe('BlackboardPanel attachment double-click', () => {
  it('opens the attachment markdown in the file viewer', async () => {
    renderPanel()
    openPanel()

    const row = container.querySelector('[title="Double-click to open in viewer"]') as HTMLElement
    expect(row).toBeTruthy()

    await act(async () => {
      row.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    })
    await flush()

    // A virtual pseudo-path keyed by the attachment id is opened as markdown.
    expect(fvMocks.openVirtualFile).toHaveBeenCalledWith('c0wrk:attachment/att-1/report.pdf', 'markdown')
    expect(fvMocks.setCollapsed).toHaveBeenCalledWith(false)
    // The markdown is fetched for this session + attachment and pushed into the tab.
    expect(apiMocks.getBlackboardAttachmentMarkdown).toHaveBeenCalledWith('sess-1', 'att-1')
    expect(fvMocks.setFileContent).toHaveBeenCalledWith('c0wrk:attachment/att-1/report.pdf', '# Report\n\ncontent', 'markdown')
    expect(fvMocks.setFileError).not.toHaveBeenCalled()
  })

  it('surfaces a viewer error when the markdown fetch fails', async () => {
    apiMocks.getBlackboardAttachmentMarkdown.mockRejectedValue(new Error('boom'))
    renderPanel()
    openPanel()

    const row = container.querySelector('[title="Double-click to open in viewer"]') as HTMLElement
    await act(async () => {
      row.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    })
    await flush()

    expect(fvMocks.setFileError).toHaveBeenCalledWith('c0wrk:attachment/att-1/report.pdf', 'boom')
    expect(fvMocks.setFileContent).not.toHaveBeenCalled()
  })
})
