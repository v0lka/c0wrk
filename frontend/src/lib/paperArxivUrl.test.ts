// paperArxivUrl — the "Open in browser" URL derivation. The table mirrors
// core/papers' NormalizeArxivID cases so the frontend action and the backend
// paper.html fetch agree on what counts as a usable arXiv identifier; a
// malformed value must never become a browser URL.

import { describe, expect, it } from 'vitest'
import { arxivAbsUrl } from './paperArxivUrl'
import type { PaperIdentifier } from '@/api/papers'

function idents(...values: [string, string][]): PaperIdentifier[] {
  return values.map(([scheme, value]) => ({ scheme, value }))
}

describe('arxivAbsUrl', () => {
  it('accepts bare, prefixed, and URL forms (version kept)', () => {
    expect(arxivAbsUrl(idents(['arxiv', '1706.03762']))).toBe(
      'https://arxiv.org/abs/1706.03762',
    )
    expect(arxivAbsUrl(idents(['arxiv', 'arXiv:1706.03762']))).toBe(
      'https://arxiv.org/abs/1706.03762',
    )
    expect(arxivAbsUrl(idents(['arxiv', 'arxiv:2401.12345']))).toBe(
      'https://arxiv.org/abs/2401.12345',
    )
    expect(arxivAbsUrl(idents(['arxiv', '1706.03762v7']))).toBe(
      'https://arxiv.org/abs/1706.03762v7',
    )
    expect(arxivAbsUrl(idents(['arxiv', 'https://arxiv.org/abs/1706.03762']))).toBe(
      'https://arxiv.org/abs/1706.03762',
    )
    expect(arxivAbsUrl(idents(['arxiv', 'arxiv.org/html/hep-th/9901001']))).toBe(
      'https://arxiv.org/abs/hep-th/9901001',
    )
  })

  it('folds the scheme case-insensitively and takes the first usable value', () => {
    expect(
      arxivAbsUrl(idents(['doi', '10.1/x'], ['ArXiv', '1706.03762'], ['arxiv', '2401.99999'])),
    ).toBe('https://arxiv.org/abs/1706.03762')
  })

  it('returns "" for values that carry no well-formed id', () => {
    expect(arxivAbsUrl(idents(['doi', '10.1/x']))).toBe('')
    expect(arxivAbsUrl(idents(['arxiv', '10.1016/j.nonsense']))).toBe('')
    expect(arxivAbsUrl(idents(['arxiv', '']))).toBe('')
    expect(arxivAbsUrl(idents(['arxiv', 'https://arxiv.org/']))).toBe('')
    expect(arxivAbsUrl([])).toBe('')
  })
})
