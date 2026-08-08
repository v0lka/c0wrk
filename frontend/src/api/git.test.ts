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
  getBranchBases,
  generateCommitMessage,
  getDiffStat,
  pull,
  push,
  fetch,
  getCommitFiles,
  getCommitFilesBatch,
  stashCreate,
  stashPop,
  stashList,
  discardChanges,
  appendToGitignore,
  merge,
  rebase,
  abortMerge,
  abortRebase,
  getRebaseMergeState,
  getGitHistory,
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

  it('calls app.Commit with message and returns the new SHA', async () => {
    mockApp.Commit = vi.fn().mockResolvedValue('abc123def456789012345678901234567890abcd')
    const result = await commit('fix: update config')
    expect(mockApp.Commit).toHaveBeenCalledWith('fix: update config')
    expect(result).toBe('abc123def456789012345678901234567890abcd')
  })

  it('throws when backend returns an invalid SHA', async () => {
    mockApp.Commit = vi.fn().mockResolvedValue('')
    await expect(commit('fix: empty sha')).rejects.toThrow('invalid commit SHA')
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

  it('returns BranchInfo from backend', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue({
      name: 'main',
      upstream: 'origin/main',
      ahead: 2,
      behind: 1,
    })
    const result = await getCurrentBranch()
    expect(result).toEqual({
      name: 'main',
      upstream: 'origin/main',
      ahead: 2,
      behind: 1,
    })
  })

  it('returns BranchInfo with empty upstream for detached HEAD', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue({
      name: 'HEAD',
      upstream: '',
      ahead: 0,
      behind: 0,
    })
    const result = await getCurrentBranch()
    expect(result.upstream).toBe('')
    expect(result.ahead).toBe(0)
    expect(result.behind).toBe(0)
  })

  it('throws when backend returns a plain string (old shape)', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue('main')
    await expect(getCurrentBranch()).rejects.toThrow('invalid BranchInfo data')
  })

  it('throws when backend returns null', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue(null)
    await expect(getCurrentBranch()).rejects.toThrow('invalid BranchInfo data')
  })

  it('throws when backend returns object missing ahead', async () => {
    mockApp.GetCurrentBranch = vi.fn().mockResolvedValue({ name: 'main', upstream: '', behind: 0 })
    await expect(getCurrentBranch()).rejects.toThrow('invalid BranchInfo data')
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
    await createBranch('feature/new', '')
    expect(mockApp.CreateBranch).toHaveBeenCalledWith('feature/new', '')
  })

  it('propagates errors from backend', async () => {
    mockApp.CreateBranch = vi.fn().mockRejectedValue(
      new Error('branch "feature/new" already exists'),
    )
    await expect(createBranch('feature/new', '')).rejects.toThrow(
      'branch "feature/new" already exists',
    )
  })
})

describe('getBranchBases', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.GetBranchBases and returns the result', async () => {
    const bases = [
      { ref: 'develop', label: 'develop', type: 'local', detail: '' },
      { ref: 'origin/main', label: 'origin/main', type: 'remote', detail: '' },
      { ref: 'v1.0', label: 'v1.0', type: 'tag', detail: '' },
      { ref: 'a3f5c1d', label: 'a3f5c1d', type: 'commit', detail: 'fix: login bug' },
    ]
    mockApp.GetBranchBases = vi.fn().mockResolvedValue(bases)
    const result = await getBranchBases()
    expect(mockApp.GetBranchBases).toHaveBeenCalled()
    expect(result).toEqual(bases)
  })

  it('returns empty array on unexpected response shape', async () => {
    mockApp.GetBranchBases = vi.fn().mockResolvedValue('not-an-array')
    const result = await getBranchBases()
    expect(result).toEqual([])
  })

  it('propagates errors from backend', async () => {
    mockApp.GetBranchBases = vi.fn().mockRejectedValue(new Error('no active project'))
    await expect(getBranchBases()).rejects.toThrow('no active project')
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
    const result = await generateCommitMessage()
    expect(result).toBe('feat(api): add commit message generation')
    expect(mockApp.GenerateCommitMessage).toHaveBeenCalledWith()
  })

  it('throws when backend returns non-string', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue({ message: 'x' })
    await expect(generateCommitMessage()).rejects.toThrow(
      'backend returned non-string data',
    )
  })

  it('throws when backend returns null', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue(null)
    await expect(generateCommitMessage()).rejects.toThrow(
      'backend returned non-string data',
    )
  })

  it('throws when backend returns number', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue(42)
    await expect(generateCommitMessage()).rejects.toThrow(
      'backend returned non-string data',
    )
  })

  it('propagates errors from backend', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockRejectedValue(
      new Error('llm router not available'),
    )
    await expect(generateCommitMessage()).rejects.toThrow(
      'llm router not available',
    )
  })

  it('throws when backend resolves with an empty string', async () => {
    // Regression: a backend that returned "" used to be treated as success,
    // silently leaving the textarea empty with no error. An empty message is
    // a failure and must surface to the user.
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue('')
    await expect(generateCommitMessage()).rejects.toThrow(
      'empty commit message',
    )
  })

  it('throws when backend resolves with only whitespace', async () => {
    mockApp.GenerateCommitMessage = vi.fn().mockResolvedValue('   \n\t  ')
    await expect(generateCommitMessage()).rejects.toThrow(
      'empty commit message',
    )
  })
})

