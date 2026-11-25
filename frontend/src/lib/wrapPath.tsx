import { Fragment } from 'react'

const SEPARATOR_PATTERN = /([/\\])/

/**
 * Renders a filesystem path as plain text with a `<wbr />` (word-break
 * opportunity) after every `/` and `\` so long paths wrap at separator
 * boundaries. `<wbr>` is an element, not a character: it never appears in
 * `textContent` or in a copied selection, so a copied path stays byte-identical
 * to the source — the reason this is not done by inserting U+200B characters.
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
