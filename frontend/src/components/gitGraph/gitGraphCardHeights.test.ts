import { describe, it, expect } from 'vitest'
import {
  MESSAGE_LINE_HEIGHT,
  MESSAGE_PAD_Y,
  MESSAGE_MAX_HEIGHT,
  DIFF_ROW_HEIGHT,
  DIFF_AREA_MAX_HEIGHT,
  STATUS_LINE_HEIGHT,
  FILE_LINE_HEIGHT,
  EXPANDED_TOP_PAD,
  EXPANDED_BOTTOM_PAD,
  LOADING_LINE_HEIGHT,
  NO_FILES_HEIGHT,
  messageAreaHeight,
  fileDiffAreaHeight,
  filesSectionHeight,
  expandedCardHeight,
  cardHeight,
  sliceDiffByPath,
  type CardExpansionState,
  type FileDiffUiState,
} from './gitGraphCardHeights'
import { ROW_SPACING } from '@/components/GitPanel/gitGraphRender'
import type { ReviewFileDiffLike } from '@/components/review/diffParsing'

/** One-hunk slice whose raw body has `lines` lines. */
function sliceWithLines(lines: number, path = 'src/a.go'): ReviewFileDiffLike {
  const body = Array.from({ length: lines }, (_, i) => `+line ${i}`).join('\n')
  return { path, hunks: [{ raw: body, old_start: 1, new_start: 1 }] }
}

function diffUiState(overrides: Partial<FileDiffUiState>): FileDiffUiState {
  return { expanded: true, loading: false, failed: false, slice: undefined, diffViewMode: 'unified', ...overrides }
}

function baseCardState(overrides: Partial<CardExpansionState>): CardExpansionState {
  return {
    messageLoading: false,
    messageFailed: false,
    fullMessage: undefined,
    filesLoading: false,
    files: undefined,
    fileDiffs: {},
    ...overrides,
  }
}

describe('messageAreaHeight', () => {
  it('is the placeholder height while loading or failed (no cached message)', () => {
    expect(messageAreaHeight({ messageLoading: true, messageFailed: false, fullMessage: undefined })).toBe(STATUS_LINE_HEIGHT)
    expect(messageAreaHeight({ messageLoading: false, messageFailed: true, fullMessage: undefined })).toBe(STATUS_LINE_HEIGHT)
  })

  it('is pad + one 16px line per newline-separated message line', () => {
    expect(
      messageAreaHeight({ messageLoading: false, messageFailed: false, fullMessage: 'subject' }),
    ).toBe(MESSAGE_PAD_Y + 1 * MESSAGE_LINE_HEIGHT)
    expect(
      messageAreaHeight({ messageLoading: false, messageFailed: false, fullMessage: 'subject\n\nbody line' }),
    ).toBe(MESSAGE_PAD_Y + 3 * MESSAGE_LINE_HEIGHT)
  })

  it('caps at MESSAGE_MAX_HEIGHT (longer messages scroll inside)', () => {
    const long = Array.from({ length: 20 }, (_, i) => `line ${i}`).join('\n')
    expect(
      messageAreaHeight({ messageLoading: false, messageFailed: false, fullMessage: long }),
    ).toBe(MESSAGE_MAX_HEIGHT)
  })
})

describe('fileDiffAreaHeight', () => {
  it('is 0 for a missing state or a collapsed file', () => {
    expect(fileDiffAreaHeight(undefined)).toBe(0)
    expect(fileDiffAreaHeight(diffUiState({ expanded: false, slice: sliceWithLines(10) }))).toBe(0)
  })

  it('is the placeholder height while loading, failed, or without hunks', () => {
    expect(fileDiffAreaHeight(diffUiState({ loading: true }))).toBe(STATUS_LINE_HEIGHT)
    expect(fileDiffAreaHeight(diffUiState({ failed: true }))).toBe(STATUS_LINE_HEIGHT)
    expect(fileDiffAreaHeight(diffUiState({ slice: undefined }))).toBe(STATUS_LINE_HEIGHT)
    expect(fileDiffAreaHeight(diffUiState({ slice: { path: 'a', hunks: [] } }))).toBe(STATUS_LINE_HEIGHT)
  })

  it('is exactly one DIFF_ROW_HEIGHT per raw hunk line', () => {
    const st = diffUiState({ slice: sliceWithLines(7) })
    expect(fileDiffAreaHeight(st)).toBeCloseTo(7 * DIFF_ROW_HEIGHT, 10)
  })

  it('sums rows across multiple hunks', () => {
    const st = diffUiState({
      slice: {
        path: 'a',
        hunks: [
          { raw: '+a\n+b', old_start: 1, new_start: 1 },
          { raw: '-c', old_start: 5, new_start: 5 },
        ],
      },
    })
    expect(fileDiffAreaHeight(st)).toBeCloseTo(3 * DIFF_ROW_HEIGHT, 10)
  })

  it('zips del/add pairs into one row in split mode (exact row parity with the table)', () => {
    const slice = {
      path: 'a',
      hunks: [
        { raw: ' ctx\n+add\n-del', old_start: 1, new_start: 1 },
      ],
    }
    const unified = fileDiffAreaHeight(diffUiState({ slice }))
    const split = fileDiffAreaHeight(diffUiState({ slice, diffViewMode: 'split' }))
    // Unified: 3 raw lines → 3 rows. Split: context row + zipped add/del row → 2 rows.
    expect(unified).toBeCloseTo(3 * DIFF_ROW_HEIGHT, 10)
    expect(split).toBeCloseTo(2 * DIFF_ROW_HEIGHT, 10)
  })

  it('caps at DIFF_AREA_MAX_HEIGHT (larger diffs scroll inside)', () => {
    const st = diffUiState({ slice: sliceWithLines(1000) })
    expect(fileDiffAreaHeight(st)).toBe(DIFF_AREA_MAX_HEIGHT)
    expect(DIFF_AREA_MAX_HEIGHT).toBeLessThan(1000 * DIFF_ROW_HEIGHT)
  })
})

