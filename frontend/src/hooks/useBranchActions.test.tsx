// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the git API wrappers so tests never touch the Wails backend ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    checkoutBranch: vi.fn(),
    renameBranch: vi.fn(),
    deleteBranch: vi.fn(),
    merge: vi.fn(),
    rebase: vi.fn(),
    pushBranch: vi.fn(),
    checkoutRemoteBranch: vi.fn(),
    deleteRemoteBranch: vi.fn(),
  },
}))

vi.mock('@/api/git', () => gitMocks)

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn() },
}))

import { useBranchActions, type BranchActions } from './useBranchActions'

let result!: BranchActions
let root: Root
let container: HTMLDivElement

function Harness() {
  result = useBranchActions()
  return null
}

function renderHook() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness />)
  })
}

beforeEach(() => {
  for (const key of Object.keys(gitMocks) as (keyof typeof gitMocks)[]) {
    gitMocks[key].mockReset()
  }
  gitMocks.checkoutBranch.mockResolvedValue(undefined)
  gitMocks.renameBranch.mockResolvedValue(undefined)
  gitMocks.deleteBranch.mockResolvedValue(undefined)
  gitMocks.merge.mockResolvedValue(undefined)
  gitMocks.rebase.mockResolvedValue(undefined)
  gitMocks.pushBranch.mockResolvedValue('ok')
  gitMocks.checkoutRemoteBranch.mockResolvedValue(undefined)
  gitMocks.deleteRemoteBranch.mockResolvedValue('ok')
})

describe('useBranchActions — operations', () => {
  it('checkout calls checkoutBranch and clears busy state', async () => {
    renderHook()
    await act(async () => {
      await result.checkout('feature/x')
    })
    expect(gitMocks.checkoutBranch).toHaveBeenCalledWith('feature/x')
    expect(result.isBusy).toBe(false)
    expect(result.busyAction).toBeNull()
    expect(result.error).toBeNull()
  })

  it('tracks in-flight busy state during an operation', async () => {
    let resolve!: () => void
    gitMocks.checkoutBranch.mockImplementation(
      () => new Promise<void>((r) => { resolve = r }),
    )
    renderHook()

    let pending!: Promise<boolean>
    act(() => {
      pending = result.checkout('feature/x')
    })

    expect(result.isBusy).toBe(true)
    expect(result.busyAction).toBe('checkout')
    expect(result.busyBranch).toBe('feature/x')

    await act(async () => {
      resolve()
      await pending
    })
    expect(result.isBusy).toBe(false)
    expect(result.busyAction).toBeNull()
  })

  it('captures operation errors in error state', async () => {
    gitMocks.checkoutBranch.mockRejectedValue(new Error('local changes would be overwritten'))
    renderHook()
    await act(async () => {
      await result.checkout('feature/x')
    })
    expect(result.error).toBe('local changes would be overwritten')
    expect(result.isBusy).toBe(false)
  })

  it('rejects a concurrent operation while another is in flight', async () => {
    let resolve!: () => void
    gitMocks.checkoutBranch.mockImplementation(
      () => new Promise<void>((r) => { resolve = r }),
    )
    renderHook()

    let first!: Promise<boolean>
    let second!: Promise<boolean>
    act(() => {
      first = result.checkout('feature/x')
      second = result.checkout('feature/y')
    })

    let firstResult!: boolean
    let secondResult!: boolean
    await act(async () => {
      resolve()
      firstResult = await first
      secondResult = await second
    })

    expect(gitMocks.checkoutBranch).toHaveBeenCalledTimes(1)
    expect(gitMocks.checkoutBranch).toHaveBeenCalledWith('feature/x')
    expect(firstResult).toBe(true)
    expect(secondResult).toBe(false)
  })

  it('rename calls renameBranch with old and new names', async () => {
    renderHook()
    await act(async () => {
      await result.rename('old', 'new')
    })
    expect(gitMocks.renameBranch).toHaveBeenCalledWith('old', 'new')
  })

  it('deleteLocal passes the force flag through', async () => {
    renderHook()
    await act(async () => {
      await result.deleteLocal('doomed', true)
    })
    expect(gitMocks.deleteBranch).toHaveBeenCalledWith('doomed', true)

    await act(async () => {
      await result.deleteLocal('doomed', false)
    })
    expect(gitMocks.deleteBranch).toHaveBeenLastCalledWith('doomed', false)
  })

  it('mergeBranch, rebaseBranch and push call the matching APIs', async () => {
    renderHook()
    await act(async () => { await result.mergeBranch('feature') })
    expect(gitMocks.merge).toHaveBeenCalledWith('feature')

    await act(async () => { await result.rebaseBranch('feature') })
    expect(gitMocks.rebase).toHaveBeenCalledWith('feature')

    await act(async () => { await result.push('feature') })
    expect(gitMocks.pushBranch).toHaveBeenCalledWith('feature')
  })

  it('checkoutRemote and deleteRemote call the matching APIs', async () => {
    renderHook()
    await act(async () => { await result.checkoutRemote('origin/feature') })
    expect(gitMocks.checkoutRemoteBranch).toHaveBeenCalledWith('origin/feature')

    await act(async () => { await result.deleteRemote('feature', 'origin') })
    expect(gitMocks.deleteRemoteBranch).toHaveBeenCalledWith('feature', 'origin')
  })
})

