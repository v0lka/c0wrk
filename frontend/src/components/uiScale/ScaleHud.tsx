// Scale HUD — the transient zoom indicator shown while the user is
// modifier+wheel-stepping the app UI scale (Ctrl, or Cmd on macOS — see
// useCtrlWheelScale).
//
// A small centered card over the app (Reset to 100% + a live slider + the
// current value). The full-screen wrapper swallows no input — it is
// pointer-events-none and only the card itself re-enables pointer events —
// so the rest of the UI stays interactive while the HUD is up; hiding is
// driven by the hook's timers (paused while the card is grabbed, so a slider
// drag can outlast the timer budget, re-armed on every slider value change
// so keyboard steps keep it up), never by clicking away.
//
// The grab releases through window-level pointerup/pointercancel listeners
// held for the grab's duration: the mouse has no implicit pointer capture,
// so a press that starts on the card but releases outside it delivers
// neither event to the card — card-level listeners alone would leave the
// HUD pinned on screen forever. (Radix's slider captures the pointer
// itself, so its drags are doubly covered.)
//
// Mounted once at the App root in every app phase: a zoom-chord wheel
// gesture is meaningful on the splash screens too (the zoom is a
// <html>-level style and survives phase transitions), and the hook keeps the
// HUD hidden outside a desktop (wails) session.

import { useEffect, useState } from 'react'
import { RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Slider } from '@/components/ui/slider'
import { useCtrlWheelScale } from '@/hooks/useCtrlWheelScale'
import { SLIDER_STEP_PERCENT } from '@/lib/uiScaleSteps'
import { useUiScaleStore, UI_SCALE_MIN, UI_SCALE_MAX } from '@/stores/uiScaleStore'

/** Scale the reset button restores — the app default. */
const DEFAULT_SCALE = 100

/**
 * The transient UI-scale HUD. Renders nothing while
 * {@link useCtrlWheelScale} reports the gesture finished (hidden is the
 * steady state — this component owns no persistent UI).
 */
export function ScaleHud() {
  const { visible, hold, release, rearm } = useCtrlWheelScale()
  // Primitive field selectors: `scale` changes on every wheel notch, while
  // `setScale` is a stable store action — neither selector allocates, so the
  // re-render per step stays cheap (stable-selector principle).
  const scale = useUiScaleStore((s) => s.scale)
  const setScale = useUiScaleStore((s) => s.setScale)
  // True from a card pointerdown until a pointerup/pointercancel lands
  // anywhere — drives the window-level release listeners in the effect.
  const [grabbing, setGrabbing] = useState(false)

  // The release half of the grab (see the file comment): a press that
  // started on the card but ends outside it delivers pointerup to whatever
  // element sits under the pointer — never to the card — so the listeners
  // ride on the window for exactly the grab's duration. The effect cleanup
  // removes them when the grab ends or the HUD unmounts mid-grab.
  useEffect(() => {
    if (!grabbing) return
    const endGrab = (): void => {
      setGrabbing(false)
      release()
    }
    window.addEventListener('pointerup', endGrab)
    window.addEventListener('pointercancel', endGrab)
    return () => {
      window.removeEventListener('pointerup', endGrab)
      window.removeEventListener('pointercancel', endGrab)
    }
  }, [grabbing, release])

  if (!visible) return null

  return (
    // The wrapper is a passive overlay: it positions the card and stacks it
    // above the app (z-[100]) but lets every click/wheel through — blocking
    // the UI while the user is still reading it would fight the gesture.
    <div className="fixed inset-0 z-[100] flex items-center justify-center pointer-events-none">
      {/* Grabbing the card pauses the hide timer (hook hold/release): a
          slider drag outlasts the 1 s budget. The release is handled by the
          window-level listeners in the effect above — it must land wherever
          the pointer comes up, not only over the card. */}
      <div
        className="pointer-events-auto flex items-center gap-3 rounded-md border border-border bg-popover text-popover-foreground px-3 py-2 shadow-[0_4px_12px_var(--color-shadow)]"
        onPointerDown={() => {
          setGrabbing(true)
          hold()
        }}
      >
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setScale(DEFAULT_SCALE)}
          aria-label="Reset UI scale to 100%"
          title="Reset UI scale to 100%"
        >
          <RotateCcw className="size-4" />
        </Button>
        {/*
          Live slider: the self-styled Radix primitive (components/ui/slider)
          so the control looks identical on every platform's webview — the
          native range input rendered GTK/WebKit/Chromium sliders that
          differed per OS. Every change lands in setScale (which normalizes,
          applies the <html> zoom and persists) — there is no apply/save
          step, matching the Settings → Appearance selector's behavior.
          Radix's value/onValueChange are array-shaped; the store value is a
          plain number, hence the [scale] / v[0] mapping. Keyboard steps and
          pointer drag both honor the 5% step grid. Every change also re-arms
          the hide (rearm) — a keyboard step on a focused thumb carries no
          pointer events, so without it the HUD (thumb included) would hide
          one delay after the last zoom chord no matter how the user steps.
        */}
        <Slider
          className="w-40 cursor-pointer"
          min={UI_SCALE_MIN}
          max={UI_SCALE_MAX}
          step={SLIDER_STEP_PERCENT}
          value={[scale]}
          onValueChange={(v) => {
            setScale(v[0] ?? scale)
            rearm()
          }}
          thumb={{
            // The thumb is the announced role=slider element — the accessible
            // name and the % unit live there; the root span is not focusable,
            // so a label on it is never announced.
            'aria-label': 'UI scale',
            'aria-valuetext': `${scale}%`,
          }}
        />
        <span className="w-10 text-right text-xs tabular-nums text-muted-foreground" aria-hidden="true">
          {scale}%
        </span>
      </div>
    </div>
  )
}
