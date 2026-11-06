import { useCallback, useEffect, useRef, useState } from 'react'
import { transportSnapshot } from '@/api/runtime'
import { isMacHost } from '@/lib/platform'
import { nextScaleOnWheel } from '@/lib/uiScaleSteps'
import { useUiScaleStore } from '@/stores/uiScaleStore'

/** How long the HUD stays visible after the zoom modifier is released (or the window blurs). */
const HUD_HIDE_DELAY_MS = 1000

/**
 * useCtrlWheelScale — desktop-only modifier+wheel AND modifier+keyboard
 * app-zoom stepping with a transient HUD visibility flag. The modifier
 * follows the platform's zoom convention: Cmd on macOS (`isMacHost`), Ctrl
 * on Windows/Linux. On macOS plain Ctrl is accepted too, because WebKit
 * reports trackpad pinch wheels with the synthetic `ctrlKey` flag — the
 * same gesture family.
 *
 * While the HUD is hidden this hook owns no UI; it listens on the window
 * (capture phase, non-passive where needed) for the modifier+wheel chord
 * and the modifier +/-/− keyboard chord, and:
 *   - steps the persisted UI scale through `nextScaleOnWheel` (snapped to the
 *     wheel grid, direction from the deltaY sign or the +/- key, clamped by
 *     the store),
 *   - shows the HUD and re-arms the hide timer on every zoom signal, so it
 *     stays visible for `HUD_HIDE_DELAY_MS` after the last chord, the
 *     modifier keyup or a window blur. Holding the modifier keeps the HUD up
 *     via its auto-repeating keydowns (each cancels a pending hide), which
 *     also covers gestures with no keyup at all — a trackpad pinch fires
 *     ctrlKey wheels without any key ever being held, and the HUD hides one
 *     delay after the last wheel notch or zoom keydown (a held Ctrl+'+'
 *     repeats keydowns that each re-arm the hide and step the scale),
 *   - pauses the hide entirely while the user is grabbing the HUD card
 *     (`hold` from a card `pointerdown`, `release` on `pointerup`/`pointercancel`):
 *     dragging the slider outlasts any single timer budget, so while held a
 *     pending hide is cancelled and cannot re-arm.
 *
 * A chord wheel with `deltaY === 0` (a deltaX-only trackpad gesture or an
 * inertial tail — the same zero-delta wheels `usePanZoom` guards) carries no
 * zoom direction and is ignored completely untouched.
 *
 * `preventDefault` + `stopPropagation` run only for the zoom chord (the
 * browser's own page-zoom chord: Ctrl+wheel, or Cmd+wheel on macOS):
 * `preventDefault` keeps the viewport from zooming, `stopPropagation` keeps
 * the chord from reaching canvas-level wheel handlers (`usePanZoom` would
 * zoom the diagram on the same gesture) — under the chord the wheel means
 * app zoom everywhere. Plain wheel events are ignored untouched so scrolling
 * stays native.
 *
 * The keyboard chord (Ctrl+'+' / Ctrl+'−', or Cmd+/- on macOS — plain Ctrl
 * accepted on mac like the wheel) is the platform webview's own page-zoom
 * shortcut too, so it is prevented the same way. The zoom keys are matched
 * on `event.key` first ('+', '=' for the unshifted plus key, '-' and the
 * U+2212 minus) with an `event.code` fallback (`NumpadAdd`,
 * `NumpadSubtract`, `Equal`, `Minus`) so the chord works on any keyboard
 * layout and on the numpad. AltGr is never a zoom modifier: a
 * `ctrlKey+altKey` keydown is left untouched — AltGr is the system's
 * Ctrl+Alt, and character production on AltGr layouts must keep working
 * (the cmChatKeymap AltGr precedent). No `stopPropagation` on keydown:
 * wheel handlers don't exist for keyboard input, and other keydown
 * listeners (menus, inputs) must keep seeing the event.
 *
 * The listener set attaches once per mount and is gated on
 * `transportSnapshot() === 'wails'`: in the plain dev-server browser, on the
 * web-access sidecar and in tests modifier+wheel keeps its native meaning,
 * so no listener is registered at all. The scale is read via
 * `useUiScaleStore.getState()` inside the handlers (not React state), and
 * the visibility/timer/hold bookkeeping lives in refs behind stable
 * callbacks, so the effect never re-attaches when the scale changes.
 *
 * Cleanup removes every listener and cancels a pending hide timer, so the
 * StrictMode mount → unmount → remount cycle leaves nothing behind.
 */