describe('useBranchActions — inline rename', () => {
  it('startRename/commitRename commits the new name and clears the rename state', async () => {
    renderHook()
    act(() => {
      result.startRename('feature')
    })
    expect(result.renamingBranch).toBe('feature')
    expect(result.renameValue).toBe('feature')

    act(() => {
      result.setRenameValue('renamed')
    })

    await act(async () => {
      await result.commitRename()
    })
    expect(gitMocks.renameBranch).toHaveBeenCalledWith('feature', 'renamed')
    expect(result.renamingBranch).toBeNull()
  })

  it('commitRename skips the API when the name is unchanged', async () => {
    renderHook()
    act(() => {
      result.startRename('feature')
    })
    await act(async () => {
      await result.commitRename()
    })
    expect(gitMocks.renameBranch).not.toHaveBeenCalled()
    expect(result.renamingBranch).toBeNull()
  })

  it('cancelRename clears the rename state without calling the API', async () => {
    renderHook()
    act(() => {
      result.startRename('feature')
    })
    act(() => {
      result.cancelRename()
    })
    expect(result.renamingBranch).toBeNull()
    expect(gitMocks.renameBranch).not.toHaveBeenCalled()
  })
})

describe('useBranchActions — delete confirmation', () => {
  it('requestDeleteLocal + confirmDelete(force) deletes with force', async () => {
    renderHook()
    act(() => {
      result.requestDeleteLocal('feature')
    })
    expect(result.pendingDelete).toEqual({ kind: 'local', name: 'feature' })

    await act(async () => {
      await result.confirmDelete('force')
    })
    expect(gitMocks.deleteBranch).toHaveBeenCalledWith('feature', true)
    expect(result.pendingDelete).toBeNull()
  })

  it('requestDeleteLocal + confirmDelete(safe) deletes safely', async () => {
    renderHook()
    act(() => {
      result.requestDeleteLocal('feature')
    })
    await act(async () => {
      await result.confirmDelete('safe')
    })
    expect(gitMocks.deleteBranch).toHaveBeenCalledWith('feature', false)
  })

  it('requestDeleteRemote + confirmDelete deletes the remote branch', async () => {
    renderHook()
    act(() => {
      result.requestDeleteRemote('feature', 'origin')
    })
    expect(result.pendingDelete).toEqual({ kind: 'remote', name: 'feature', remote: 'origin' })

    await act(async () => {
      await result.confirmDelete('safe')
    })
    expect(gitMocks.deleteRemoteBranch).toHaveBeenCalledWith('feature', 'origin')
    expect(result.pendingDelete).toBeNull()
  })

  it('cancelDelete clears the pending action without deleting', async () => {
    renderHook()
    act(() => {
      result.requestDeleteLocal('feature')
    })
    act(() => {
      result.cancelDelete()
    })
    expect(result.pendingDelete).toBeNull()
    expect(gitMocks.deleteBranch).not.toHaveBeenCalled()
  })
})
