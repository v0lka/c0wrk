// Stale-async protection — wraps promises so only the latest call resolves.

import { useCallback, useRef } from 'react'

interface LatestAsync {
  /** Wrap a promise; returns undefined if a newer call has superseded it */
  wrap: <T>(promise: Promise<T>) => Promise<T | undefined>
}

/**
 * Returns a `wrap` function that tracks call order.
 * If `wrap` is called again before a previous promise settles,
 * the older call resolves to `undefined`.
 */
export function useLatestAsync(): LatestAsync {
  const counterRef = useRef(0)

  const wrap = useCallback(<T>(promise: Promise<T>): Promise<T | undefined> => {
    const id = ++counterRef.current
    return promise.then(
      (value) => (counterRef.current === id ? value : undefined),
      (err) => { if (counterRef.current === id) throw err; return undefined },
    )
  }, [])

  return { wrap }
}
