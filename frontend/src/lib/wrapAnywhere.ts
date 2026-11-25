import type { CSSProperties } from 'react'

/**
 * Set as the `style` of the DialogContent of dialogs that render an
 * unbreakable token (see PathWithBreaks). `overflow-wrap: anywhere` is a
 * min-content sizing signal, not just a wrap permission: the token can no
 * longer grow the dialog's grid track past the fixed max-width and push the
 * footer out. Applied per-dialog instead of on DialogContent itself so the
 * behavior does not leak into every dialog in the app.
 */
export const WRAP_ANYWHERE: CSSProperties = { overflowWrap: 'anywhere' }
