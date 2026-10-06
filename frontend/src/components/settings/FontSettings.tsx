import { useCallback, useEffect, useMemo, useState, type CSSProperties } from 'react'
import { Button } from '@/components/ui/button'
import { StringCombobox } from '@/components/ui/StringCombobox'
import { listFontFamilies } from '@/api/fonts'
import {
  composeFontFamily,
  FONT_MONO_STACK,
  FONT_SANS_STACK,
  stripUnsafeFontNameChars,
} from '@/lib/fonts'
import {
  useFontStore,
  smoothingToCss,
  type FontSmoothingSetting,
} from '@/stores/fontStore'

/**
 * Sentinel option standing for "c0wrk's default stack" — the store's `null`.
 * No real font family is named exactly this; the sentinel never leaves this
 * component (it is mapped back to `null` before it reaches the store).
 */
const DEFAULT_OPTION = 'Default'

/**
 * Font smoothing (anti-aliasing) choices, offered in this order. The labels
 * are the committed combobox strings (`StringCombobox` options are bare
 * strings); they map onto the store's `FontSmoothingSetting` here and never
 * leak outward. "Not set" is the `''` default — the webview's own rendering
 * stands, nothing is written to CSS. The select-only combobox (editable=
 * false) drives these: free text is meaningless for an enum.
 */
const SMOOTHING_CHOICES: ReadonlyArray<{ label: string; value: FontSmoothingSetting }> = [
  { label: 'Not set', value: '' },
  { label: 'None (aliased)', value: 'none' },
  { label: 'Grayscale', value: 'grayscale' },
  { label: 'Subpixel (LCD)', value: 'subpixel' },
]

const SMOOTHING_LABELS: readonly string[] = SMOOTHING_CHOICES.map((c) => c.label)

/** Store value → dropdown label; an unrecognized value reads as "Not set". */
function smoothingLabel(value: FontSmoothingSetting): string {
  return SMOOTHING_CHOICES.find((c) => c.value === value)?.label ?? 'Not set'
}

/**
 * Typed-text normalization mirroring `fontStore`'s `normalizeFamily`: the
 * shared unsafe character set stripped (quotes, backslashes, control
 * characters — the same `stripUnsafeFontNameChars` helper the store and
 * api/fonts run, so pasted input is normalized identically at every
 * boundary) and trimmed. An input that carries nothing usable reverts inside
 * `StringCombobox` instead of round-tripping a value the store would
 * immediately collapse to `null`.
 */
function normalizeTyped(raw: string): string {
  return stripUnsafeFontNameChars(raw).trim()
}

/**
 * Options for one combobox, in offer order: Default (the store's `null`),
 * then the session-detected system family (its "mark"
 * is the detected-names caption below — `StringCombobox` options are bare
 * strings whose text IS the committed value, so the annotation cannot live
 * in the option itself), then the current custom value (always present, so
 * the choice stays visible and re-pickable), then the installed families.
 * Any real family literally named "Default" is filtered from the detected
 * and installed sources: the bare string IS the committed value and
 * 'Default' is the reset sentinel, so offering such a family would silently
 * reset the stack instead of applying it. A family can legitimately never be
 * renamed into the picker through a different string, so the sentinel wins
 * the name and the (vanishingly rare) real carrier stays unoffered; typed
 * input keeps the one documented meaning of the word ("reset").
 */
function buildOptions(
  current: string | null,
  detected: string | null,
  families: readonly string[],
): string[] {
  const options: string[] = [DEFAULT_OPTION]
  if (detected !== null && detected !== DEFAULT_OPTION && !options.includes(detected)) {
    options.push(detected)
  }
  if (current !== null && !options.includes(current)) options.push(current)
  for (const family of families) {
    if (family === DEFAULT_OPTION) continue
    if (!options.includes(family)) options.push(family)
  }
  return options
}

/**
 * Appearance-tab "Fonts" block: a StringCombobox for each of the UI and
 * monospaced families, a select-only smoothing (anti-aliasing) combobox for
 * each, and a "Use system" shortcut, over the persisted fontStore. Like
 * UIScaleSelector there is no apply/save step — the store applies the value
 * to <html> the moment a commit lands. The four pickers sit in a 2×2 grid:
 * the font names render in their own typeface (composeFontFamily preview),
 * each smoothing knob sits directly under its font and previews its own
 * mode; the interface picker enumerates every installed family while the
 * monospace picker lists only the families fontconfig tags as monospace
 * (`fc-list :mono`, the ListFontFamilies monospace flag). Nerd Font glyphs
 * (starship, eza, git status decorations) need no picker entry: the bundled
 * SauceCodePro NF webfont leads the mono stack itself (FONT_MONO_STACK), so
 * it renders by default and stays the glyph-fallback layer under any pick
 * (see specs/domains/frontend/fonts.md).
 *
 * The block ALWAYS renders: the comboboxes are free-text fields, so choosing
 * a family works on every OS — where detection/enumeration come up empty the
 * dropdown simply offers Default (+ the current choice) and the typed input
 * carries the rest. The "Use system" button, in contrast, has nothing to do
 * without a detection and stays disabled until the backend reports one.
 */
