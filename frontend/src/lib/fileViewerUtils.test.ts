import { describe, it, expect } from 'vitest'
import { fileNameFromPath, isBinaryContent, isImageFilePath } from './fileViewerUtils'

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

  it('renders the slug for a c0wrk:paper:<slug> path', () => {
    expect(fileNameFromPath('c0wrk:paper:vaswani-2017-attention')).toBe('vaswani-2017-attention')
  })

  it('falls back to "Paper" for a bare paper prefix', () => {
    expect(fileNameFromPath('c0wrk:paper:')).toBe('Paper')
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

describe('isImageFilePath', () => {
  it('returns true for the supported image extensions', () => {
    for (const ext of ['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'svg', 'avif']) {
      expect(isImageFilePath(`/ws/assets/pic.${ext}`)).toBe(true)
    }
  })

  it('is case-insensitive', () => {
    expect(isImageFilePath('/ws/PHOTO.PNG')).toBe(true)
    expect(isImageFilePath('/ws/Photo.Jpg')).toBe(true)
  })

  it('returns false for non-image extensions', () => {
    expect(isImageFilePath('/ws/src/main.ts')).toBe(false)
    expect(isImageFilePath('/ws/README.md')).toBe(false)
    expect(isImageFilePath('/ws/data.csv')).toBe(false)
  })

  it('returns false for files without an extension', () => {
    expect(isImageFilePath('/ws/README')).toBe(false)
    expect(isImageFilePath('/ws/Makefile')).toBe(false)
  })

  it('returns false for synthetic pseudo-paths', () => {
    expect(isImageFilePath('c0wrk:review')).toBe(false)
    expect(isImageFilePath('c0wrk:commit:abcdef1234567890')).toBe(false)
    expect(isImageFilePath('c0wrk:paper:vaswani-2017-attention')).toBe(false)
  })

  it('judges by the final extension only (dotted directory names)', () => {
    expect(isImageFilePath('/ws/a.b/c')).toBe(false)
    expect(isImageFilePath('/ws/a.b/photo.png')).toBe(true)
  })
})
