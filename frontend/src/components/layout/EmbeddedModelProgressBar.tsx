// The status bar's own compact progress bar, extracted from
// EmbeddedModelStatus (which keeps only the surface selection and the markup).
//
// 64 layout-px wide, so the block's width stays stable while the percent
// changes. The width is an ABSOLUTE length (`w-[64px]`) on purpose: `w-16` is
// 4rem, which at this app's 14px root font size (`html { font-size: 14px }`) is
// 56 layout-px — the rem indirection would silently disagree with the figure
// documented here. Absolute lengths scale with the UI Scale zoom exactly like
// rem ones do, so zoom-safety is unchanged.
// `percent === null` renders an indeterminate pulse: a byte-less stage
// (verifying / extracting / signing) and a weight load both report no fraction,
// and a percentage there would be a lie.
//
// Zoom-safety: fixed layout-px sizes and a percentage width only — no viewport
// unit, no `*-screen` utility, no pointer-anchored placement.

export function EmbeddedModelProgressBar({
  percent,
  label,
}: {
  percent: number | null
  label: string
}) {
  return (
    <span
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      {...(percent === null ? {} : { 'aria-valuenow': percent })}
      data-testid="embedded-model-progress"
      className="h-1.5 w-[64px] shrink-0 overflow-hidden rounded-full bg-muted"
    >
      {percent === null ? (
        <span className="block h-full w-1/3 animate-pulse rounded-full bg-primary" />
      ) : (
        <span
          className="block h-full rounded-full bg-primary transition-all duration-150"
          style={{ width: `${percent}%` }}
        />
      )}
    </span>
  )
}
