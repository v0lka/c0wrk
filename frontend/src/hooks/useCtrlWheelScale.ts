import { useEffect, useState } from 'react'
import { transportSnapshot } from '@/api/runtime'
import { nextScaleOnWheel } from '@/lib/uiScaleSteps'
import { useUiScaleStore } from '@/stores/uiScaleStore'

/** How long the HUD stays visible after Ctrl is released (or the window blurs). */
const HUD_HIDE_DELAY_MS = 1000

/**
 * useCtrlWheelScale — desktop-only Ctrl+wheel app-zoom stepping with a
 * transient HUD visibility flag.
 *
 * While the HUD is hidden this hook owns no UI; it only listens for the
 * Ctrl+wheel chord on the window (capture phase, non-passive) and:
 *   - steps the persisted UI scale through `nextScaleOnWheel` (snapped to the
 *     wheel grid, direction from the deltaY sign only, clamped by the store),
 *   - shows the HUD and keeps it visible for `HUD_HIDE_DELAY_MS` after the
 *     last signal (keyup Control or window blur). A fresh Ctrl+wheel cancels
 *     a pending hide, and a keydown Control cancels it too — holding Ctrl
 *     through a wheel burst never lets the HUD flicker away mid-gesture.
 *
 * `preventDefault` runs only for Ctrl+wheel (the browser's own page-zoom
 * chord); plain wheel events are ignored untouched so scrolling stays native.
 *
 * The listener set attaches once per mount and is gated on
 * `transportSnapshot() === 'wails'`: in the plain dev-server browser, on the
 * web-access sidecar and in tests ctrl+wheel keeps its native meaning, so no
 * listener is registered at all. The scale is read via
 * `useUiScaleStore.getState()` inside the handlers (not React state) so the
 * effect never re-attaches when the scale changes.
 *
 * Cleanup removes every listener and cancels a pending hide timer, so the
 * StrictMode mount → unmount → remount cycle leaves nothing behind.
 */
export function useCtrlWheelScale(): { visible: boolean } {
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    if (transportSnapshot() !== 'wails') return

    // Synchronous mirror of `visible` for the event handlers — they run
    // outside React and must not depend on a closure that a re-render could
    // make stale (the listeners are attached once per mount).
    let hudVisible = false
    let hideTimer: ReturnType<typeof setTimeout> | null = null

    const cancelHide = (): void => {
      if (hideTimer !== null) {
        clearTimeout(hideTimer)
        hideTimer = null
      }
    }

    const scheduleHide = (): void => {
      if (!hudVisible) return
      cancelHide()
      hideTimer = setTimeout(() => {
        hideTimer = null
        hudVisible = false
        setVisible(false)
      }, HUD_HIDE_DELAY_MS)
    }

    const showHud = (): void => {
      cancelHide()
      if (!hudVisible) {
        hudVisible = true
        setVisible(true)
      }
    }

    const onWheel = (event: WheelEvent): void => {
      if (!event.ctrlKey) return
      // Ctrl+wheel is the browser's page-zoom chord: swallow it so the
      // viewport never zooms while we step the app scale instead.
      event.preventDefault()
      showHud()
      const scale = useUiScaleStore.getState().scale
      useUiScaleStore.getState().setScale(nextScaleOnWheel(scale, event.deltaY))
    }

    const onKeyDown = (event: KeyboardEvent): void => {
      // Ctrl still held while the HUD is up — the user keeps zooming, so a
      // pending hide must not fire mid-gesture.
      if (event.key === 'Control' && hudVisible) cancelHide()
    }

    const onKeyUp = (event: KeyboardEvent): void => {
      if (event.key === 'Control') scheduleHide()
    }

    const onBlur = (): void => {
      // Releasing Ctrl after the window lost focus produces no keyup
      // (useShiftHeld precedent) — route blur through the same timed hide.
      scheduleHide()
    }

    // Capture phase: the chord must be cancelled before it reaches the
    // document (and any canvas-level wheel handlers); non-passive so
    // preventDefault is honored.
    window.addEventListener('wheel', onWheel, { capture: true, passive: false })
    window.addEventListener('keydown', onKeyDown)
    window.addEventListener('keyup', onKeyUp)
    window.addEventListener('blur', onBlur)

    return () => {
      window.removeEventListener('wheel', onWheel, { capture: true })
      window.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('keyup', onKeyUp)
      window.removeEventListener('blur', onBlur)
      cancelHide()
    }
  }, [])

  return { visible }
}