// ── Phase 5: remote operations ──

describe('pull', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.Pull with remote and returns output', async () => {
    mockApp.Pull = vi.fn().mockResolvedValue('Already up to date.')
    const result = await pull('origin')
    expect(result).toBe('Already up to date.')
    expect(mockApp.Pull).toHaveBeenCalledWith('origin', [])
  })

  it('passes empty remote to use configured upstream', async () => {
    mockApp.Pull = vi.fn().mockResolvedValue('Updating abc..def')
    await pull('')
    expect(mockApp.Pull).toHaveBeenCalledWith('', [])
  })

  it('passes flags through to backend', async () => {
    mockApp.Pull = vi.fn().mockResolvedValue('Fast-forward')
    await pull('origin', ['--ff-only'])
    expect(mockApp.Pull).toHaveBeenCalledWith('origin', ['--ff-only'])
  })

  it('throws when backend returns non-string', async () => {
    mockApp.Pull = vi.fn().mockResolvedValue({ output: 'x' })
    await expect(pull('')).rejects.toThrow('non-string output')
  })

  it('propagates errors', async () => {
    mockApp.Pull = vi.fn().mockRejectedValue(new Error('merge conflict'))
    await expect(pull('')).rejects.toThrow('merge conflict')
  })
})

describe('push', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.Push with remote and returns output', async () => {
    mockApp.Push = vi.fn().mockResolvedValue('Everything up-to-date')
    const result = await push('origin')
    expect(result).toBe('Everything up-to-date')
    expect(mockApp.Push).toHaveBeenCalledWith('origin', [])
  })

  it('passes flags through to backend', async () => {
    mockApp.Push = vi.fn().mockResolvedValue('Forced update')
    await push('origin', ['--force-with-lease'])
    expect(mockApp.Push).toHaveBeenCalledWith('origin', ['--force-with-lease'])
  })

  it('throws when backend returns non-string', async () => {
    mockApp.Push = vi.fn().mockResolvedValue(42)
    await expect(push('')).rejects.toThrow('non-string output')
  })

  it('propagates errors', async () => {
    mockApp.Push = vi.fn().mockRejectedValue(new Error('rejected'))
    await expect(push('')).rejects.toThrow('rejected')
  })
})

describe('fetch', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.Fetch with remote and returns output', async () => {
    mockApp.Fetch = vi.fn().mockResolvedValue('From origin\n   abc..def  main -> origin/main')
    const result = await fetch('origin')
    expect(result).toContain('origin/main')
    expect(mockApp.Fetch).toHaveBeenCalledWith('origin', [])
  })

  it('passes flags through to backend', async () => {
    mockApp.Fetch = vi.fn().mockResolvedValue('Pruning')
    await fetch('origin', ['--prune'])
    expect(mockApp.Fetch).toHaveBeenCalledWith('origin', ['--prune'])
  })

  it('throws when backend returns non-string', async () => {
    mockApp.Fetch = vi.fn().mockResolvedValue(null)
    await expect(fetch('')).rejects.toThrow('non-string output')
  })

  it('propagates errors', async () => {
    mockApp.Fetch = vi.fn().mockRejectedValue(new Error('network'))
    await expect(fetch('')).rejects.toThrow('network')
  })
})

