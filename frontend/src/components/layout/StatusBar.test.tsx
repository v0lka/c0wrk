// @vitest-environment jsdom
//
// Tests for the StatusBar composition contract: the live process-memory
// indicator is the last right-hand block, separated from the vector-index
// block, and stays visible in No Project mode while the index block is
// hidden. When the indicator has no sample it renders nothing at all — in
// particular no stray separator is left after the last visible block.

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Store mocks (plain selector-invoking doubles; state objects are
// referenced lazily from closures so vi.mock hoisting is safe) ---

const sessionStoreState = { activeSessionId: 's1' }
vi.mock('@/stores/sessionStore', () => ({
  useSessionStore: (selector: (s: typeof sessionStoreState) => unknown) =>
    selector(sessionStoreState),
}))

// No sessionTokens entry for the active session: the token/context-fill
// blocks stay hidden so the separators under test are exactly the two
// right-hand ones.
const chatStoreState = { sessionTokens: {} }
vi.mock('@/stores/chatStore', () => ({
  useChatStore: (selector: (s: typeof chatStoreState) => unknown) =>
    selector(chatStoreState),
}))

interface ProjectEntry {
  id: string
  is_no_project?: boolean
}
const projectStoreState: { projects: ProjectEntry[]; activeProjectId: string | null } = {
  projects: [],
  activeProjectId: null,
}
vi.mock('@/stores/projectStore', () => ({
  useProjectStore: (
    selector: (s: { projects: ProjectEntry[]; activeProjectId: string | null }) => unknown,
  ) => selector(projectStoreState),
}))

const uiStoreState = { sidebarCollapsed: false }
vi.mock('@/stores/uiStore', () => ({
  useUIStore: (selector: (s: typeof uiStoreState) => unknown) => selector(uiStoreState),
}))

const fileViewerStoreState = { collapsed: false }
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: (selector: (s: typeof fileViewerStoreState) => unknown) =>
    selector(fileViewerStoreState),
}))

// The real ProcessMemoryStatus is rendered; only its data source is stubbed
// so the composition (indicator + its leading separator) is exercised for
// real. `rss` is mutated per test to cover the hidden case.
const processMemoryState: { rss: number | null } = { rss: 1_234_567_896 }
vi.mock('@/hooks/useProcessMemory', () => ({
  useProcessMemory: () => processMemoryState.rss,
}))

// --- Child-component mocks (text markers; Separator as a countable stub) ---

vi.mock('@/components/ui/separator', () => ({
  Separator: () => <span data-testid="sep" />,
}))
vi.mock('./IndexingStatus', () => ({ IndexingStatus: () => <span>IDX</span> }))
vi.mock('./ContextFillStatus', () => ({ ContextFillStatus: () => <span>FILL</span> }))
vi.mock('./CompactContextButton', () => ({ CompactContextButton: () => <span>COMPACT</span> }))
vi.mock('./GoalStatusIndicator', () => ({ GoalStatusIndicator: () => null }))

import { StatusBar } from './StatusBar'

let container: HTMLElement
let root: Root

const render = () =>
  act(() => {
    root.render(<StatusBar />)
  })

beforeEach(() => {
  projectStoreState.projects = [{ id: 'p1', is_no_project: false }]
  projectStoreState.activeProjectId = 'p1'
  processMemoryState.rss = 1_234_567_896
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

describe('StatusBar process-memory placement', () => {
  it('renders the RSS indicator after the index block, separated by a Sep', () => {
    render()

    const text = container.textContent ?? ''
    const idxPos = text.indexOf('IDX')
    const rssPos = text.indexOf('RSS')
    expect(idxPos).toBeGreaterThanOrEqual(0)
    expect(rssPos).toBeGreaterThan(idxPos)

    // Token/context blocks are hidden (no session tokens) so exactly two
    // separators exist: the one before the index block and the one before
    // the RSS block.
    expect(container.querySelectorAll('[data-testid="sep"]')).toHaveLength(2)
  })

  it('hides the index block but keeps the RSS indicator in No Project mode', () => {
    projectStoreState.projects = [{ id: 'np', is_no_project: true }]
    projectStoreState.activeProjectId = 'np'

    render()

    const text = container.textContent ?? ''
    expect(text).not.toContain('IDX')
    expect(text).toContain('RSS')
    // Only the RSS indicator's separator remains.
    expect(container.querySelectorAll('[data-testid="sep"]')).toHaveLength(1)
  })

  it('renders no stray separator when the memory sample is unavailable', () => {
    processMemoryState.rss = null

    render()

    const text = container.textContent ?? ''
    expect(text).toContain('IDX')
    expect(text).not.toContain('RSS')
    // Only the index block's separator remains, and nothing trails it.
    expect(container.querySelectorAll('[data-testid="sep"]')).toHaveLength(1)
    expect(container.lastElementChild?.textContent).toBe('IDX')
  })
})
