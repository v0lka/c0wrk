// Single wrapper for running a git operation and recording its outcome.
//
// Every user-triggered git action in the Git panel funnels through here so the
// per-project `operationByProject` slice in gitPanelStore always reflects the
// most recent result (success banner or failure toast) without each call site
// re-implementing the try/catch/record dance — and, crucially, without an
// early `return` or a rethrown error skipping the record.

import { logger } from '@/lib/logger'
import { useGitPanelStore, type GitOperationKind } from '@/stores/gitPanelStore'

/** Arguments for {@link runGitOperation}. */
export interface RunGitOperationArgs<T> {
  /**
   * Owning project id. Captured at call time so an operation that completes
   * after a project switch can never land in the wrong project's record.
   */
  projectId: string
  /** Which operation this is — written to the record's `kind`. */
  kind: GitOperationKind
  /** Human-readable summary shown next to the result — the record's `label`. */
  label: string
  /**
   * The operation itself. A resolved promise is a success; a rejected one is a
   * captured failure and is NEVER rethrown.
   */
  fn: () => Promise<T>
  /**
   * Project `fn`'s result onto the record's bounded `output` string. Must be a
   * pure, non-throwing projection. Defaults to the result itself when it is
   * already a string, and to `''` for every other shape.
   */
  extractOutput?: (result: T) => string
  /**
   * When false, a successful outcome is NOT recorded — the caller already
   * surfaces success through the changed git state (e.g. a file moving between
   * the staged/unstaged sections, an entry leaving the tree after a
   * `.gitignore` append), so a success entry would only add console noise.
   * Failures are ALWAYS recorded, regardless of this flag. Defaults to true.
   */
  recordSuccess?: boolean
  /**
   * Severity for the log line emitted when `fn` rejects. Defaults to `'error'`
   * — a failed remote op, commit, or branch mutation is a genuine fault. The
   * per-file staging/discard/gitignore ops pass `'warn'`: their failures are
   * routine, user-driven outcomes (a discard on an unchanged file, a stage of
   * an already-staged path), not faults. Deliberately independent of
   * `recordSuccess`, which governs the console record, not logging severity.
   */
  logLevel?: 'error' | 'warn'
}

/**
 * Result of a git operation: the success value, or the captured failure. A
 * discriminated union on `ok` so callers narrow the payload without casts.
 */
export type GitOperationOutcome<T> =
  | { ok: true; result: T; error: null }
  | { ok: false; result: undefined; error: string }

/** Default `extractOutput`: identity for string results, `''` otherwise. */
function defaultExtractOutput(result: unknown): string {
  return typeof result === 'string' ? result : ''
}

/**
 * Normalize an unknown thrown value into a displayable message. Mirrors the
 * `err instanceof Error ? err.message : String(err)` idiom used across the
 * frontend so a non-Error rejection still yields a usable string.
 */
export function gitOperationErrorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/**
 * Run `fn` and record its outcome for `projectId` in the git panel store.
 *
 * On success records `{ kind, label, ok: true, output, error: null }` where
 * `output` is `extractOutput(result)` (or the string result itself) and returns
 * `{ ok: true, result, error: null }`.
 *
 * On failure records `{ kind, label, ok: false, output: '', error }` with the
 * normalized error message, logs it at `logLevel` (an ordinary `'error'` by
 * default, or `'warn'` for the routine per-file ops that pass it explicitly),
 * and returns `{ ok: false, result: undefined, error }`.
 *
 * When `recordSuccess` is false a success is not recorded at all (but is still
 * returned); failures are always recorded.
 *
 * Never throws: a rejected `fn` is captured, not propagated, so the caller's
 * control flow is preserved and it decides what to do with the outcome. The
 * recorded `at` timestamp and `acknowledged: false` reset the banner state for
 * this operation.
 */
export async function runGitOperation<T>({
  projectId,
  kind,
  label,
  fn,
  extractOutput,
  recordSuccess = true,
  logLevel = 'error',
}: RunGitOperationArgs<T>): Promise<GitOperationOutcome<T>> {
  // Capture the project id at call time: the record must land under the
  // project that started the operation, even if the active project changed
  // while `fn` was in flight.
  const pid = projectId
  try {
    const result = await fn()
    if (recordSuccess) {
      const output = extractOutput ? extractOutput(result) : defaultExtractOutput(result)
      useGitPanelStore.getState().recordGitOperation(pid, {
        kind,
        label,
        ok: true,
        output,
        error: null,
        at: Date.now(),
        acknowledged: false,
      })
    }
    return { ok: true, result, error: null }
  } catch (err) {
    const error = gitOperationErrorMessage(err)
    // Severity is chosen explicitly by the call site, not inferred from
    // `recordSuccess` (which governs the console record, not logging): the
    // per-file staging/discard/gitignore ops pass `logLevel: 'warn'` because
    // their failures are routine, user-driven outcomes (a discard on an
    // unchanged file, a stage of an already-staged path). Every other op — remote ops,
    // commit, the batch toolbars — defaults to ERROR: a failed mutation there
    // is a genuine fault, not a normal consequence of what the user clicked.
    const logFailure = logLevel === 'warn' ? logger.warn : logger.error
    logFailure(`gitOperation: "${kind}" failed for project ${pid}:`, err)
    useGitPanelStore.getState().recordGitOperation(pid, {
      kind,
      label,
      ok: false,
      output: '',
      error,
      at: Date.now(),
      acknowledged: false,
    })
    return { ok: false, result: undefined, error }
  }
}
