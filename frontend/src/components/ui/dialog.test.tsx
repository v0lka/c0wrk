// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act, type CSSProperties } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

function renderDialog(contentStyle?: Partial<CSSProperties>) {
  act(() => {
    root.render(
      <Dialog open onOpenChange={() => {}}>
        <DialogContent style={contentStyle}>
          <DialogTitle>Test dialog</DialogTitle>
          {/* Without a Description Radix warns on every render (see
              combobox.test.tsx for the same harness note). */}
          <DialogDescription>Test harness dialog</DialogDescription>
        </DialogContent>
      </Dialog>,
    )
  })
  const dialogContent = document.body.querySelector<HTMLElement>('[data-slot="dialog-content"]')
  expect(dialogContent).not.toBeNull()
  return dialogContent!
}

describe('DialogContent', () => {
  it('carries overflow-wrap anywhere so an unbreakable token cannot push the footer past the dialog edge', () => {
    const dialogContent = renderDialog()
    expect(dialogContent.style.overflowWrap).toBe('anywhere')
  })

  it('merges caller style over the default instead of dropping it', () => {
    const dialogContent = renderDialog({ padding: '12px' })
    expect(dialogContent.style.overflowWrap).toBe('anywhere')
    expect(dialogContent.style.padding).toBe('12px')
  })

  it('lets caller style override the default wrap behavior explicitly', () => {
    const dialogContent = renderDialog({ overflowWrap: 'normal' })
    expect(dialogContent.style.overflowWrap).toBe('normal')
  })
})
