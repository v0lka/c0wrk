// @vitest-environment jsdom
//
// Tests for api/workspace.ts — two suites:
//
// 1. probePathStat — the tri-state existence probe the markdown link
//    resolver uses to rank candidate paths. The seam contract under test:
//    a definitive stat verdict maps to `exists`/`missing`, while ANY
//    failure (RPC error, malformed payload) maps to `unknown` — never
//    to `missing`, so a failed probe can never silently advance link
//    resolution to the next candidate.
//
// 2. the workspace RPC wrappers added for the file-tree mutations and the
//    reveal-in-file-manager surface (issue #60): argument forwarding, the
//    log-then-rethrow error path, and RevealTarget validation.
//
// Both suites drive the wrappers through the REAL runtime seam — a fake
// Wails App binding installed on `window.go` — so the wrappers' logger
// output stays live and is intercepted at its source: the console spy
// below (logger.error prints to console.error at the default level).

import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'

import {
  createFile,
  createDirectory,
  deleteEntry,
  probePathStat,
  renameEntry,
  revealInFileManager,
} from './workspace'

let consoleError: ReturnType<typeof vi.spyOn>

/** Install a fake Wails App binding with the given RPC implementations
 *  (contravariance escape hatch, same shape as api/fonts.test.ts). */
function installAppBinding(impls: Record<string, (...args: never[]) => unknown>): void {
  ;(window as unknown as Record<string, unknown>).go = {
    desktop: { App: impls },
  }
}

beforeEach(() => {
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  delete (window as unknown as Record<string, unknown>).go
})

afterEach(() => {
  consoleError.mockRestore()
  delete (window as unknown as Record<string, unknown>).go
})

describe('probePathStat', () => {
  let statPath: Mock<(path: string) => Promise<unknown>>

  beforeEach(() => {
    statPath = vi.fn<(path: string) => Promise<unknown>>()
    installAppBinding({ StatPath: statPath })
  })

  it('maps an existing file to "exists"', async () => {
    statPath.mockResolvedValue({ exists: true, is_dir: false })
    await expect(probePathStat('/ws/docs/b.md')).resolves.toBe('exists')
    expect(statPath).toHaveBeenCalledWith('/ws/docs/b.md')
  })

  it('maps a missing path to "missing"', async () => {
    statPath.mockResolvedValue({ exists: false, is_dir: false })
    await expect(probePathStat('/ws/docs/b.md')).resolves.toBe('missing')
  })

  it('maps a directory to "exists" (the viewer surfaces the refusal)', async () => {
    // A directory at the candidate path EXISTS — resolution must open it
    // and let the viewer show its error, not treat it as absent.
    statPath.mockResolvedValue({ exists: true, is_dir: true })
    await expect(probePathStat('/ws/docs')).resolves.toBe('exists')
  })

  it('maps an RPC failure to "unknown", never "missing"', async () => {
    statPath.mockRejectedValue(new Error('backend unavailable'))
    await expect(probePathStat('/ws/docs/b.md')).resolves.toBe('unknown')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('maps a malformed response to "unknown" and logs it once', async () => {
    statPath.mockResolvedValue({ unexpected: 'shape' })
    await expect(probePathStat('/ws/docs/b.md')).resolves.toBe('unknown')
    expect(consoleError).toHaveBeenCalledTimes(1)
    expect(consoleError).toHaveBeenCalledWith(
      'probePathStat: unexpected response shape',
      { unexpected: 'shape' },
    )
  })
})

describe('structural mutation wrappers', () => {
  it('forward session id and paths to the backend RPCs', async () => {
    const calls: Array<[string, unknown[]]> = []
    installAppBinding({
      CreateFile: (...args: never[]) => {
        calls.push(['CreateFile', args])
        return Promise.resolve()
      },
      CreateDirectory: (...args: never[]) => {
        calls.push(['CreateDirectory', args])
        return Promise.resolve()
      },
      RenameEntry: (...args: never[]) => {
        calls.push(['RenameEntry', args])
        return Promise.resolve()
      },
      DeleteEntry: (...args: never[]) => {
        calls.push(['DeleteEntry', args])
        return Promise.resolve()
      },
    })

    await createFile('sess-1', '/ws/new.txt')
    await createDirectory('sess-1', '/ws/new-dir')
    await renameEntry('sess-1', '/ws/a.txt', '/ws/b.txt')
    await deleteEntry('sess-1', '/ws/b.txt')

    expect(calls).toEqual([
      ['CreateFile', ['sess-1', '/ws/new.txt']],
      ['CreateDirectory', ['sess-1', '/ws/new-dir']],
      ['RenameEntry', ['sess-1', '/ws/a.txt', '/ws/b.txt']],
      ['DeleteEntry', ['sess-1', '/ws/b.txt']],
    ])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('log and rethrow backend rejections', async () => {
    installAppBinding({
      CreateFile: () => Promise.reject(new Error('path already exists')),
    })
    await expect(createFile('sess-1', '/ws/x')).rejects.toThrow('path already exists')
    expect(consoleError).toHaveBeenCalledTimes(1)
  })
})

describe('revealInFileManager', () => {
  it('passes the session id, the semantic target and the entry path', async () => {
    const reveal = vi.fn()
    installAppBinding({ RevealInFileManager: reveal })

    await revealInFileManager('sess-1', 'workspace', '/ws/sub/file.txt')
    await revealInFileManager('sess-2', 'temp', '')

    expect(reveal).toHaveBeenNthCalledWith(1, 'sess-1', 'workspace', '/ws/sub/file.txt')
    expect(reveal).toHaveBeenNthCalledWith(2, 'sess-2', 'temp', '')
  })

  it('log and rethrow backend rejections', async () => {
    installAppBinding({
      RevealInFileManager: () => Promise.reject(new Error('path outside session workspace')),
    })
    await expect(revealInFileManager('sess-1', 'workspace', '/etc')).rejects.toThrow(
      'path outside session workspace',
    )
    expect(consoleError).toHaveBeenCalledTimes(1)
  })
})
