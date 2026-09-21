// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { CommitSuppressedDialog } from './CommitSuppressedDialog'
import type { CommitSuppression } from '@/api/git'

let onTrust: () => void
let onForceCommit: (skipDialog: boolean) => void
let onCancel: () => void
let root: Root | null
let container: HTMLDivElement | null

function renderDialog(suppressed: CommitSuppression | null, isSubmitting = false) {
  if (!root) {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  }
  const r = root
  act(() => {
    r.render(
      <CommitSuppressedDialog
        suppressed={suppressed}
        repoPath="/ws/proj-a"
        isSubmitting={isSubmitting}
        onTrust={onTrust}
        onForceCommit={onForceCommit}
        onCancel={onCancel}
      />,
    )
  })
}

// Radix Dialog portals content into document.body.
function allButtons(): HTMLButtonElement[] {
  return Array.from(document.body.querySelectorAll('button'))
}

function buttonByText(text: string): HTMLButtonElement | undefined {
  return allButtons().find((b) => b.textContent?.includes(text))
}

function skipCheckbox(): HTMLInputElement {
  const el = document.body.querySelector('[data-testid="commit-suppressed-skip-checkbox"]')
  expect(el).toBeDefined()
  return el as HTMLInputElement
}

beforeEach(() => {
  onTrust = vi.fn<() => void>()
  onForceCommit = vi.fn<(skipDialog: boolean) => void>()
  onCancel = vi.fn<() => void>()
  document.body.innerHTML = ''
})

afterEach(() => {
  if (root) {
    const r = root
    act(() => {
      r.unmount()
    })
    root = null
  }
  if (container) container.remove()
  document.body.innerHTML = ''
})

describe('CommitSuppressedDialog', () => {
  it('renders nothing when there is no withheld commit', () => {
    renderDialog(null)
    expect(document.body.textContent).not.toContain('Commit withheld')
    expect(allButtons().length).toBe(0)
  })

  it('lists the armed hooks and signing flags with the hardening explanation', () => {
    renderDialog({
      hooks: ['pre-commit', 'commit-msg'],
      signing_repo: true,
      signing_global: false,
    })
    const text = document.body.textContent ?? ''
    expect(text).toContain('Commit withheld')
    expect(text).toContain('pre-commit')
    expect(text).toContain('commit-msg')
    expect(text).toContain('commit.gpgsign (repo config)')
    expect(text).not.toContain('commit.gpgsign (global config)')
    expect(text).toContain('hardened baseline')
    expect(text).toContain('/ws/proj-a')
    expect(text).toContain('Trust this repo')
    expect(text).toContain('Commit without hooks/signing')
  })

  it('shows signing entries when only signing is armed (no hooks listed)', () => {
    renderDialog({ hooks: [], signing_repo: false, signing_global: true })
    const text = document.body.textContent ?? ''
    expect(text).toContain('commit.gpgsign (global config)')
    expect(text).not.toContain('commit.gpgsign (repo config)')
  })

  it('calls onTrust for the Trust button', () => {
    renderDialog({ hooks: ['pre-commit'] })
    act(() => {
      buttonByText('Trust this repo & commit')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onTrust).toHaveBeenCalledTimes(1)
    expect(onForceCommit).not.toHaveBeenCalled()
  })

  it('calls onForceCommit(false) without the checkbox', () => {
    renderDialog({ hooks: ['pre-commit'] })
    act(() => {
      buttonByText('Commit without hooks/signing')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onForceCommit).toHaveBeenCalledWith(false)
  })

  it('calls onForceCommit(true) when "Don\'t ask again" is checked', () => {
    renderDialog({ hooks: ['pre-commit'] })
    act(() => {
      skipCheckbox().click()
    })
    expect(skipCheckbox().checked).toBe(true)
    act(() => {
      buttonByText('Commit without hooks/signing')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onForceCommit).toHaveBeenCalledWith(true)
  })

  it('the checkbox resets to unchecked for a fresh suppression', () => {
    renderDialog({ hooks: ['pre-commit'] })
    act(() => {
      skipCheckbox().click()
    })
    expect(skipCheckbox().checked).toBe(true)
    // A new withheld commit re-opens the dialog (suppressed prop changes).
    renderDialog({ hooks: ['commit-msg'] })
    expect(skipCheckbox().checked).toBe(false)
  })

  it('disables all actions while a dialog action is submitting', () => {
    renderDialog({ hooks: ['pre-commit'] }, true)
    for (const label of ['Trust this repo & commit', 'Commit without hooks/signing', 'Cancel']) {
      expect(buttonByText(label)!.disabled).toBe(true)
    }
    expect(skipCheckbox().disabled).toBe(true)
  })

  it('calls onCancel for the Cancel button', () => {
    renderDialog({ hooks: ['pre-commit'] })
    act(() => {
      buttonByText('Cancel')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onCancel).toHaveBeenCalledTimes(1)
  })
})
