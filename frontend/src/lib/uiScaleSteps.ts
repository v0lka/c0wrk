import { UI_SCALE_MAX, UI_SCALE_MIN } from '@/stores/uiScaleStore'

/** Percent increment applied per wheel notch over the UI scale. */
export const WHEEL_STEP_PERCENT = 10

/** Percent increment applied per slider/keyboard step over the UI scale. */
export const SLIDER_STEP_PERCENT = 5

/**
 * roundScaleToStep snaps `value` to the nearest multiple of `step`.
 *
 * Uses Math.round, so an exact halfway point rounds up (toward +Infinity) —
 * e.g. 195 snaps to 200, 105 snaps to 110 on a 10-step grid. This keeps
 * repeated wheel notches walking a deterministic grid instead of drifting.
 */
export function roundScaleToStep(value: number, step: number): number {
  return Math.round(value / step) * step
}

/**
 * nextScaleOnWheel maps one wheel notch over the UI scale onto the next value.
 *
 * `deltaY > 0` (wheel down) DECREASES the scale; `deltaY < 0` (wheel up)
 * increases it — scrolling down reads as "zoom out". Only the sign of
 * `deltaY` matters: trackpads emit wildly varying magnitudes for the same
 * notch, so magnitude must never scale the step.
 *
 * The current scale is first snapped to the nearest {@link WHEEL_STEP_PERCENT}
 * multiple (see {@link roundScaleToStep}), then one step is applied in the
 * wheel's direction and the result is clamped to the store's supported range
 * [UI_SCALE_MIN, UI_SCALE_MAX].
 */
export function nextScaleOnWheel(current: number, deltaY: number): number {
  const base = roundScaleToStep(current, WHEEL_STEP_PERCENT)
  const direction = deltaY > 0 ? -1 : 1
  const next = base + direction * WHEEL_STEP_PERCENT
  return Math.min(UI_SCALE_MAX, Math.max(UI_SCALE_MIN, next))
}
