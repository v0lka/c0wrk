import { useEffect, useState } from 'react'
import { getFileIcon } from '@/api/workspace'

type IconEntry = { icon: string; icon_color: string }

const MAX_CACHE_SIZE = 500
const cache = new Map<string, IconEntry>()

/** Read from cache with LRU promotion. */
function cacheGet(key: string): IconEntry | undefined {
  const entry = cache.get(key)
  if (entry) {
    // Move to end (most recently used)
    cache.delete(key)
    cache.set(key, entry)
  }
  return entry
}

/** Write to cache with LRU eviction. */
function cacheSet(key: string, value: IconEntry): void {
  if (cache.has(key)) cache.delete(key)
  cache.set(key, value)
  if (cache.size > MAX_CACHE_SIZE) {
    // Evict least-recently-used (first entry)
    const first = cache.keys().next().value
    if (first !== undefined) cache.delete(first)
  }
}

export function useFileIcon(filePath: string) {
  const [result, setResult] = useState<IconEntry | null>(
    cacheGet(filePath) ?? null,
  )

  useEffect(() => {
    const cached = cacheGet(filePath)
    if (cached) {
      setResult(cached)
      return
    }

    let cancelled = false
    getFileIcon(filePath)
      .then((res) => {
        if (cancelled) return
        cacheSet(filePath, res)
        setResult(res)
      })
      .catch(() => {
        // ignore
      })

    return () => {
      cancelled = true
    }
  }, [filePath])

  return result
}
