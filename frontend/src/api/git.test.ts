// Unit tests for api/git.ts — type guards and API wrapper functions

import { describe, it, expect, vi, beforeEach } from 'vitest'

// --- Mock getApp before importing the module under test ---
const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
  subscribe: vi.fn(() => () => {}),
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn() },
}))

// Import after mocks are set up so they take effect
import {
  stageFile,
  unstageFile,
  stageAll,
  unstageAll,
  commit,
  getBranches,
  getCurrentBranch,
  checkoutBranch,
  createBranch,
  generateCommitMessage,
  getDiffStat,
} from '@/api/git'

// --- Type guard tests (import inline guards) ---
// Re-declare the guards here to test them directly (they're module-private)
// We test them *indirectly* through getBranches/getDiffStat behavior below,
// and directly by re-implementing the same logic in these tests.

function isBranch(v: unknown): v is { name: string; is_current: boolean } {
  if (typeof v !== 'object' || v === null) return false
  return typeof (v as Record<string, unknown>).name === 'string' && typeof (v as Record<string, unknown>).is_current === 'boolean'
}

function isDiffStat(v: unknown): v is { added: number; deleted: number } {
  if (typeof v !== 'object' || v === null) return false
  return typeof (v as Record<string, unknown>).added === 'number' && typeof (v as Record<string, unknown>).deleted === 'number'
}

describe('isBranch (type guard)', () => {
  it('accepts valid Branch', () => {
    expect(isBranch({ name: 'main', is_current: true })).toBe(true)
  })

  it('accepts Branch with is_current=false', () => {
    expect(isBranch({ name: 'feature/x', is_current: false })).toBe(true)
  })

  it('rejects null', () => {
    expect(isBranch(null)).toBe(false)
  })

  it('rejects undefined', () => {
    expect(isBranch(undefined)).toBe(false)
  })

  it('rejects string', () => {
    expect(isBranch('main')).toBe(false)
  })

  it('rejects object with missing name', () => {
    expect(isBranch({ is_current: true })).toBe(false)
  })

  it('rejects object with missing is_current', () => {
    expect(isBranch({ name: 'main' })).toBe(false)
  })

  it('rejects object with wrong name type', () => {
    expect(isBranch({ name: 42, is_current: true })).toBe(false)
  })

  it('rejects object with wrong is_current type', () => {
    expect(isBranch({ name: 'main', is_current: 'yes' })).toBe(false)
  })

  it('rejects empty object', () => {
    expect(isBranch({})).toBe(false)
  })

  it('rejects array', () => {
    expect(isBranch([])).toBe(false)
  })
})

describe('isDiffStat (type guard)', () => {
  it('accepts valid DiffStat', () => {
    expect(isDiffStat({ added: 5, deleted: 3 })).toBe(true)
  })

  it('accepts DiffStat with zeros', () => {
    expect(isDiffStat({ added: 0, deleted: 0 })).toBe(true)
  })

  it('rejects null', () => {
    expect(isDiffStat(null)).toBe(false)
  })

  it('rejects undefined', () => {
    expect(isDiffStat(undefined)).toBe(false)
  })

  it('rejects string', () => {
    expect(isDiffStat('5\t3\tfile.ts')).toBe(false)
  })

  it('rejects object with missing added', () => {
    expect(isDiffStat({ deleted: 3 })).toBe(false)
  })

  it('rejects object with missing deleted', () => {
    expect(isDiffStat({ added: 5 })).toBe(false)
  })

  it('rejects object with wrong added type', () => {
    expect(isDiffStat({ added: '5', deleted: 3 })).toBe(false)
  })

  it('rejects object with wrong deleted type', () => {
    expect(isDiffStat({ added: 5, deleted: '3' })).toBe(false)
  })

  it('rejects empty object', () => {
    expect(isDiffStat({})).toBe(false)
  })

  it('rejects array', () => {
    expect(isDiffStat([])).toBe(false)
  })
})

// --- API wrapper tests ---

