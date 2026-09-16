// @vitest-environment jsdom
// PriorArtRow — the research dashboard's prior-art Deep-read dispatch row.
//
// The row is mounted only when the active project has prior art; its Deep-read
// action dispatches the `study-paper` skill (constant prompt from the audit
// module) through the shared message sender, and its Open action opens the raw
// catalog in the file viewer.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { PriorArtRow } from './PriorArtRow'
import {
  STUDY_PAPER_SKILL,
  DEFAULT_STUDY_MODE,
  PRIOR_ART_DEEP_READ_MODE,
  buildDeepReadPriorArtPrompt,
} from '@/components/papers/paperActions'
import { useResearchStore } from '@/stores/researchStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'

const { sendMock } = vi.hoisted(() => ({ sendMock: vi.fn() }))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendMock, cancel: vi.fn(), isProcessing: false }),
}))

let activeRoot: Root | null = null

async function render(el: React.ReactNode): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(el)
  })
  return container
}

const PATH = '/ws/.research/R-001-flaw/prior-art.md'

beforeEach(() => {
  sendMock.mockReset()
  sendMock.mockResolvedValue(undefined)
  useResearchStore.getState().reset()
  useFileViewerStore.setState({ openTabs: [], activeFile: null, files: {}, collapsed: true })
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('PriorArtRow — Deep read', () => {
  it('renders the prior-art label with its entry count', async () => {
    const container = await render(<PriorArtRow path={PATH} count={3} />)
    const row = container.querySelector('[data-testid="research-prior-art-row"]')!
    expect(row.textContent).toContain('Prior art')
    expect(
      container.querySelector('[data-testid="research-prior-art-count"]')!.textContent,
    ).toBe('3')
  })

  it('dispatches study-paper at the forced deep-read mode', async () => {
    const container = await render(<PriorArtRow path={PATH} count={3} />)

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[data-testid="research-prior-art-deep-read"]')!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    expect(sendMock).toHaveBeenCalledWith(
      buildDeepReadPriorArtPrompt(PATH),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
    // The forced depth is the deep appraisal pass, not the inferred default.
    expect(buildDeepReadPriorArtPrompt(PATH)).toContain(
      `Use ${PRIOR_ART_DEEP_READ_MODE} mode.`,
    )
    expect(DEFAULT_STUDY_MODE).toBe('auto')
  })

  it('Shift+Deep read dispatches into a new session', async () => {
    const container = await render(<PriorArtRow path={PATH} count={3} />)

    await act(async () => {
      container
        .querySelector<HTMLButtonElement>('[data-testid="research-prior-art-deep-read"]')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(sendMock).toHaveBeenCalledWith(
      buildDeepReadPriorArtPrompt(PATH),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: true },
    )
  })

  it('routes a dispatch failure into the research store error', async () => {
    sendMock.mockRejectedValue(new Error('runtime not ready'))
    const container = await render(<PriorArtRow path={PATH} count={3} />)

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[data-testid="research-prior-art-deep-read"]')!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(useResearchStore.getState().error).toContain('Failed to dispatch')
    expect(useResearchStore.getState().error).toContain('runtime not ready')
  })
})

describe('PriorArtRow — Open', () => {
  it('opens the raw catalog in the file viewer and uncollapses it', async () => {
    const container = await render(<PriorArtRow path={PATH} count={3} />)

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[data-testid="research-prior-art-open"]')!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    const viewer = useFileViewerStore.getState()
    expect(viewer.openTabs).toContain(PATH)
    expect(viewer.activeFile).toBe(PATH)
    expect(viewer.collapsed).toBe(false)
    // Open never dispatches the skill.
    expect(sendMock).not.toHaveBeenCalled()
  })
})