// ── Commit history ──

describe('getCommitFiles', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns parsed commit files from backend', async () => {
    mockApp.GetCommitFiles = vi.fn().mockResolvedValue([
      { path: 'src/a.ts', status: 'A' },
      { path: 'src/b.ts', status: 'M' },
    ])
    const result = await getCommitFiles('abc123')
    expect(result).toEqual([
      { path: 'src/a.ts', status: 'A' },
      { path: 'src/b.ts', status: 'M' },
    ])
    expect(mockApp.GetCommitFiles).toHaveBeenCalledWith('abc123')
  })

  it('returns empty array when backend returns non-array', async () => {
    mockApp.GetCommitFiles = vi.fn().mockResolvedValue('invalid')
    expect(await getCommitFiles('abc')).toEqual([])
  })

  it('returns empty array when an element fails the guard', async () => {
    mockApp.GetCommitFiles = vi.fn().mockResolvedValue([
      { path: 'a.ts', status: 'M' },
      { path: 'b.ts' },
    ])
    expect(await getCommitFiles('abc')).toEqual([])
  })

  it('propagates errors', async () => {
    mockApp.GetCommitFiles = vi.fn().mockRejectedValue(new Error('bad sha'))
    await expect(getCommitFiles('abc')).rejects.toThrow('bad sha')
  })
})

describe('getCommitFilesBatch', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns parsed commit files map from backend', async () => {
    mockApp.GetCommitFilesBatch = vi.fn().mockResolvedValue({
      aaa: [{ path: 'src/a.ts', status: 'A' }],
      bbb: [{ path: 'README.md', status: 'M' }],
    })
    const result = await getCommitFilesBatch(['aaa', 'bbb'])
    expect(result).toEqual({
      aaa: [{ path: 'src/a.ts', status: 'A' }],
      bbb: [{ path: 'README.md', status: 'M' }],
    })
    expect(mockApp.GetCommitFilesBatch).toHaveBeenCalledWith(['aaa', 'bbb'])
  })

  it('returns empty object when backend returns non-object', async () => {
    mockApp.GetCommitFilesBatch = vi.fn().mockResolvedValue('invalid')
    expect(await getCommitFilesBatch(['aaa'])).toEqual({})
  })

  it('replaces entries with invalid file arrays with empty arrays', async () => {
    mockApp.GetCommitFilesBatch = vi.fn().mockResolvedValue({
      aaa: [{ path: 'a.ts', status: 'M' }],
      bbb: 'not-an-array',
    })
    const result = await getCommitFilesBatch(['aaa', 'bbb'])
    expect(result.aaa).toEqual([{ path: 'a.ts', status: 'M' }])
    expect(result.bbb).toEqual([])
  })

  it('propagates errors', async () => {
    mockApp.GetCommitFilesBatch = vi.fn().mockRejectedValue(new Error('bad sha'))
    await expect(getCommitFilesBatch(['abc'])).rejects.toThrow('bad sha')
  })
})

// ── Phase 5: stash ──

describe('stashCreate', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.StashCreate with message', async () => {
    mockApp.StashCreate = vi.fn().mockResolvedValue(undefined)
    await stashCreate('wip')
    expect(mockApp.StashCreate).toHaveBeenCalledWith('wip')
  })

  it('passes empty message for default stash', async () => {
    mockApp.StashCreate = vi.fn().mockResolvedValue(undefined)
    await stashCreate('')
    expect(mockApp.StashCreate).toHaveBeenCalledWith('')
  })

  it('propagates errors', async () => {
    mockApp.StashCreate = vi.fn().mockRejectedValue(new Error('no changes'))
    await expect(stashCreate('wip')).rejects.toThrow('no changes')
  })
})

describe('stashPop', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.StashPop with index', async () => {
    mockApp.StashPop = vi.fn().mockResolvedValue(undefined)
    await stashPop(0)
    expect(mockApp.StashPop).toHaveBeenCalledWith(0)
  })

  it('propagates errors', async () => {
    mockApp.StashPop = vi.fn().mockRejectedValue(new Error('conflict'))
    await expect(stashPop(0)).rejects.toThrow('conflict')
  })
})

