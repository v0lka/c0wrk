// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { GitFileEntry } from './GitFileEntry'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

const LONG_PATH =
  '/repo/src/deeply/nested/directory/structure/with-a-very-long-file-name.ts'

function makeEntry(overrides: Partial<GitPanelEntry> = {}): GitPanelEntry {
  return {
    path: LONG_PATH,
    status: 'M',
    staged: false,
    diffStat: null,
    indexStatus: ' ',
    worktreeStatus: 'M',
    ...overrides,
  }
}

function render(ui: React.ReactNode): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => {
    root.render(ui)
  })
  return container
}

/** The truncating span that renders the file name. */
function nameSpan(container: HTMLElement): HTMLSpanElement {
  return container.querySelector('span.truncate') as HTMLSpanElement
}

const noop = () => {}

describe('GitFileEntry', () => {
  it('carries the workspace-relative display path as the name tooltip', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry()}
        workspaceRoot="/repo"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(
      'src/deeply/nested/directory/structure/with-a-very-long-file-name.ts',
    )
  })

  it('falls back to the raw path in the tooltip when no workspace root is known', () => {
    const container = render(
      <GitFileEntry entry={makeEntry()} onToggle={noop} onOpenDiff={noop} />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(LONG_PATH)
  })

  it('keeps the raw path in the tooltip when it lies outside the workspace root', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry()}
        workspaceRoot="/other-repo"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(LONG_PATH)
  })
})
