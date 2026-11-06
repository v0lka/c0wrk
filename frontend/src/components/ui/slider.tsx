// Slider primitive — a Radix-based, self-styled range slider (shadcn/new-york
// shape). Radix renders every part with role/ARIA plumbing out of the box
// (the thumb is the focusable div[role=slider] carrying the value ARIA, Arrow
// keys and Home/End step it), so this file only supplies the visuals. All
// colors go through Tailwind design tokens and all lengths are absolute
// px/rem — both intentionally scale with the app-wide UI Scale zoom, and no
// viewport unit is used anywhere (the zoomViewportInvariant guard). The thumb
// is positioned by Radix with a percentage `left` + `translate(-50%)` —
// percentages are zoom-safe by the same rule the spec records for the Scale
// HUD centering.

import * as React from "react"
import { Slider as SliderPrimitive } from "radix-ui"

import { cn } from "@/lib/utils"

/**
 * Normalize a scalar `value`/`defaultValue` to the array shape Radix's slider
 * context requires — the primitive crashes on a scalar (`values.map`).
 */
const asRange = (v: number | number[] | undefined): number[] | undefined =>
  v === undefined ? undefined : Array.isArray(v) ? v : [v]

type SliderProps = Omit<React.ComponentProps<typeof SliderPrimitive.Root>, 'value' | 'defaultValue'> & {
  /**
   * Props spread onto the thumb — the announced element (role=slider).
   * `className` merges after the built-in visuals instead of replacing them.
   */
  thumb?: React.ComponentProps<typeof SliderPrimitive.Thumb>
  /** Scalar accepted; normalized to Radix's array shape. */
  value?: number | number[]
  /** Scalar accepted; normalized to Radix's array shape. */
  defaultValue?: number | number[]
}

function Slider({ className, thumb: thumbProps, value, defaultValue, ...props }: SliderProps) {
  const valueRange = asRange(value)
  const defaultValueRange = asRange(defaultValue)
  const thumbCount = (valueRange ?? defaultValueRange)?.length ?? 1

  return (
    <SliderPrimitive.Root
      data-slot="slider"
      className={cn(
        "relative flex w-full touch-none select-none items-center",
        "data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        className
      )}
      {...props}
      value={valueRange}
      defaultValue={defaultValueRange}
    >
      <SliderPrimitive.Track
        data-slot="slider-track"
        className="relative h-1 w-full grow overflow-hidden rounded-full bg-secondary"
      >
        <SliderPrimitive.Range
          data-slot="slider-range"
          className="absolute h-full bg-primary"
        />
      </SliderPrimitive.Track>
      {Array.from({ length: thumbCount }, (_, index) => (
        <SliderPrimitive.Thumb
          key={index}
          {...thumbProps}
          data-slot="slider-thumb"
          className={cn(
            "block size-4 shrink-0 rounded-full border border-primary bg-background shadow-sm outline-none transition-[color,box-shadow] focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-offset-1 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50",
            thumbProps?.className
          )}
        />
      ))}
    </SliderPrimitive.Root>
  )
}

export { Slider }