describe('stageFile', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.StageFile with the path', async () => {
    mockApp.StageFile = vi.fn().mockResolvedValue(undefined)
    await stageFile('/some/file.ts')
    expect(mockApp.StageFile).toHaveBeenCalledWith('/some/file.ts')
  })

  it('propagates errors from backend', async () => {
    mockApp.StageFile = vi.fn().mockRejectedValue(new Error('git error'))
    await expect(stageFile('/some/file.ts')).rejects.toThrow('git error')
  })
})

describe('unstageFile', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.UnstageFile with the path', async () => {
    mockApp.UnstageFile = vi.fn().mockResolvedValue(undefined)
    await unstageFile('/some/file.ts')
    expect(mockApp.UnstageFile).toHaveBeenCalledWith('/some/file.ts')
  })

  it('propagates errors from backend', async () => {
    mockApp.UnstageFile = vi.fn().mockRejectedValue(new Error('git error'))
    await expect(unstageFile('/some/file.ts')).rejects.toThrow('git error')
  })
})

describe('stageAll', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.StageAll', async () => {
    mockApp.StageAll = vi.fn().mockResolvedValue(undefined)
    await stageAll()
    expect(mockApp.StageAll).toHaveBeenCalled()
  })

  it('propagates errors', async () => {
    mockApp.StageAll = vi.fn().mockRejectedValue(new Error('stage all failed'))
    await expect(stageAll()).rejects.toThrow('stage all failed')
  })
})

describe('unstageAll', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.UnstageAll', async () => {
    mockApp.UnstageAll = vi.fn().mockResolvedValue(undefined)
    await unstageAll()
    expect(mockApp.UnstageAll).toHaveBeenCalled()
  })

  it('propagates errors', async () => {
    mockApp.UnstageAll = vi.fn().mockRejectedValue(new Error('unstage all failed'))
    await expect(unstageAll()).rejects.toThrow('unstage all failed')
  })
})

describe('commit', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.Commit with message', async () => {
    mockApp.Commit = vi.fn().mockResolvedValue(undefined)
    await commit('fix: update config')
    expect(mockApp.Commit).toHaveBeenCalledWith('fix: update config')
  })

  it('propagates errors', async () => {
    mockApp.Commit = vi.fn().mockRejectedValue(new Error('nothing to commit'))
    await expect(commit('empty')).rejects.toThrow('nothing to commit')
  })
})

describe('getBranches', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns parsed branches from backend', async () => {
    mockApp.GetBranches = vi.fn().mockResolvedValue([
      { name: 'main', is_current: true },
      { name: 'feature/x', is_current: false },
    ])
    const result = await getBranches()
    expect(result).toEqual([
      { name: 'main', is_current: true },
      { name: 'feature/x', is_current: false },
    ])
  })

  it('returns empty array when backend returns non-array', async () => {
    mockApp.GetBranches = vi.fn().mockResolvedValue('invalid')
    const result = await getBranches()
    expect(result).toEqual([])
  })

  it('returns empty array when backend returns array with invalid elements', async () => {
    mockApp.GetBranches = vi.fn().mockResolvedValue([
      { name: 'main', is_current: true },
      'not-a-branch',
    ])
    const result = await getBranches()
    expect(result).toEqual([])
  })

  it('returns empty array when backend returns empty array', async () => {
    mockApp.GetBranches = vi.fn().mockResolvedValue([])
    const result = await getBranches()
    expect(result).toEqual([])
  })

  it('propagates errors from backend', async () => {
    mockApp.GetBranches = vi.fn().mockRejectedValue(new Error('git error'))
    await expect(getBranches()).rejects.toThrow('git error')
  })
})

describe('getCurrentBranch', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns branch name string', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue('main')
    const result = await getCurrentBranch()
    expect(result).toBe('main')
  })

  it('throws when backend returns non-string', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue({ name: 'main' })
    await expect(getCurrentBranch()).rejects.toThrow('backend returned non-string data')
  })

  it('throws when backend returns null', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue(null)
    await expect(getCurrentBranch()).rejects.toThrow('backend returned non-string data')
  })

  it('throws when backend returns number', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue(42)
    await expect(getCurrentBranch()).rejects.toThrow('backend returned non-string data')
  })

  it('propagates errors from backend', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockRejectedValue(new Error('no repo'))
    await expect(getCurrentBranch()).rejects.toThrow('no repo')
  })
})