export function FontSettings() {
  const uiFontFamily = useFontStore((s) => s.uiFontFamily)
  const monoFontFamily = useFontStore((s) => s.monoFontFamily)
  const uiFontSmoothing = useFontStore((s) => s.uiFontSmoothing)
  const monoFontSmoothing = useFontStore((s) => s.monoFontSmoothing)
  const detectedUIFamily = useFontStore((s) => s.detectedUIFamily)
  const detectedMonoFamily = useFontStore((s) => s.detectedMonoFamily)
  const setUIFontFamily = useFontStore((s) => s.setUIFontFamily)
  const setMonoFontFamily = useFontStore((s) => s.setMonoFontFamily)
  const setUIFontSmoothing = useFontStore((s) => s.setUIFontSmoothing)
  const setMonoFontSmoothing = useFontStore((s) => s.setMonoFontSmoothing)

  // Installed families load once per mount: every family for the interface
  // picker, only the fontconfig-mono families for the monospace picker.
  // The two enumerations are independent fail-soft fetches — a mono-only
  // failure must not empty the interface list — and every failure is
  // already logged at the api/fonts boundary, degrading to the reduced
  // option set (Default, detections, current values); the block keeps
  // working, so nothing is surfaced here.
  const [uiFamilies, setUiFamilies] = useState<readonly string[]>([])
  const [monoFamilies, setMonoFamilies] = useState<readonly string[]>([])
  useEffect(() => {
    let cancelled = false
    listFontFamilies(false).then(
      (installed) => {
        if (!cancelled) setUiFamilies(installed)
      },
      () => {
        // Deliberate no-op: see the comment above.
      },
    )
    listFontFamilies(true).then(
      (installed) => {
        if (!cancelled) setMonoFamilies(installed)
      },
      () => {
        // Deliberate no-op: see the comment above.
      },
    )
    return () => {
      cancelled = true
    }
  }, [])

  const uiOptions = useMemo(
    () => buildOptions(uiFontFamily, detectedUIFamily, uiFamilies),
    [uiFontFamily, detectedUIFamily, uiFamilies],
  )
  const monoOptions = useMemo(
    () => buildOptions(monoFontFamily, detectedMonoFamily, monoFamilies),
    [monoFontFamily, detectedMonoFamily, monoFamilies],
  )

  const handleUIChange = useCallback(
    (value: string) => {
      setUIFontFamily(value === DEFAULT_OPTION ? null : value)
    },
    [setUIFontFamily],
  )

  const handleMonoChange = useCallback(
    (value: string) => {
      setMonoFontFamily(value === DEFAULT_OPTION ? null : value)
    },
    [setMonoFontFamily],
  )

  const handleUISmoothingChange = useCallback(
    (label: string) => {
      setUIFontSmoothing(SMOOTHING_CHOICES.find((c) => c.label === label)?.value ?? '')
    },
    [setUIFontSmoothing],
  )

  const handleMonoSmoothingChange = useCallback(
    (label: string) => {
      setMonoFontSmoothing(SMOOTHING_CHOICES.find((c) => c.label === label)?.value ?? '')
    },
    [setMonoFontSmoothing],
  )

  // Enabled once ANY family was detected; clicking applies what was found —
  // both families when the desktop reported both, the one it did otherwise.
  const handleUseSystem = useCallback(() => {
    if (detectedUIFamily !== null) setUIFontFamily(detectedUIFamily)
    if (detectedMonoFamily !== null) setMonoFontFamily(detectedMonoFamily)
  }, [detectedUIFamily, detectedMonoFamily, setUIFontFamily, setMonoFontFamily])

  const hasDetection = detectedUIFamily !== null || detectedMonoFamily !== null

  const detectedParts: string[] = []
  if (detectedUIFamily !== null) detectedParts.push(`interface: ${detectedUIFamily}`)
  if (detectedMonoFamily !== null) detectedParts.push(`monospace: ${detectedMonoFamily}`)

  // Preview: every option — and the current value in the closed field —
  // renders in its own typeface. composeFontFamily prepends the quoted name
  // to the stock stack, so a name that resolves to nothing (typed junk, a
  // family since uninstalled) degrades to the stock stack instead of a bare
  // serif default; "Default" previews the stock stack it stands for. This
  // call form is what the type-scale guard requires (`fontFamily:` must not
  // carry a raw literal outside lib/fonts.ts). The mono picker's options
  // fall back to the mono stack, keeping that picker's monospace context.
  const uiItemStyle = useCallback(
    (opt: string): CSSProperties => ({
      fontFamily: composeFontFamily(opt === DEFAULT_OPTION ? null : opt, FONT_SANS_STACK),
    }),
    [],
  )
  const monoItemStyle = useCallback(
    (opt: string): CSSProperties => ({
      fontFamily: composeFontFamily(opt === DEFAULT_OPTION ? null : opt, FONT_MONO_STACK),
    }),
    [],
  )
  const uiInputStyle = useMemo(
    () => ({ fontFamily: composeFontFamily(uiFontFamily, FONT_SANS_STACK) }),
    [uiFontFamily],
  )
  const monoInputStyle = useMemo(
    () => ({ fontFamily: composeFontFamily(monoFontFamily, FONT_MONO_STACK) }),
    [monoFontFamily],
  )
  // Each smoothing option previews its own mode through the very property
  // the knob drives (`-webkit-font-smoothing`) — subtle at this size, but
  // the same previews-itself contract as the font pickers above. "Not set"
  // carries no style: it stands for "nothing written to CSS".
  const smoothingItemStyle = useCallback((label: string): CSSProperties => {
    const css = smoothingToCss(SMOOTHING_CHOICES.find((c) => c.label === label)?.value ?? '')
    return css === null ? {} : { WebkitFontSmoothing: css }
  }, [])

  return (
    <div className="flex flex-col gap-3" data-testid="font-settings">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">Fonts</span>
        <Button
          variant="outline"
          size="sm"
          disabled={!hasDetection}
          onClick={handleUseSystem}
          title="Apply the fonts detected for this desktop"
        >
          Use system
        </Button>
      </div>
      {/* 2×2 grid: the font pickers share the first row, and each smoothing
          knob sits directly under its font. The StringComboboxes default to
          min-w-72 for standalone use — min-w-0 here lets them shrink to the
          half-column. */}
      <div className="grid grid-cols-2 gap-3" data-testid="font-settings-grid">
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-xs text-muted-foreground">Interface font</span>
          <StringCombobox
            value={uiFontFamily ?? DEFAULT_OPTION}
            options={uiOptions}
            onChange={handleUIChange}
            normalize={normalizeTyped}
            ariaLabel="Interface font"
            inputStyle={uiInputStyle}
            itemStyle={uiItemStyle}
            className="min-w-0"
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-xs text-muted-foreground">Monospace font</span>
          <StringCombobox
            value={monoFontFamily ?? DEFAULT_OPTION}
            options={monoOptions}
            onChange={handleMonoChange}
            normalize={normalizeTyped}
            ariaLabel="Monospace font"
            inputStyle={monoInputStyle}
            itemStyle={monoItemStyle}
            className="min-w-0"
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-xs text-muted-foreground">Interface smoothing</span>
          <StringCombobox
            value={smoothingLabel(uiFontSmoothing)}
            options={SMOOTHING_LABELS}
            onChange={handleUISmoothingChange}
            ariaLabel="Interface font smoothing"
            editable={false}
            itemStyle={smoothingItemStyle}
            className="min-w-0"
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-xs text-muted-foreground">Monospace smoothing</span>
          <StringCombobox
            value={smoothingLabel(monoFontSmoothing)}
            options={SMOOTHING_LABELS}
            onChange={handleMonoSmoothingChange}
            ariaLabel="Monospace font smoothing"
            editable={false}
            itemStyle={smoothingItemStyle}
            className="min-w-0"
          />
        </div>
      </div>
      <p className="text-xs text-muted-foreground" data-testid="font-detection-caption">
        {detectedParts.length > 0
          ? `Detected system fonts — ${detectedParts.join(', ')}.`
          : 'System fonts not detected on this desktop.'}
      </p>
      <p className="text-xs text-muted-foreground">
        Font changes on the desktop apply after restarting c0wrk.
      </p>
    </div>
  )
}
