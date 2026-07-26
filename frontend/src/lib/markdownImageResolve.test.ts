import { describe, it, expect } from 'vitest'
import { normalizeAbsolutePath, candidateImagePaths } from './markdownImageResolve'

describe('normalizeAbsolutePath', () => {
  it('collapses "." and ".." segments', () => {
    expect(normalizeAbsolutePath('/a/b/./c/../d')).toBe('/a/b/d')
  })

  it('treats ".." above root as root', () => {
    expect(normalizeAbsolutePath('/a/../../b')).toBe('/b')
  })

  it('keeps already-clean absolute paths', () => {
    expect(normalizeAbsolutePath('/repo/docs/img.png')).toBe('/repo/docs/img.png')
  })
})

describe('candidateImagePaths', () => {
  it('returns the path verbatim for absolute src', () => {
    expect(candidateImagePaths('/abs/img.png', '/repo/readme.md', '/repo')).toEqual(['/abs/img.png'])
  })

  it('resolves relative to the document directory first, then the workspace root', () => {
    expect(candidateImagePaths('assets/a.png', '/repo/docs/readme.md', '/repo')).toEqual([
      '/repo/docs/assets/a.png',
      '/repo/assets/a.png',
    ])
  })

  it('omits the document-relative candidate when the document has no directory', () => {
    expect(candidateImagePaths('a.png', 'readme.md', '/repo')).toEqual([
      '/repo/a.png',
    ])
  })

  it('falls back to the workspace root only when no document path is given', () => {
    expect(candidateImagePaths('docs/a.png', null, '/repo')).toEqual(['/repo/docs/a.png'])
  })

  it('returns an empty array when neither base nor root is available', () => {
    expect(candidateImagePaths('a.png', null, null)).toEqual([])
  })
})
