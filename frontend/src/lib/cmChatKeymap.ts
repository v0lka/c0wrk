import { keymap } from '@codemirror/view'
import { completionStatus } from '@codemirror/autocomplete'
import type { Extension } from '@codemirror/state'
import type { MutableRefObject } from 'react'

/**
 * Chat-specific keymap:
 * - Enter sends the message (unless autocomplete is active)
 * - Shift-Enter inserts a newline
 */
export function createChatKeymap(onSendRef: MutableRefObject<(() => void) | null>): Extension {
  return keymap.of([
    {
      key: 'Enter',
      run: (view) => {
        if (completionStatus(view.state) !== null) return false
        onSendRef.current?.()
        return true
      },
    },
    {
      key: 'Shift-Enter',
      run: (view) => {
        view.dispatch(view.state.replaceSelection('\n'))
        return true
      },
    },
  ])
}
