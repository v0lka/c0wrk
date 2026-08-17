import { describe, it, expect } from 'vitest'
import { parseUserMessageMeta, buildUserMessageMeta } from './userMessageMeta'
import type { AttachmentInfoUI } from '@/types/models'

// Sample well-formed records mirroring the backend SNAKE_CASE shape.
const validImage = {
  id: 'img-1',
  name: 'cat.png',
  thumbnail: 'data:image/jpeg;base64,AAA',
  path: '/tmp/img-1.png',
  media_type: 'image/png',
}

const validAttachment = {
  original_name: 'report.pdf',
  format: 'pdf',
  size_bytes: 4096,
}

describe('parseUserMessageMeta', () => {
  describe('empty / non-object input', () => {
    it('returns {} for undefined', () => {
      expect(parseUserMessageMeta(undefined)).toEqual({})
    })

    it('returns {} for an empty object', () => {
      expect(parseUserMessageMeta({})).toEqual({})
    })

    it('returns {} when only unknown fields are present', () => {
      expect(parseUserMessageMeta({ foo: 'bar', n: 1 })).toEqual({})
    })

    it('omits goal when it is false (mirrors backend omitempty)', () => {
      expect(parseUserMessageMeta({ goal: false })).toEqual({})
    })

    it('omits goal when it is a non-boolean truthy value', () => {
      expect(parseUserMessageMeta({ goal: 'yes' })).toEqual({})
      expect(parseUserMessageMeta({ goal: 1 })).toEqual({})
    })

    it('omits is_nudge when false (mirrors backend omitempty)', () => {
      expect(parseUserMessageMeta({ is_nudge: false })).toEqual({})
    })

    it('omits is_nudge when a non-boolean truthy value', () => {
      expect(parseUserMessageMeta({ is_nudge: 'yes' })).toEqual({})
    })

    it('includes is_nudge when strictly true', () => {
      expect(parseUserMessageMeta({ is_nudge: true })).toEqual({ is_nudge: true })
    })
  })

  describe('full parse', () => {
    it('parses goal + images + attachments together', () => {
      expect(
        parseUserMessageMeta({
          goal: true,
          images: [validImage],
          attachments: [validAttachment],
        }),
      ).toEqual({
        goal: true,
        images: [validImage],
        attachments: [validAttachment],
      })
    })

    it('parses multiple images and attachments', () => {
      const image2 = { ...validImage, id: 'img-2' }
      const att2 = { ...validAttachment, original_name: 'doc.docx' }
      const out = parseUserMessageMeta({
        goal: true,
        images: [validImage, image2],
        attachments: [validAttachment, att2],
      })
      expect(out.images).toHaveLength(2)
      expect(out.attachments).toHaveLength(2)
      expect(out.goal).toBe(true)
    })
  })

  describe('partial signals', () => {
    it('parses goal only', () => {
      expect(parseUserMessageMeta({ goal: true })).toEqual({ goal: true })
    })

    it('parses images only (legacy {images:[...]} shape)', () => {
      expect(parseUserMessageMeta({ images: [validImage] })).toEqual({
        images: [validImage],
      })
    })

    it('parses attachments only', () => {
      expect(parseUserMessageMeta({ attachments: [validAttachment] })).toEqual({
        attachments: [validAttachment],
      })
    })

    it('parses goal + images without attachments', () => {
      expect(parseUserMessageMeta({ goal: true, images: [validImage] })).toEqual({
        goal: true,
        images: [validImage],
      })
    })
  })

  describe('invalid / incomplete array elements are skipped', () => {
    it('drops image records missing required fields', () => {
      const out = parseUserMessageMeta({
        images: [
          validImage,
          { id: 'x', name: 'y' }, // missing thumbnail/path/media_type
          { id: 'x', name: 'y', thumbnail: 't', path: 'p' }, // missing media_type
        ],
      })
      expect(out.images).toEqual([validImage])
    })

    it('drops image records with wrong field types', () => {
      const out = parseUserMessageMeta({
        images: [
          { ...validImage, id: 123 }, // id not a string
          { ...validImage, media_type: 9 }, // media_type not a string
          validImage,
        ],
      })
      expect(out.images).toEqual([validImage])
    })

    it('drops non-object image elements (string/number/null)', () => {
      const out = parseUserMessageMeta({
        images: ['not-an-object', 42, null, validImage, undefined],
      })
      expect(out.images).toEqual([validImage])
    })

    it('drops attachment records missing required fields', () => {
      const out = parseUserMessageMeta({
        attachments: [
          validAttachment,
          { original_name: 'a', format: 'pdf' }, // missing size_bytes
          { format: 'pdf', size_bytes: 1 }, // missing original_name
        ],
      })
      expect(out.attachments).toEqual([validAttachment])
    })

    it('drops attachment records with wrong field types', () => {
      const out = parseUserMessageMeta({
        attachments: [
          { ...validAttachment, size_bytes: 'big' }, // size_bytes not a number
          { ...validAttachment, format: 7 }, // format not a string
          validAttachment,
        ],
      })
      expect(out.attachments).toEqual([validAttachment])
    })

    it('drops all-invalid images → omits the images key entirely', () => {
      const out = parseUserMessageMeta({ images: [{ id: 'x' }, 'nope', null] })
      expect(out.images).toBeUndefined()
      expect(out).toEqual({})
    })

    it('drops all-invalid attachments → omits the attachments key entirely', () => {
      const out = parseUserMessageMeta({ attachments: [{ format: 'pdf' }] })
      expect(out.attachments).toBeUndefined()
      expect(out).toEqual({})
    })

    it('keeps goal even when sibling arrays are entirely invalid', () => {
      expect(parseUserMessageMeta({ goal: true, images: ['bad'] })).toEqual({
        goal: true,
      })
    })
  })

  describe('non-array images/attachments are ignored', () => {
    it('ignores a non-array images value', () => {
      expect(parseUserMessageMeta({ images: 'not-an-array' })).toEqual({})
    })

    it('ignores null images value', () => {
      expect(parseUserMessageMeta({ images: null })).toEqual({})
    })

    it('ignores an object-typed images value', () => {
      expect(parseUserMessageMeta({ images: { a: 1 } })).toEqual({})
    })

    it('ignores a non-array attachments value', () => {
      expect(parseUserMessageMeta({ attachments: 99 })).toEqual({})
    })
  })

  describe('never throws', () => {
    it('handles deeply weird input without throwing', () => {
      expect(() =>
        parseUserMessageMeta({
          goal: { nested: true },
          images: [[], {}, 0],
          attachments: [{ original_name: undefined }],
        }),
      ).not.toThrow()
    })
  })
})

