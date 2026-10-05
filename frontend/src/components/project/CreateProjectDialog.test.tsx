// @vitest-environment jsdom
//
// Invariant under test: the directory picker returns an OS-native path. When a
// Windows directory is picked (`C:\Users\me\proj`) the auto-filled project name
// must be the final segment, not the whole path — a plain split('/') leaves the
// entire string in the field. The picked path is additionally rendered with the
// `truncate` overflow guard so long paths never stretch the dialog.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { pickDirectoryMock, createProjectMock } = vi.hoisted(() => ({
  pickDirectoryMock: vi.fn<() => Promise<string>>(),
  createProjectMock: vi.fn<(name: string, path: string) => Promise<{ id: string }>>(),
}))

vi.mock('@/api/projects', () => ({
  pickDirectory: pickDirectoryMock,
  createProject: createProjectMock,
}))

vi.mock('@/hooks/useProjectSwitchState', () => ({
  useProjectSwitchState: () => vi.fn(async () => {}),
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { CreateProjectDialog } from './CreateProjectDialog'

// Radix portals the dialog content to document.body, so queries target the
// whole document rather than the render container.
const dialogEl = () => document.querySelector<HTMLElement>('[role="dialog"]')
const findButton = (text: string) =>
  Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.includes(text))
const nameInput = () => dialogEl()?.querySelector('input') ?? null

describe('CreateProjectDialog', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    pickDirectoryMock.mockReset()
    createProjectMock.mockReset()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  const renderOpen = () =>
    act(async () => {
      root.render(<CreateProjectDialog open onOpenChange={() => {}} />)
    })

  it('derives the project name from the final segment of a Windows path', async () => {
    pickDirectoryMock.mockResolvedValue('C:\\Users\\tomsk\\Downloads\\imap_chrome_extension')
    await renderOpen()

    await act(async () => {
      findButton('Choose directory')?.click()
    })

    expect((nameInput() as HTMLInputElement | null)?.value).toBe('imap_chrome_extension')
    // The full path stays visible (ellipsized) and is exposed via the title.
    // The directory button also carries a title, so target the path display
    // paragraph explicitly rather than the first [title] in the dialog.
    expect(dialogEl()?.querySelector('p[title]')?.getAttribute('title')).toBe(
      'C:\\Users\\tomsk\\Downloads\\imap_chrome_extension',
    )
  })

  it('derives the project name from the final segment of a POSIX path', async () => {
    pickDirectoryMock.mockResolvedValue('/Users/dev/projects/my-app')
    await renderOpen()

    await act(async () => {
      findButton('Choose directory')?.click()
    })

    expect((nameInput() as HTMLInputElement | null)?.value).toBe('my-app')
  })

  it('does not overwrite a name the user already typed', async () => {
    pickDirectoryMock.mockResolvedValue('/Users/dev/projects/my-app')
    await renderOpen()

    const input = nameInput() as HTMLInputElement
    await act(async () => {
      // React tracks the DOM value via a native setter, so drive it directly.
      const setValue = Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        'value',
      )?.set
      setValue?.call(input, 'custom-name')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })

    await act(async () => {
      findButton('Choose directory')?.click()
    })

    expect((nameInput() as HTMLInputElement | null)?.value).toBe('custom-name')
  })

  it('renders the picked path with the truncate overflow guard', async () => {
    pickDirectoryMock.mockResolvedValue('C:\\Users\\tomsk\\Downloads\\imap_chrome_extension')
    await renderOpen()

    await act(async () => {
      findButton('Choose directory')?.click()
    })

    expect(dialogEl()?.querySelector('p.truncate')).not.toBeNull()
  })
})