describe('stashList', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns parsed stash entries from backend', async () => {
    mockApp.StashList = vi.fn().mockResolvedValue([
      { index: 0, message: 'WIP on main: abc123 fix' },
      { index: 1, message: 'WIP on main: def456 init' },
    ])
    const result = await stashList()
    expect(result).toHaveLength(2)
    expect(result[0]!.index).toBe(0)
    expect(mockApp.StashList).toHaveBeenCalled()
  })

  it('returns empty array when backend returns non-array', async () => {
    mockApp.StashList = vi.fn().mockResolvedValue('invalid')
    expect(await stashList()).toEqual([])
  })

  it('returns empty array when an element fails the guard', async () => {
    mockApp.StashList = vi.fn().mockResolvedValue([
      { index: 0, message: 'wip' },
      { index: 'x', message: 'bad' },
    ])
    expect(await stashList()).toEqual([])
  })

  it('returns empty array for empty backend array', async () => {
    mockApp.StashList = vi.fn().mockResolvedValue([])
    expect(await stashList()).toEqual([])
  })

  it('propagates errors', async () => {
    mockApp.StashList = vi.fn().mockRejectedValue(new Error('no repo'))
    await expect(stashList()).rejects.toThrow('no repo')
  })
})

// ---------------------------------------------------------------------------
// Phase 6 wrappers: discard, gitignore, hunk staging, merge/rebase, graph
// ---------------------------------------------------------------------------

describe('discardChanges', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.DiscardChanges with path', async () => {
    mockApp.DiscardChanges = vi.fn().mockResolvedValue(undefined)
    await discardChanges('/repo/file.txt')
    expect(mockApp.DiscardChanges).toHaveBeenCalledWith('/repo/file.txt')
  })

  it('propagates errors', async () => {
    mockApp.DiscardChanges = vi.fn().mockRejectedValue(new Error('discard failed'))
    await expect(discardChanges('/repo/file.txt')).rejects.toThrow('discard failed')
  })
})

describe('appendToGitignore', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.AppendToGitignore with pattern', async () => {
    mockApp.AppendToGitignore = vi.fn().mockResolvedValue(undefined)
    await appendToGitignore('build/')
    expect(mockApp.AppendToGitignore).toHaveBeenCalledWith('build/')
  })

  it('propagates errors', async () => {
    mockApp.AppendToGitignore = vi.fn().mockRejectedValue(new Error('write failed'))
    await expect(appendToGitignore('build/')).rejects.toThrow('write failed')
  })
})

describe('merge', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.Merge with branch', async () => {
    mockApp.Merge = vi.fn().mockResolvedValue(undefined)
    await merge('topic')
    expect(mockApp.Merge).toHaveBeenCalledWith('topic')
  })

  it('propagates conflict errors', async () => {
    mockApp.Merge = vi.fn().mockRejectedValue(new Error('merge conflict'))
    await expect(merge('topic')).rejects.toThrow('merge conflict')
  })
})

describe('rebase', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.Rebase with branch', async () => {
    mockApp.Rebase = vi.fn().mockResolvedValue(undefined)
    await rebase('main')
    expect(mockApp.Rebase).toHaveBeenCalledWith('main')
  })

  it('propagates conflict errors', async () => {
    mockApp.Rebase = vi.fn().mockRejectedValue(new Error('rebase conflict'))
    await expect(rebase('main')).rejects.toThrow('rebase conflict')
  })
})

describe('abortMerge', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.AbortMerge', async () => {
    mockApp.AbortMerge = vi.fn().mockResolvedValue(undefined)
    await abortMerge()
    expect(mockApp.AbortMerge).toHaveBeenCalled()
  })

  it('propagates errors', async () => {
    mockApp.AbortMerge = vi.fn().mockRejectedValue(new Error('no merge in progress'))
    await expect(abortMerge()).rejects.toThrow('no merge in progress')
  })
})

describe('abortRebase', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('calls app.AbortRebase', async () => {
    mockApp.AbortRebase = vi.fn().mockResolvedValue(undefined)
    await abortRebase()
    expect(mockApp.AbortRebase).toHaveBeenCalled()
  })

  it('propagates errors', async () => {
    mockApp.AbortRebase = vi.fn().mockRejectedValue(new Error('no rebase in progress'))
    await expect(abortRebase()).rejects.toThrow('no rebase in progress')
  })
})

