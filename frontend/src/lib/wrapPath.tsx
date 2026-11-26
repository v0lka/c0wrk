import { Fragment } from 'react'
import type { ComponentProps, CSSProperties } from 'react'

import { DialogContent } from '@/components/ui/dialog'

const SEPARATOR_PATTERN = /([/\\])/

/**
 * `overflow-wrap: anywhere` is a min-content sizing signal, not just a wrap
 * permission: an unbreakable token can no longer grow the dialog's grid track
 * past the fixed max-width and push the footer out. Applied per-dialog (see
 * PathDialogContent) instead of on DialogContent itself so the behavior does
 * not leak into every dialog in the app.
 */
const WRAP_ANYWHERE: CSSProperties = { overflowWrap: 'anywhere' }

/**
 * Renders a filesystem path as plain text with a `<wbr />` (word-break
 * opportunity) after every `/` and `\` so long paths wrap at separator
 * boundaries. `<wbr>` is an element, not a character: it never appears in
 * `textContent` or in a copied selection, so a copied path stays byte-identical
 * to the source — the reason this is not done by inserting U+200B characters.
 *
 * Break opportunities alone are not enough: the CONTAINER must also stop
 * sizing to the unbreakable token, so render these dialogs through
 * PathDialogContent — the two mechanisms ship together.
 */
export function PathWithBreaks({ path, className }: { path: string; className?: string }) {
  // Split with a capturing group: separator lands at every odd index.
  const parts = path.split(SEPARATOR_PATTERN)
  return (
    <span className={className}>
      {parts.map((part, i) => (
        <Fragment key={i}>
          {part}
          {i % 2 === 1 && <wbr />}
        </Fragment>
      ))}
    </span>
  )
}

/**
 * DialogContent for dialogs that render an unbreakable token (see
 * PathWithBreaks). Bakes `overflow-wrap: anywhere` into the dialog's grid
 * track so the token renderer and its container fix cannot be split apart —
 * a PathWithBreaks inside a plain DialogContent re-allows the long-token
 * blowout this pair exists to prevent. A caller-supplied `style` still wins
 * per-property.
 */
export function PathDialogContent({ style, ...props }: ComponentProps<typeof DialogContent>) {
  return <DialogContent {...props} style={{ ...WRAP_ANYWHERE, ...style }} />
}