export function useCtrlWheelScale(): {
  visible: boolean
  hold: () => void
  release: () => void
} {
  const [visible, setVisible] = useState(false)
  // Synchronous mirrors for the event handlers — they run outside React and
  // must not depend on a closure that a re-render could make stale (the
  // listeners are attached once per mount). The callbacks below are
  // referentially stable (refs only), so listing them in the effect deps
  // never re-attaches the listeners.
  const hudVisibleRef = useRef(false)
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // True while the user holds the HUD card: the hide is paused.
  const heldRef = useRef(false)

  const cancelHide = useCallback((): void => {
    if (hideTimerRef.current !== null) {
      clearTimeout(hideTimerRef.current)
      hideTimerRef.current = null
    }
  }, [])

  const scheduleHide = useCallback((): void => {
    if (!hudVisibleRef.current || heldRef.current) return
    cancelHide()
    hideTimerRef.current = setTimeout(() => {
      hideTimerRef.current = null
      hudVisibleRef.current = false
      setVisible(false)
    }, HUD_HIDE_DELAY_MS)
  }, [cancelHide])

  const showHud = useCallback((): void => {
    cancelHide()
    if (!hudVisibleRef.current) {
      hudVisibleRef.current = true
      setVisible(true)
    }
  }, [cancelHide])

  /** Card pointerdown: pause the hide while the user works the card. */
  const hold = useCallback((): void => {
    if (!hudVisibleRef.current) return
    heldRef.current = true
    cancelHide()
  }, [cancelHide])

  /** Card pointerup/pointercancel: re-arm the hide. */
  const release = useCallback((): void => {
    if (!heldRef.current) return
    heldRef.current = false
    scheduleHide()
  }, [scheduleHide])

  useEffect(() => {
    if (transportSnapshot() !== 'wails') return

    // Per-mount bookkeeping reset: a StrictMode remount reuses the refs of
    // the same fiber, so the steady state must start hidden and unheld.
    hudVisibleRef.current = false
    hideTimerRef.current = null
    heldRef.current = false

    // Platform zoom modifier: Cmd on macOS, Ctrl elsewhere. Captured once
    // per mount — the host platform cannot change mid-run.
    const mac = isMacHost()
    const hasZoomModifier = (event: WheelEvent): boolean =>
      mac ? event.metaKey || event.ctrlKey : event.ctrlKey

    /**
     * Zoom direction from a modifier+key keydown: +1 (zoom in) for the plus
     * keys, −1 (zoom out) for the minus keys, 0 for anything else. Matched
     * on `event.key` first ('+', '=' — the unshifted printed-plus key, '-'
     * and the U+2212 true-minus) with an `event.code` fallback
     * (`NumpadAdd`, `NumpadSubtract`, `Equal`, `Minus`) so the chord works
     * on any keyboard layout and on the numpad. Pure and module-local so
     * the keydown handler stays readable.
     */
    const zoomKeyDirection = (event: KeyboardEvent): number => {
      const key = event.key
      const code = event.code
      if (key === '+' || key === '=' || key === '\u2212' || code === 'NumpadAdd' || code === 'Equal') {
        return 1
      }
      if (key === '-' || code === 'NumpadSubtract' || code === 'Minus') {
        return -1
      }
      return 0
    }

    /** True while a zoom modifier chord is held (mirrors hasZoomModifier for keys). */
    const hasZoomModifierKey = (event: KeyboardEvent): boolean =>
      mac ? (event.metaKey || event.ctrlKey) && !event.altKey : event.ctrlKey && !event.altKey

    const stepScale = (deltaY: number): void => {
      const scale = useUiScaleStore.getState().scale
      useUiScaleStore.getState().setScale(nextScaleOnWheel(scale, deltaY))
    }

    const onWheel = (event: WheelEvent): void => {
      if (!hasZoomModifier(event)) return
      // No vertical input — no zoom direction: leave the event untouched.
      if (event.deltaY === 0) return
      // The zoom chord is the browser's page-zoom gesture (Ctrl+wheel, or
      // Cmd+wheel on macOS): swallow it entirely — see the docblock. Re-arm
      // the hide on every notch — a held physical modifier keeps canceling
      // via its auto-repeating keydowns, and a pinch (synthetic ctrlKey, no
      // keyup) hides after the last one.
      event.preventDefault()
      event.stopPropagation()
      showHud()
      scheduleHide()
      stepScale(event.deltaY)
    }

    const onKeyDown = (event: KeyboardEvent): void => {
      // The zoom modifier is still held while the HUD is up (auto-repeat
      // fires continuously) — the user keeps zooming, so a pending hide must
      // not fire mid-gesture.
      if ((event.key === 'Control' || event.key === 'Meta') && hudVisibleRef.current) {
        cancelHide()
        return
      }
      // The keyboard zoom chord (Ctrl+'+' / Ctrl+'−', Cmd on macOS): the
      // platform webview's own page-zoom shortcut — prevent the native zoom
      // and step the app scale instead. An AltGr keydown (Ctrl+Alt, the
      // system's AltGr) is never a zoom chord — character production on
      // AltGr layouts must keep working. Auto-repeat (a held Ctrl+'+')
      // steps on every repeat and re-arms the hide, mirroring a wheel burst.
      const direction = hasZoomModifierKey(event) ? zoomKeyDirection(event) : 0
      if (direction === 0) return
      event.preventDefault()
      showHud()
      scheduleHide()
      // nextScaleOnWheel's deltaY is wheel-signed (down = zoom out): a zoom-in
      // keypress (+1) maps to deltaY = -1.
      stepScale(-direction)
    }

    const onKeyUp = (event: KeyboardEvent): void => {
      if (event.key === 'Control' || event.key === 'Meta') scheduleHide()
    }

    const onBlur = (): void => {
      // Releasing the modifier after the window lost focus produces no keyup
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
  }, [cancelHide, showHud, scheduleHide])

  return { visible, hold, release }
}