describe('getRebaseMergeState', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns the merge/rebase state from backend', async () => {
    mockApp.GetRebaseMergeState = vi.fn().mockResolvedValue({ is_merging: true, is_rebasing: false })
    const result = await getRebaseMergeState()
    expect(result).toEqual({ is_merging: true, is_rebasing: false })
  })

  it('returns clean default when backend returns a non-conforming shape', async () => {
    mockApp.GetRebaseMergeState = vi.fn().mockResolvedValue({ is_merging: 'yes' })
    expect(await getRebaseMergeState()).toEqual({ is_merging: false, is_rebasing: false })
  })

  it('returns clean default when backend returns null', async () => {
    mockApp.GetRebaseMergeState = vi.fn().mockResolvedValue(null)
    expect(await getRebaseMergeState()).toEqual({ is_merging: false, is_rebasing: false })
  })

  it('propagates errors', async () => {
    mockApp.GetRebaseMergeState = vi.fn().mockRejectedValue(new Error('git error'))
    await expect(getRebaseMergeState()).rejects.toThrow('git error')
  })
})

describe('getGitHistory', () => {
  beforeEach(() => {
    Object.keys(mockApp).forEach(k => delete mockApp[k])
  })

  it('returns parsed commits with all unified fields from backend', async () => {
    mockApp.GetGitHistory = vi.fn().mockResolvedValue([
      { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'jane@x.io', date: '2026-07-10', message: 'feat: x', refs: ['HEAD -> main'] },
      { sha: 'bbb', parents: [], author: 'Jane', email: 'jane@x.io', date: '2026-07-09', message: 'init', refs: [] },
    ])
    const result = await getGitHistory()
    expect(result).toEqual([
      { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'jane@x.io', date: '2026-07-10', message: 'feat: x', refs: ['HEAD -> main'] },
      { sha: 'bbb', parents: [], author: 'Jane', email: 'jane@x.io', date: '2026-07-09', message: 'init', refs: [] },
    ])
  })

  it('returns empty array when backend returns a non-array', async () => {
    mockApp.GetGitHistory = vi.fn().mockResolvedValue('invalid')
    expect(await getGitHistory()).toEqual([])
  })

  it('returns empty array when an element fails the guard (bad sha type)', async () => {
    mockApp.GetGitHistory = vi.fn().mockResolvedValue([
      { sha: 'aaa', parents: [], author: 'Jane', email: 'j@x.io', date: 'd', message: 'ok', refs: [] },
      { sha: 123, parents: [], author: 'Jane', email: 'j@x.io', date: 'd', message: 'bad', refs: [] },
    ])
    expect(await getGitHistory()).toEqual([])
  })

  it('returns empty array when parents is not an array', async () => {
    mockApp.GetGitHistory = vi.fn().mockResolvedValue([
      { sha: 'aaa', parents: 'bbb', author: 'Jane', email: 'j@x.io', date: 'd', message: 'ok', refs: [] },
    ])
    expect(await getGitHistory()).toEqual([])
  })

  it('rejects commits with parents:null (guard requires arrays, backend sends [])', async () => {
    mockApp.GetGitHistory = vi.fn().mockResolvedValue([
      { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'j@x.io', date: 'd', message: 'feat', refs: ['HEAD -> main'] },
      { sha: 'bbb', parents: null, author: 'Jane', email: 'j@x.io', date: 'd', message: 'root', refs: null },
    ])
    // The guard rejects null arrays; the backend now sends [] via
    // parents := []string{} so this scenario should not occur in practice.
    expect(await getGitHistory()).toEqual([])
  })

  it('returns empty array when a required string field is missing', async () => {
    mockApp.GetGitHistory = vi.fn().mockResolvedValue([
      { sha: 'aaa', parents: [], author: 'Jane', email: 'j@x.io', date: 'd', message: 'ok', refs: [] },
      { sha: 'bbb', parents: [], author: 'Jane', email: 'j@x.io', date: 'd', refs: [] },
    ])
    expect(await getGitHistory()).toEqual([])
  })

  it('returns empty array when backend returns empty array', async () => {
    mockApp.GetGitHistory = vi.fn().mockResolvedValue([])
    expect(await getGitHistory()).toEqual([])
  })

  it('propagates errors', async () => {
    mockApp.GetGitHistory = vi.fn().mockRejectedValue(new Error('git error'))
    await expect(getGitHistory()).rejects.toThrow('git error')
  })
})
