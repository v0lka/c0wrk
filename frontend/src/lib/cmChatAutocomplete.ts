import { autocompletion, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete'
import type { Extension } from '@codemirror/state'
import { listSkills } from '@/api/skills'
import { listDirectory } from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { fuzzyFilter } from '@/lib/fuzzyMatch'
import type { SkillDescriptor, FileEntry } from '@/types/models'

let skillsCache: SkillDescriptor[] = []
let skillsLoaded = false

let filesCache: FileEntry[] = []
let filesLoaded = false
let filesCacheRoot = ''

function invalidateFilesCache() {
  filesLoaded = false
}

// Subscribe once to rootPath changes.
let rootSubActive = false
function ensureRootSubscription() {
  if (rootSubActive) return
  rootSubActive = true
  let prevRoot = useFileTreeStore.getState().rootPath
  useFileTreeStore.subscribe((s) => {
    if (s.rootPath !== prevRoot) {
      prevRoot = s.rootPath
      invalidateFilesCache()
    }
  })
}

async function getSkills(): Promise<SkillDescriptor[]> {
  if (skillsLoaded) return skillsCache
  skillsCache = await listSkills()
  skillsLoaded = true
  return skillsCache
}

async function getFiles(): Promise<FileEntry[]> {
  const rootPath = useFileTreeStore.getState().rootPath
  if (!rootPath) return []
  if (filesLoaded && filesCacheRoot === rootPath) return filesCache
  filesCache = await listDirectory(rootPath, true)
  filesLoaded = true
  filesCacheRoot = rootPath
  return filesCache
}

async function skillSource(ctx: CompletionContext): Promise<CompletionResult | null> {
  // Scan backward for '/' trigger.
  const line = ctx.state.doc.lineAt(ctx.pos)
  const textBefore = line.text.slice(0, ctx.pos - line.from)

  let triggerIdx = -1
  for (let i = textBefore.length - 1; i >= 0; i--) {
    const ch = textBefore[i]
    if (ch === ' ' || ch === '\t') break
    if (ch === '/') {
      if (i === 0 || textBefore[i - 1] === ' ' || textBefore[i - 1] === '\t') {
        triggerIdx = i
      }
      break
    }
  }

  if (triggerIdx === -1) return null

  const from = line.from + triggerIdx
  const query = textBefore.slice(triggerIdx + 1)

  const skills = await getSkills()
  const filtered = fuzzyFilter(query, skills, (s) => s.name)
  if (filtered.length === 0) return null

  return {
    from,
    options: filtered.map((s) => ({
      label: '/' + s.name,
      detail: s.description,
      type: 'keyword',
      apply: '/' + s.name + ' ',
    })),
  }
}

async function fileSource(ctx: CompletionContext): Promise<CompletionResult | null> {
  ensureRootSubscription()

  const line = ctx.state.doc.lineAt(ctx.pos)
  const textBefore = line.text.slice(0, ctx.pos - line.from)

  let triggerIdx = -1
  for (let i = textBefore.length - 1; i >= 0; i--) {
    const ch = textBefore[i]
    if (ch === ' ' || ch === '\n' || ch === '\t') break
    if (ch === '@') {
      if (i === 0 || textBefore[i - 1] === ' ' || textBefore[i - 1] === '\t') {
        triggerIdx = i
      }
      break
    }
  }

  if (triggerIdx === -1) return null

  const from = line.from + triggerIdx
  const query = textBefore.slice(triggerIdx + 1)

  const entries = await getFiles()
  if (entries.length === 0) return null

  const openTabs = useFileViewerStore.getState().openTabs
  const openTabsSet = new Set(openTabs)

  const options: Array<{ label: string; type: string; boost?: number; apply: string }> = []

  if (query.length === 0) {
    // Show pinned tabs first, then remaining entries.
    for (const path of openTabs) {
      const entry = entries.find((e) => e.path === path)
      if (entry) {
        options.push({
          label: '@' + entry.path,
          type: 'file',
          boost: 10,
          apply: '@' + entry.path.replace(/ /g, '\\ ') + ' ',
        })
      }
    }
    const limit = 50 - options.length
    for (const f of entries) {
      if (options.length >= 50) break
      if (!openTabsSet.has(f.path)) {
        const suffix = f.is_dir ? '/' : ' '
        options.push({
          label: '@' + f.path,
          type: f.is_dir ? 'folder' : 'file',
          apply: '@' + f.path.replace(/ /g, '\\ ') + suffix,
        })
      }
      if (options.length - openTabs.length >= limit) break
    }
  } else {
    const filtered = fuzzyFilter(query, entries, (f) => f.name)
    for (const f of filtered) {
      const pinned = openTabsSet.has(f.path)
      const suffix = f.is_dir ? '/' : ' '
      options.push({
        label: '@' + f.path,
        type: f.is_dir ? 'folder' : 'file',
        boost: pinned ? 10 : 0,
        apply: '@' + f.path.replace(/ /g, '\\ ') + suffix,
      })
    }
  }

  if (options.length === 0) return null

  return { from, options }
}

/**
 * CodeMirror autocomplete extension configured with /skill and @file sources.
 */
export function createChatAutocomplete(): Extension {
  ensureRootSubscription()
  return autocompletion({
    override: [skillSource, fileSource],
    closeOnBlur: true,
    activateOnTyping: true,
  })
}
