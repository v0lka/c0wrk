import { useCallback, useRef, useState } from 'react'
import { dumpVectorIndexDiagnostics } from '@/api/vector'
import { emit } from '@/api/runtime'
import { generateUUID } from '@/lib/ids'

/**
 * The "Collect diagnostics" action for a stalled indexing pass (the
 * status-bar pill's stalled overlay). One click fires
 * DumpVectorIndexDiagnostics: the backend pairs the live status snapshot
 * with the capped all-goroutine stack dump the pass-stall watchdog would log
 * as evidence and writes the bundle under ~/.c0wrk/diagnostics/. The hook
 * owns the busy latch (no duplicate RPC while one is in flight — the latch
 * lives in a ref, so even two clicks inside one render window fire exactly
 * one RPC), resolves with the written file's path, and surfaces a rejection
 * through the app-wide runtime_error toast — a click that only logs reads as
 * a broken action (the useVectorReindex precedent). The action is
 * deliberately not gated on anything but the in-flight latch: the RPC itself
 * carries the routing verdicts (No Project / embedder / shutdown) and names
 * the real cause on rejection.
 */
export function useVectorDiagnosticsDump() {
  const [collecting, setCollecting] = useState(false)
  // The airtight half of the latch: state updates only land after a
  // re-render, so a second click before that re-render would still read the
  // stale `collecting` closure. The ref closes that window.
  const collectingRef = useRef(false)
  // The bundle the last successful collection wrote. Rendered as the
  // button's follow-up title ("saved to …") so the path is visible without
  // opening anything; a fresh click overwrites it.
  const [savedPath, setSavedPath] = useState<string | null>(null)

  const collect = useCallback(() => {
    if (collectingRef.current) return
    collectingRef.current = true
    setCollecting(true)
    setSavedPath(null)
    dumpVectorIndexDiagnostics()
      .then((res) => {
        setSavedPath(res.path)
        collectingRef.current = false
        setCollecting(false)
      })
      .catch((err: unknown) => {
        collectingRef.current = false
        setCollecting(false)
        const message = err instanceof Error ? err.message : String(err)
        // generateUUID, never the raw crypto.randomUUID: this UI is also
        // served over plain HTTP on non-loopback addresses by the
        // web-access sidecar (ADR-101), where randomUUID is absent and the
        // raw call would throw INSIDE the catch — no toast, an unhandled
        // rejection.
        emit('runtime_error', {
          id: generateUUID(),
          message: `Collect diagnostics failed: ${message}`,
        })
      })
  }, [])

  return { collecting, savedPath, collect }
}
