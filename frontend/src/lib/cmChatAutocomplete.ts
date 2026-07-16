import {
  autocompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
} from '@codemirror/autocomplete'
import type { Extension } from '@codemirror/state'
import { listSkills } from '@/api/skills'
import { listDirectory } from '@/api/workspace'
import { subscribe } from '@/api/runtime'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { fuzzyFilter } from '@/lib/fuzzyMatch'
import type { SkillDescriptor, FileEntry } from '@/types/models'

const DEFAULT_FILE_ICON = '\uf15b'
const DEFAULT_FOLDER_ICON = '\uf07b'

interface FileCompletion extends Completion {
  nerdIcon?: string
  nerdIconColor?: string
}

let skillsCache: SkillDescriptor[] = []
let skillsLoaded = false

let filesCache: FileEntry[] = []
let filesLoaded = false
let filesCacheRoot = ''

function invalidateFilesCache() {
  filesLoaded = false
}

function invalidateSkillsCache() {
  skillsLoaded = false
}

// Subscribe once to rootPath changes and filesystem/skills events.
let rootSubActive = false
function ensureRootSubscription() {
  if (rootSubActive) return
  rootSubActive = true
  let prevRoot = useFileTreeStore.getState().rootPath
  useFileTreeStore.subscribe((s) => {
    if (s.rootPath !== prevRoot) {
      prevRoot = s.rootPath
      invalidateFilesCache()
      invalidateSkillsCache()
    }
  })
  // Filesystem changes inside the workspace (including project-local skills
  // under .agents/skills/) invalidate both caches so the next completion
  // request refetches fresh data.
  subscribe('workspace:tree_changed', () => {
    invalidateFilesCache()
    invalidateSkillsCache()
  })
  // Global skill directory changes (outside the workspace) invalidate the
  // skills cache only — files are unaffected.
  subscribe('skills:changed', () => {
    invalidateSkillsCache()
  })
}

async function getSkills(): Promise<SkillDescriptor[]> {
  if (skillsLoaded) return skillsCache
  try {
    skillsCache = await listSkills()
    skillsLoaded = true
  } catch {
    skillsCache = []
  }
  return skillsCache
}

async function getFiles(): Promise<FileEntry[]> {
  const rootPath = useFileTreeStore.getState().rootPath
  if (!rootPath) return []
  if (filesLoaded && filesCacheRoot === rootPath) return filesCache
  try {
    filesCache = await listDirectory(rootPath, true)
    filesLoaded = true
    filesCacheRoot = rootPath
  } catch {
    filesCache = []
  }
  return filesCache
}

function relativePath(absPath: string, rootPath: string | null): string {
  if (rootPath && absPath.startsWith(rootPath + '/')) {
    return absPath.slice(rootPath.length + 1)
  }
  return absPath
}

/**
 * Text inserted into the chat input when a file is selected: a path made
 * relative to the workspace root (when the file lives inside it) with spaces
 * escaped, plus the given suffix (e.g. `' '` or `'/'`). Files outside the
 * workspace keep their absolute path.
 */
function chatApplyPath(absPath: string, rootPath: string | null, suffix: string): string {
  return relativePath(absPath, rootPath).replace(/ /g, '\\ ') + suffix
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

  const from = line.from + triggerIdx + 1
  const query = textBefore.slice(triggerIdx + 1)

  const skills = await getSkills()
  const filtered = fuzzyFilter(query, skills, (s) => s.name)
  if (filtered.length === 0) return null

  return {
    from,
    filter: false,
    options: filtered.map((s) => ({
      label: s.name,
      detail: s.description,
      type: 'keyword',
      apply: s.name + ' ',
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

  const from = line.from + triggerIdx + 1
  const query = textBefore.slice(triggerIdx + 1)

  const entries = await getFiles()
  if (entries.length === 0) return null

  const rootPath = useFileTreeStore.getState().rootPath
  const openTabs = useFileViewerStore.getState().openTabs
  const openTabsSet = new Set(openTabs)

  const options: FileCompletion[] = []

  if (query.length === 0) {
    // Show pinned tabs first, then remaining entries.
    for (const path of openTabs) {
      const entry = entries.find((e) => e.path === path)
      if (entry) {
        options.push({
          label: relativePath(entry.path, rootPath),
          type: 'file',
          boost: 10,
          apply: chatApplyPath(entry.path, rootPath, ' '),
          nerdIcon: entry.icon || DEFAULT_FILE_ICON,
          nerdIconColor: entry.icon_color,
        })
      }
    }
    const cap = 50 - options.length
    for (const f of entries) {
      if (options.length >= 50) break
      if (!openTabsSet.has(f.path)) {
        const suffix = f.is_dir ? '/' : ' '
        options.push({
          label: relativePath(f.path, rootPath),
          type: f.is_dir ? 'folder' : 'file',
          apply: chatApplyPath(f.path, rootPath, suffix),
          nerdIcon: f.is_dir ? (f.icon || DEFAULT_FOLDER_ICON) : (f.icon || DEFAULT_FILE_ICON),
          nerdIconColor: f.icon_color,
        })
      }
      if (options.length - openTabs.length >= cap) break
    }
  } else {
    const filtered = fuzzyFilter(query, entries, (f) => f.name)
    for (const f of filtered) {
      const pinned = openTabsSet.has(f.path)
      const suffix = f.is_dir ? '/' : ' '
      options.push({
        label: relativePath(f.path, rootPath),
        type: f.is_dir ? 'folder' : 'file',
        boost: pinned ? 10 : 0,
        apply: chatApplyPath(f.path, rootPath, suffix),
        nerdIcon: f.is_dir ? (f.icon || DEFAULT_FOLDER_ICON) : (f.icon || DEFAULT_FILE_ICON),
        nerdIconColor: f.icon_color,
      })
    }
  }

  if (options.length === 0) return null

  return { from, filter: false, options }
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
    icons: false,
    optionClass: (completion) => (completion.type === 'keyword' ? 'skill-item' : 'file-item'),
    addToOptions: [
      {
        render: (completion: Completion) => {
          const fc = completion as FileCompletion
          if (!fc.nerdIcon) return null
          const span = document.createElement('span')
          span.className = 'cm-completion-nerd-icon'
          span.textContent = fc.nerdIcon
          if (fc.nerdIconColor) {
            span.style.color = fc.nerdIconColor
          }
          return span
        },
        position: 20,
      },
    ],
  })
}
