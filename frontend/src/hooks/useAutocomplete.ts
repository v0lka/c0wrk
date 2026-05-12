// Autocomplete hook for skill (/) and file (@) references in chat input.

import { useState, useRef, useCallback, useEffect } from 'react'
import { listSkills } from '@/api/skills'
import { listDirectory } from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { fuzzyFilter } from '@/lib/fuzzyMatch'
import type { SkillDescriptor, FileEntry } from '@/types/models'

export interface AutocompleteItem {
  type: 'skill' | 'file' | 'directory'
  label: string
  value: string
  description?: string
  pinned?: boolean
}

interface AutocompleteState {
  isOpen: boolean
  triggerType: 'skill' | 'file' | null
  triggerPos: number
  query: string
  selectedIndex: number
  items: AutocompleteItem[]
}

const INITIAL_STATE: AutocompleteState = {
  isOpen: false,
  triggerType: null,
  triggerPos: 0,
  query: '',
  selectedIndex: 0,
  items: [],
}

export function useAutocomplete() {
  const [state, setState] = useState<AutocompleteState>(INITIAL_STATE)
  const skillsCache = useRef<SkillDescriptor[]>([])
  const filesCache = useRef<FileEntry[]>([])
  const skillsLoaded = useRef(false)
  const filesLoaded = useRef(false)

  // Preload skills on first use.
  const ensureSkills = useCallback(async () => {
    if (skillsLoaded.current) return skillsCache.current
    const skills = await listSkills()
    skillsCache.current = skills
    skillsLoaded.current = true
    return skills
  }, [])

  // Preload workspace entries (files + directories) on first use.
  const ensureFiles = useCallback(async () => {
    if (filesLoaded.current) return filesCache.current
    const rootPath = useFileTreeStore.getState().rootPath
    if (!rootPath) return []
    const entries = await listDirectory(rootPath, true)
    filesCache.current = entries
    filesLoaded.current = true
    return filesCache.current
  }, [])

  // Invalidate files cache on workspace tree changes.
  useEffect(() => {
    let prevRoot = useFileTreeStore.getState().rootPath
    const unsub = useFileTreeStore.subscribe((s) => {
      if (s.rootPath !== prevRoot) {
        prevRoot = s.rootPath
        filesLoaded.current = false
      }
    })
    return unsub
  }, [])

  const close = useCallback(() => {
    setState(INITIAL_STATE)
  }, [])

  const handleChange = useCallback(
    async (text: string, cursorPos: number) => {
      // Find trigger character scanning backwards from cursor.
      const before = text.slice(0, cursorPos)
      let triggerIdx = -1
      let triggerChar: '/' | '@' | null = null

      for (let i = before.length - 1; i >= 0; i--) {
        const ch = before[i]
        if (ch === ' ' || ch === '\n' || ch === '\t') break
        if (ch === '/' || ch === '@') {
          // Must be preceded by whitespace or be at start.
          if (i === 0 || ' \n\t'.includes(before[i - 1] ?? '')) {
            triggerIdx = i
            triggerChar = ch
          }
          break
        }
      }

      if (triggerIdx === -1 || triggerChar === null) {
        if (state.isOpen) close()
        return
      }

      const query = before.slice(triggerIdx + 1)
      const triggerType = triggerChar === '/' ? 'skill' : 'file'

      let items: AutocompleteItem[] = []
      if (triggerType === 'skill') {
        const skills = await ensureSkills()
        const filtered = fuzzyFilter(query, skills, (s) => s.name)
        items = filtered.map((s) => ({
          type: 'skill' as const,
          label: s.name,
          value: s.name,
          description: s.description,
        }))
      } else {
        const entries = await ensureFiles()
        const openTabs = useFileViewerStore.getState().openTabs
        const openTabsSet = new Set(openTabs)

        const pinned: AutocompleteItem[] = []
        const rest: AutocompleteItem[] = []

        if (query.length === 0) {
          // Show all open tabs at the top, then fill with remaining entries.
          for (const path of openTabs) {
            const entry = entries.find((e) => e.path === path)
            if (entry) {
              pinned.push({ type: 'file', label: entry.path, value: entry.path, pinned: true })
            }
          }
          const limit = 50 - pinned.length
          for (const f of entries) {
            if (rest.length >= limit) break
            if (!openTabsSet.has(f.path)) {
              rest.push({ type: f.is_dir ? 'directory' : 'file', label: f.path, value: f.path })
            }
          }
        } else {
          // Fuzzy match against last path component (file/dir name).
          const filtered = fuzzyFilter(query, entries, (f) => f.name)
          for (const f of filtered) {
            const item: AutocompleteItem = {
              type: f.is_dir ? 'directory' : 'file',
              label: f.path,
              value: f.path,
              pinned: openTabsSet.has(f.path),
            }
            if (item.pinned) pinned.push(item)
            else rest.push(item)
          }
        }

        items = [...pinned, ...rest]
      }

      setState({
        isOpen: items.length > 0,
        triggerType,
        triggerPos: triggerIdx,
        query,
        selectedIndex: 0,
        items,
      })
    },
    [state.isOpen, close, ensureSkills, ensureFiles],
  )

  /**
   * Handle keyboard events. Returns true if the event was consumed by autocomplete.
   */
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent): boolean => {
      if (!state.isOpen) return false

      if (e.key === 'Escape') {
        e.preventDefault()
        close()
        return true
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setState((s) => ({
          ...s,
          selectedIndex: Math.min(s.selectedIndex + 1, s.items.length - 1),
        }))
        return true
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setState((s) => ({
          ...s,
          selectedIndex: Math.max(s.selectedIndex - 1, 0),
        }))
        return true
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault()
        return true // Selection handled by caller via `select()`
      }
      return false
    },
    [state.isOpen, close],
  )

  /**
   * Apply selection: returns new text with the reference inserted.
   */
  const select = useCallback(
    (index: number, currentText: string): { text: string; cursorPos: number } => {
      const item = state.items[index]
      if (!item) return { text: currentText, cursorPos: currentText.length }

      const prefix = state.triggerType === 'skill' ? '/' : '@'
      // Escape spaces in file/directory paths; append trailing slash for directories.
      let value: string
      if (item.type === 'directory') {
        const dir = item.value.endsWith('/') ? item.value : item.value + '/'
        value = dir.replace(/ /g, '\\ ')
      } else if (item.type === 'file') {
        value = item.value.replace(/ /g, '\\ ')
      } else {
        value = item.value
      }

      const before = currentText.slice(0, state.triggerPos)
      const after = currentText.slice(state.triggerPos + 1 + state.query.length)
      const insertion = prefix + value + ' '
      const newText = before + insertion + after
      const cursorPos = before.length + insertion.length

      close()
      return { text: newText, cursorPos }
    },
    [state, close],
  )

  return {
    isOpen: state.isOpen,
    items: state.items,
    selectedIndex: state.selectedIndex,
    triggerType: state.triggerType,
    handleKeyDown,
    handleChange,
    select,
    close,
  }
}
