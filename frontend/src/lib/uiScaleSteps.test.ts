import { describe, expect, it } from 'vitest'
import { UI_SCALE_MAX, UI_SCALE_MIN } from '@/stores/uiScaleStore'
import {
  SLIDER_STEP_PERCENT,
  WHEEL_STEP_PERCENT,
  nextScaleOnWheel,
  roundScaleToStep,
} from './uiScaleSteps'

describe('uiScaleSteps constants', () => {
  it('exposes the wheel and slider step sizes in percent', () => {
    expect(WHEEL_STEP_PERCENT).toBe(10)
    expect(SLIDER_STEP_PERCENT).toBe(5)
  })
})

describe('roundScaleToStep', () => {
  it('rounds to the nearest multiple of the step', () => {
    expect(roundScaleToStep(123, 10)).toBe(120)
    expect(roundScaleToStep(127, 10)).toBe(130)
    expect(roundScaleToStep(103, SLIDER_STEP_PERCENT)).toBe(105)
    expect(roundScaleToStep(102, SLIDER_STEP_PERCENT)).toBe(100)
  })

  it('rounds an exact halfway point up', () => {
    expect(roundScaleToStep(195, 10)).toBe(200)
    expect(roundScaleToStep(105, 10)).toBe(110)
    expect(roundScaleToStep(27.5, SLIDER_STEP_PERCENT)).toBe(30)
  })

  it('leaves exact multiples unchanged', () => {
    expect(roundScaleToStep(100, 10)).toBe(100)
    expect(roundScaleToStep(75, SLIDER_STEP_PERCENT)).toBe(75)
  })
})

describe('nextScaleOnWheel', () => {
  it('increases by 10 on wheel up', () => {
    expect(nextScaleOnWheel(100, -1)).toBe(110)
  })

  it('uses only the sign of deltaY, not its magnitude', () => {
    expect(nextScaleOnWheel(100, -120)).toBe(110)
    expect(nextScaleOnWheel(100, 40)).toBe(90)
  })

  it('snaps the current scale to the grid before stepping down', () => {
    // 123 snaps to 120, then one wheel-down step subtracts 10.
    expect(nextScaleOnWheel(123, 1)).toBe(110)
  })

  it('rounds a halfway value up before stepping down', () => {
    // 195 snaps up to 200, then one wheel-down step lands on 190.
    expect(nextScaleOnWheel(195, 1)).toBe(190)
  })

  it('clamps to UI_SCALE_MAX on wheel up near the top', () => {
    expect(nextScaleOnWheel(195, -1)).toBe(UI_SCALE_MAX)
    expect(nextScaleOnWheel(UI_SCALE_MAX, -1)).toBe(UI_SCALE_MAX)
  })

  it('clamps to UI_SCALE_MIN on wheel down at the bottom', () => {
    expect(nextScaleOnWheel(UI_SCALE_MIN, 1)).toBe(UI_SCALE_MIN)
    expect(nextScaleOnWheel(50, 1)).toBe(50)
  })

  it('never leaves the supported range for in-range inputs', () => {
    expect(UI_SCALE_MIN).toBe(50)
    expect(UI_SCALE_MAX).toBe(200)
    expect(nextScaleOnWheel(50, -1)).toBe(60)
    expect(nextScaleOnWheel(200, 1)).toBe(190)
  })
})
