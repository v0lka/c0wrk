// Polls the desktop process's resident memory (RSS) for the status-bar
// indicator. One immediate sample on mount, then one every
// PROCESS_MEMORY_POLL_MS. Failures are silent: the last good sample is kept
// (null when none was ever taken), so an absent Wails runtime (vitest,
// dev-frontend) or a transient RPC failure simply leaves the indicator
// hidden instead of flashing or erroring.

import { useEffect, useState } from 'react'
import { getProcessMemory } from '@/api/system'

export const PROCESS_MEMORY_POLL_MS = 5000

export function useProcessMemory(): number | null {
  const [rssBytes, setRssBytes] = useState<number | null>(null)

  useEffect(() => {
    let cancelled = false

    const poll = () => {
      getProcessMemory()
        .then((bytes) => {
          if (!cancelled) setRssBytes(bytes)
        })
        .catch(() => {
          // Silent by design — see the hook comment. In particular this is
          // the expected path when window.go is unavailable.
        })
    }

    poll()
    const id = setInterval(poll, PROCESS_MEMORY_POLL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  return rssBytes
}
