import { describe, it, expect } from 'vitest'
import { fileNameFromPath, isBinaryContent } from './fileViewerUtils'

describe('fileNameFromPath', () => {
  it('extracts the basename from a regular path', () => {
    expect(fileNameFromPath('src/components/Button.tsx')).toBe('Button.tsx')
  })

  it('returns the path itself when it has no slashes', () => {
    expect(fileNameFromPath('README.md')).toBe('README.md')
  })

  it('title-cases the label for a c0wrk: synthetic path', () => {
    expect(fileNameFromPath('c0wrk:review')).toBe('Review')
  })

  it('renders "Commit <short-sha>" for a c0wrk:commit:<sha> path', () => {
    expect(fileNameFromPath('c0wrk:commit:abcdef1234567890')).toBe('Commit abcdef1')
  })

  it('handles a full 40-char SHA in the commit path', () => {
    const sha = '0123456789abcdef0123456789abcdef01234567'
    expect(fileNameFromPath(`c0wrk:commit:${sha}`)).toBe('Commit 0123456')
  })
})

describe('isBinaryContent', () => {
  it('returns false for plain text', () => {
    expect(isBinaryContent('hello world')).toBe(false)
  })

  it('returns true for content with null bytes', () => {
    expect(isBinaryContent('hello\0world')).toBe(true)
  })
})