describe('getDiffStat', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns DiffStat from backend', async () => {
    mockApp.GetDiffStat = vi.fn().mockResolvedValue({ added: 5, deleted: 3 })
    const result = await getDiffStat('/file.ts')
    expect(result).toEqual({ added: 5, deleted: 3 })
    expect(mockApp.GetDiffStat).toHaveBeenCalledWith('/file.ts')
  })

  it('returns zero DiffStat', async () => {
    mockApp.GetDiffStat = vi.fn().mockResolvedValue({ added: 0, deleted: 0 })
    const result = await getDiffStat('/unchanged.ts')
    expect(result).toEqual({ added: 0, deleted: 0 })
  })

  it('throws when backend returns invalid data', async () => {
    mockApp.GetDiffStat = vi.fn().mockResolvedValue({ added: '5', deleted: 3 })
    await expect(getDiffStat('/file.ts')).rejects.toThrow('backend returned invalid data')
  })

  it('throws when backend returns non-object', async () => {
    mockApp.GetDiffStat = vi.fn().mockResolvedValue('invalid')
    await expect(getDiffStat('/file.ts')).rejects.toThrow('backend returned invalid data')
  })

  it('throws when backend returns null', async () => {
    mockApp.GetDiffStat = vi.fn().mockResolvedValue(null)
    await expect(getDiffStat('/file.ts')).rejects.toThrow('backend returned invalid data')
  })

  it('propagates errors from backend', async () => {
    mockApp.GetDiffStat = vi.fn().mockRejectedValue(new Error('git error'))
    await expect(getDiffStat('/file.ts')).rejects.toThrow('git error')
  })
})

describe('checkoutBranch', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.CheckoutBranch with the branch name', async () => {
    mockApp.CheckoutBranch = vi.fn().mockResolvedValue(undefined)
    await checkoutBranch('feature/x')
    expect(mockApp.CheckoutBranch).toHaveBeenCalledWith('feature/x')
  })

  it('propagates errors from backend', async () => {
    mockApp.CheckoutBranch = vi.fn().mockRejectedValue(
      new Error('local changes would be overwritten'),
    )
    await expect(checkoutBranch('main')).rejects.toThrow(
      'local changes would be overwritten',
    )
  })
})

describe('createBranch', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.CreateBranch with the branch name', async () => {
    mockApp.CreateBranch = vi.fn().mockResolvedValue(undefined)
    await createBranch('feature/new')
    expect(mockApp.CreateBranch).toHaveBeenCalledWith('feature/new')
  })

  it('propagates errors from backend', async () => {
    mockApp.CreateBranch = vi.fn().mockRejectedValue(
      new Error('branch "feature/new" already exists'),
    )
    await expect(createBranch('feature/new')).rejects.toThrow(
      'branch "feature/new" already exists',
    )
  })
})

describe('generateCommitMessage', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns the generated commit message string', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue(
      'feat(api): add commit message generation',
    )
    const result = await generateCommitMessage('diff --git ...')
    expect(result).toBe('feat(api): add commit message generation')
    expect(mockApp.GenerateCommitMessage).toHaveBeenCalledWith('diff --git ...')
  })

  it('throws when backend returns non-string', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue({ message: 'x' })
    await expect(generateCommitMessage('diff')).rejects.toThrow(
      'backend returned non-string data',
    )
  })

  it('throws when backend returns null', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue(null)
    await expect(generateCommitMessage('diff')).rejects.toThrow(
      'backend returned non-string data',
    )
  })

  it('throws when backend returns number', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue(42)
    await expect(generateCommitMessage('diff')).rejects.toThrow(
      'backend returned non-string data',
    )
  })

  it('propagates errors from backend', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockRejectedValue(
      new Error('llm router not available'),
    )
    await expect(generateCommitMessage('diff')).rejects.toThrow(
      'llm router not available',
    )
  })
})