describe('buildUserMessageMeta', () => {
  const DOC: AttachmentInfoUI = {
    id: 'd1',
    originalName: 'report.pdf',
    format: 'pdf',
    sizeBytes: 4096,
  }
  const IMG: AttachmentInfoUI = {
    id: 'img-1',
    originalName: 'cat.png',
    format: 'png',
    sizeBytes: 2048,
    isImage: true,
    thumbnail: 'data:image/jpeg;base64,AAA',
    path: '/tmp/img-1.png',
    mediaType: 'image/png',
  }

  it('returns undefined for a plain text message (no goal, no attachments, no nudge)', () => {
    expect(buildUserMessageMeta(false, [])).toBeUndefined()
  })

  it('returns {goal:true} for a goal-only message', () => {
    expect(buildUserMessageMeta(true, [])).toEqual({ goal: true })
  })

  it('returns {is_nudge:true} for a nudge-only message', () => {
    expect(buildUserMessageMeta(false, [], true)).toEqual({ is_nudge: true })
  })

  it('maps document attachments to StoredAttachmentMeta records', () => {
    expect(buildUserMessageMeta(false, [DOC])).toEqual({
      attachments: [{ original_name: 'report.pdf', format: 'pdf', size_bytes: 4096 }],
    })
  })

  it('maps image attachments to StoredImageMeta records', () => {
    expect(buildUserMessageMeta(false, [IMG])).toEqual({
      images: [
        {
          id: 'img-1',
          name: 'cat.png',
          thumbnail: 'data:image/jpeg;base64,AAA',
          path: '/tmp/img-1.png',
          media_type: 'image/png',
        },
      ],
    })
  })

  it('combines all signals in one blob', () => {
    expect(buildUserMessageMeta(true, [DOC, IMG], true)).toEqual({
      goal: true,
      is_nudge: true,
      attachments: [{ original_name: 'report.pdf', format: 'pdf', size_bytes: 4096 }],
      images: [
        {
          id: 'img-1',
          name: 'cat.png',
          thumbnail: 'data:image/jpeg;base64,AAA',
          path: '/tmp/img-1.png',
          media_type: 'image/png',
        },
      ],
    })
  })

  it('skips image records missing any required field (parse would drop them anyway)', () => {
    const incomplete = { ...IMG, path: undefined } as AttachmentInfoUI
    expect(buildUserMessageMeta(false, [incomplete])).toBeUndefined()
  })

  it('round-trips through parseUserMessageMeta', () => {
    const built = buildUserMessageMeta(true, [DOC, IMG], true)
    expect(built).toBeDefined()
    expect(parseUserMessageMeta(built)).toEqual({
      goal: true,
      is_nudge: true,
      attachments: [{ original_name: 'report.pdf', format: 'pdf', size_bytes: 4096 }],
      images: [
        {
          id: 'img-1',
          name: 'cat.png',
          thumbnail: 'data:image/jpeg;base64,AAA',
          path: '/tmp/img-1.png',
          media_type: 'image/png',
        },
      ],
    })
  })
})
