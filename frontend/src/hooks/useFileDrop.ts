// Native OS drag-and-drop → attachment staging, active only in chat mode.
//
// The backend (desktop/startup.go) registers a Wails `OnFileDrop` listener that
// emits the global `files:dropped` event with `{ paths, x, y }` (absolute file
// paths + drop coordinates). This hook subscribes to it and forwards the dropped
// paths to the shared staging pipeline (useStageAttachments) — the SAME pipeline
// the 📎 picker and paste-file use — so vision gating, session-on-demand and
// store sync all behave identically across every entry point.
//
// Wails v2's native drop gives us only the final `drop` event (no
// drag-over/drag-leave from the OS), so the highlight overlay can't rely on the
// Wails path alone. We additionally listen to document-level HTML5
// `dragenter`/`dragover`/`dragleave`/`drop` events purely to drive a CSS
// highlight (preventDefault stops the webview from navigating to the file); the
// actual staging comes from the Wails `files:dropped` event, which is the only
// source that yields absolute paths.
//
// The hook is a no-op in terminal mode: dropping files into the terminal would
// be surprising, and the terminal panel is the active surface there.

import { useEffect, useRef, useState } from 'react'
import { onGlobalEvent } from '@/api/runtime'
import { isFilesDroppedData } from '@/types/events'
import { useStageAttachments } from '@/hooks/useStageAttachments'
import { useInputModeStore } from '@/stores/inputModeStore'

/**
 * Subscribe to native OS file drop and stage the dropped paths as attachments.
 *
 * @param activeSessionId active chat session id (null = the staging pipeline
 *   creates one on demand).
 * @returns dragActive — whether a file is currently being dragged over the
 *   window, for rendering a drop-zone highlight overlay. Always `false` in
 *   terminal mode.
 */
export function useFileDrop(activeSessionId: string | null): {
  dragActive: boolean
} {
  const mode = useInputModeStore((s) => s.mode)
  const { stageAttachmentPaths } = useStageAttachments()
  const [dragActive, setDragActive] = useState(false)
  // The HTML5 drag sequence fires many dragenter/dragleave events as the cursor
  // moves between child elements. We ref-count them (per hook instance) so the
  // overlay only hides once the cursor has truly left the window. Owned as a
  // ref rather than a module global so two mounts never share/borrow state.
  const dragEnterCount = useRef(0)

  useEffect(() => {
    if (mode !== 'chat') {
      // Reset state so a leftover highlight doesn't persist across a mode switch.
      dragEnterCount.current = 0
      setDragActive(false)
      return
    }

    // --- Stage dropped files from the Wails native drop event. ---
    const unsub = onGlobalEvent('files:dropped', (data) => {
      if (!isFilesDroppedData(data)) return
      if (data.paths.length === 0) return
      void stageAttachmentPaths(activeSessionId, [...data.paths])
    })

    // --- Drive the highlight overlay via document-level HTML5 drag events. ---
    // preventDefault on dragover is REQUIRED for the browser to fire `drop` and
    // to suppress its default "open/navigate to the file" behavior. We never
    // read the HTML5 dataTransfer here — paths come from the Wails event — so
    // this is purely visual + navigation-suppression.
    const resetDrag = () => {
      dragEnterCount.current = 0
      setDragActive(false)
    }

    const onDragEnter = (e: DragEvent) => {
      // Only react to drags carrying files.
      if (!hasFiles(e)) return
      e.preventDefault()
      dragEnterCount.current++
      setDragActive(true)
    }

    const onDragOver = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      // Keep the highlight steady while moving over the window. setState with
      // the same value is a no-op, so this is cheap and avoids a stale-closure
      // read of dragActive.
      setDragActive(true)
    }

    const onDragLeave = (e: DragEvent) => {
      if (!hasFiles(e)) return
      dragEnterCount.current = Math.max(0, dragEnterCount.current - 1)
      if (dragEnterCount.current === 0) setDragActive(false)
    }

    const onDrop = (e: DragEvent) => {
      // Suppress the webview's native "open file" navigation; the Wails
      // files:dropped event delivers the paths.
      e.preventDefault()
      resetDrag()
    }

    document.addEventListener('dragenter', onDragEnter)
    document.addEventListener('dragover', onDragOver)
    document.addEventListener('dragleave', onDragLeave)
    document.addEventListener('drop', onDrop)

    return () => {
      unsub()
      document.removeEventListener('dragenter', onDragEnter)
      document.removeEventListener('dragover', onDragOver)
      document.removeEventListener('dragleave', onDragLeave)
      document.removeEventListener('drop', onDrop)
      // Reset the ref counter so a re-mount (e.g. mode switch back to chat)
      // starts from a clean state.
      dragEnterCount.current = 0
    }
  }, [mode, activeSessionId, stageAttachmentPaths])

  return { dragActive }
}

/** A native drag carrying files exposes `files` on its dataTransfer (or
 *  `types` containing "Files"). Synthetic/drive-by drags (e.g. text) are
 *  ignored so the overlay only lights up for real file drops. */
function hasFiles(e: DragEvent): boolean {
  // dataTransfer can be null per spec for some synthetic events.
  const dt = e.dataTransfer
  if (!dt) return false
  if (dt.files && dt.files.length > 0) return true
  return Array.from(dt.types ?? []).includes('Files')
}