describe('filesSectionHeight', () => {
  it('is the loading-line height while fetching or unfetched', () => {
    expect(filesSectionHeight({ filesLoading: true, files: undefined, fileDiffs: {} })).toBe(LOADING_LINE_HEIGHT)
    expect(filesSectionHeight({ filesLoading: false, files: undefined, fileDiffs: {} })).toBe(LOADING_LINE_HEIGHT)
  })

  it('is the no-files height for an empty list (merge / root / file-less)', () => {
    expect(filesSectionHeight({ filesLoading: false, files: [], fileDiffs: {} })).toBe(NO_FILES_HEIGHT)
  })

  it('is one FILE_LINE_HEIGHT per file plus each expanded file diff', () => {
    const files = [
      { path: 'a.go', status: 'M' },
      { path: 'b.go', status: 'A' },
      { path: 'c.go', status: 'D' },
    ]
    const fileDiffs = {
      'a.go': diffUiState({ slice: sliceWithLines(4) }),
      // b.go collapsed (no entry), c.go collapsed with an explicit entry.
      'c.go': diffUiState({ expanded: false, slice: sliceWithLines(4) }),
    }
    const expected = 3 * FILE_LINE_HEIGHT + 4 * DIFF_ROW_HEIGHT
    expect(filesSectionHeight({ filesLoading: false, files, fileDiffs })).toBe(expected)
  })
})

describe('expandedCardHeight / cardHeight', () => {
  it('sums message + top pad + files + bottom pad', () => {
    const s = baseCardState({
      fullMessage: 'subject',
      files: [{ path: 'a.go', status: 'M' }],
    })
    expect(expandedCardHeight(s)).toBe(
      messageAreaHeight(s) + EXPANDED_TOP_PAD + filesSectionHeight(s) + EXPANDED_BOTTOM_PAD,
    )
  })

  it('cardHeight is ROW_SPACING collapsed and ROW_SPACING + expansion expanded', () => {
    const s = baseCardState({ files: [{ path: 'a.go', status: 'M' }] })
    expect(cardHeight(s, false)).toBe(ROW_SPACING)
    expect(cardHeight(s, true)).toBe(ROW_SPACING + expandedCardHeight(s))
    expect(ROW_SPACING).toBe(32)
  })

  it('is a pure function of state: equal states, equal heights', () => {
    const mk = () =>
      baseCardState({
        messageLoading: false,
        fullMessage: 'a\nb',
        files: [
          { path: 'a.go', status: 'M' },
          { path: 'b.go', status: 'A' },
        ],
        fileDiffs: { 'b.go': diffUiState({ slice: sliceWithLines(9) }) },
      })
    expect(expandedCardHeight(mk())).toBe(expandedCardHeight(mk()))
  })
})

describe('sliceDiffByPath', () => {
  const diff: ReviewFileDiffLike[] = [
    { path: 'renamed-dest.go', old_path: 'old-name.go', hunks: [{ raw: '+x', old_start: 1, new_start: 1 }] },
    { path: 'plain.go', hunks: [{ raw: '-y', old_start: 1, new_start: 1 }] },
  ]

  it('finds a file by post-commit path', () => {
    expect(sliceDiffByPath(diff, 'plain.go')).toBe(diff[1])
  })

  it('falls back to old_path for renamed files', () => {
    expect(sliceDiffByPath(diff, 'old-name.go')).toBe(diff[0])
    expect(sliceDiffByPath(diff, 'renamed-dest.go')).toBe(diff[0])
  })

  it('returns undefined for an unknown path or an empty diff (merge commits)', () => {
    expect(sliceDiffByPath(diff, 'nope.go')).toBeUndefined()
    expect(sliceDiffByPath([], 'plain.go')).toBeUndefined()
  })
})
