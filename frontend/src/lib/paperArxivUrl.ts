// arXiv abs-page URL derivation from a paper card's identifiers.
//
// Mirrors core/papers' NormalizeArxivID (Go) so the "Open in browser" action
// and the backend's paper.html fetch agree on what counts as a usable arXiv
// identifier: the first identifier whose scheme folds to "arxiv", accepted in
// bare ("1706.03762"), prefixed ("arXiv:1706.03762"), and URL
// ("https://arxiv.org/abs/1706.03762v2", "arxiv.org/html/hep-th/9901001")
// form, with an optional version suffix (vN) kept. Input that carries no
// well-formed id yields '' (the caller hides the action) — a malformed value
// must never become a browser URL.
//
// Pure over data: no React, no DOM, no I/O.

import type { PaperIdentifier } from '@/api/papers'

/** Modern arXiv id: YYMM.NNNNN with an optional version suffix. */
const ARXIV_ID_NEW_RE = /^\d{4}\.\d{4,5}(v\d+)?$/
/** Legacy arXiv id: subject-area/YYMMNNN with an optional version suffix. */
const ARXIV_ID_OLD_RE = /^[a-z-]+(\.[A-Z]{2})?\/\d{7}(v\d+)?$/
/** arXiv URL path forms an id may be embedded in. */
const ARXIV_URL_PATH_PREFIXES = ['abs', 'html', 'pdf']

/** Normalize one raw identifier value; see arxivAbsUrl. */
function normalizeArxivValue(raw: string): string {
  let v = raw
  // URL form: absolutize a bare host prefix, then strip the path down to the
  // id segment.
  if (!v.includes('://') && v.toLowerCase().startsWith('arxiv.org/')) {
    v = `https://${v}`
  }
  if (v.includes('://')) {
    let path: string
    try {
      path = new URL(v).pathname
    } catch {
      return ''
    }
    let segments = path.split('/').filter((segment) => segment !== '')
    if (segments.length > 1 && ARXIV_URL_PATH_PREFIXES.includes(segments[0]!)) {
      segments = segments.slice(1)
    }
    v = segments.join('/')
  }
  // Prefixed form: strip a case-insensitive "arXiv:" prefix.
  if (v.toLowerCase().startsWith('arxiv:')) v = v.slice('arxiv:'.length)
  v = v.trim()
  return ARXIV_ID_NEW_RE.test(v) || ARXIV_ID_OLD_RE.test(v) ? v : ''
}

/**
 * The arXiv abs-page URL for a paper's first usable arXiv identifier
 * (`https://arxiv.org/abs/<id>`), or '' when the card carries none.
 */
export function arxivAbsUrl(identifiers: PaperIdentifier[]): string {
  for (const ident of identifiers) {
    if (ident.scheme.trim().toLowerCase() !== 'arxiv') continue
    const id = normalizeArxivValue(ident.value.trim())
    if (id !== '') return `https://arxiv.org/abs/${id}`
  }
  return ''
}
