import {
  autocompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
} from '@codemirror/autocomplete'
import type { Extension } from '@codemirror/state'
import { listSkills } from '@/api/skills'
import { listAgents } from '@/api/agents'
import { listDirectory, getSessionWorkspace } from '@/api/workspace'
import { subscribe } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { fuzzyFilter } from '@/lib/fuzzyMatch'
import type { SkillDescriptor, AgentDescriptor, FileEntry } from '@/types/models'

const DEFAULT_FILE_ICON = '\uf15b'
const DEFAULT_FOLDER_ICON = '\uf07b'

interface FileCompletion extends Completion {
  nerdIcon?: string
  nerdIconColor?: string
}

let skillsCache: SkillDescriptor[] = []
let skillsLoaded = false

let agentsCache: AgentDescriptor[] = []
let agentsLoaded = false

let filesCache: FileEntry[] = []
let filesLoaded = false
let filesCacheRoot = ''
// Timestamp of the last failed listing fetch. While a fetch keeps failing
// (e.g. backend and frontend transiently disagree about the active project),
// every keystroke would otherwise fire a fresh ListDirectory RPC — a busy
// retry storm with no benefit, since the failure persists until the next
// switch. A short cooldown keeps self-healing (retry on the next trigger
// after it expires) while bounding the chatter.
let filesFetchFailedAt = 0
const FILES_FAILURE_COOLDOWN_MS = 1000

// Memoized completion root, keyed by the active session. resolveCompletionRoot
// runs on EVERY completion trigger (each keystroke of an @-query), so the
// GetSessionWorkspace RPC behind it must not: the memo serves repeated
// triggers, and invalidateFilesCache drops it — the transitions that refresh
// the listing caches (project/session switches, file-tree root changes,
// workspace tree changes) are exactly the ones that can change the
// backend-authoritative root or heal a memoized RPC-failure fallback.
let completionRootMemo: { sessionId: string; root: string | null } | null = null

/**
 * Resolve the workspace root for the @-file completion source.
 *
 * The backend is the authority: ListDirectory validates every path against
 * ITS active project, so the completion must ask the backend which root it
 * would accept. GetSessionWorkspace(activeSessionId) returns exactly that —
 * the session's workspace when it belongs to the active project, the active
 * project workspace otherwise — and is therefore immune to the transient
 * frontend/backend desyncs that follow rapid CHAT↔CODE / project / session
 * switches. Under those desyncs a root taken from fileTreeStore alone kept
 * failing containment on every call and @-hints stayed empty until an app
 * restart.
 *
 * Falls back to the file-tree root when no session is active or the RPC
 * fails (e.g. backend has no active project yet). The per-session memo above
 * bounds the RPC to real transitions; the fallback keeps read-live semantics
 * in the no-session case.
 */
async function resolveCompletionRoot(): Promise<string | null> {
  const sessionId = useSessionStore.getState().activeSessionId
  if (!sessionId) {
    // No session ⇒ no backend authority to ask: serve the live file-tree
    // root without memoizing (the store read is already fresh).
    return useFileTreeStore.getState().rootPath
  }
  if (completionRootMemo && completionRootMemo.sessionId === sessionId) {
    return completionRootMemo.root
  }
  let root: string | null = null
  try {
    const ws = await getSessionWorkspace(sessionId)
    if (ws) root = ws
  } catch (err) {
    logger.warn('completion root: GetSessionWorkspace failed; falling back to the file-tree root', err)
  }
  if (root === null) {
    root = useFileTreeStore.getState().rootPath
  }
  completionRootMemo = { sessionId, root }
  return root
}

function invalidateFilesCache() {
  filesLoaded = false
  // The memoized root shares the listing cache's lifecycle: every trigger
  // that can change the backend-authoritative root also refreshes the
  // listings, so dropping both here keeps a stale root — or a memoized
  // RPC-failure fallback — from outliving the transition.
  completionRootMemo = null
}

function invalidateSkillsCache() {
  skillsLoaded = false
}

function invalidateAgentsCache() {
  agentsLoaded = false
}

// Subscribe once to rootPath changes and filesystem/skills events.
let rootSubActive = false
function ensureRootSubscription() {
  if (rootSubActive) return
  // The flag is flipped only AFTER every subscription is registered:
  // subscribe() throws when the Wails runtime is not injected yet, and a
  // premature flag would permanently skip re-registration on later calls,
  // killing cache invalidation (and with it @-completions) for the whole
  // session. This function is fully synchronous, so no re-entrancy is
  // possible before the flag is set.
  let prevRoot = useFileTreeStore.getState().rootPath
  useFileTreeStore.subscribe((s) => {
    if (s.rootPath !== prevRoot) {
      prevRoot = s.rootPath
      invalidateFilesCache()
      invalidateSkillsCache()
      invalidateAgentsCache()
    }
  })
  // Project and session switches change which workspace the backend expects
  // (and which root GetSessionWorkspace resolves to) even when the file-tree
  // root is momentarily unchanged or its writer (FileTreePanel) is unmounted
  // — e.g. collapsed sidebar or a non-explorer workspace tab. The completion
  // cache must not survive those transitions.
  let prevProjectId = useProjectStore.getState().activeProjectId
  useProjectStore.subscribe((s) => {
    if (s.activeProjectId !== prevProjectId) {
      prevProjectId = s.activeProjectId
      invalidateFilesCache()
      invalidateSkillsCache()
      invalidateAgentsCache()
    }
  })
  let prevSessionId = useSessionStore.getState().activeSessionId
  useSessionStore.subscribe((s) => {
    if (s.activeSessionId !== prevSessionId) {
      prevSessionId = s.activeSessionId
      invalidateFilesCache()
      invalidateSkillsCache()
      invalidateAgentsCache()
    }
  })
  // Filesystem changes inside the workspace (including project-local skills
  // and agents under .agents/) invalidate all caches so the next completion
  // request refetches fresh data.
  subscribe('workspace:tree_changed', () => {
    invalidateFilesCache()
    invalidateSkillsCache()
    invalidateAgentsCache()
  })
  // Global skill directory changes (outside the workspace) invalidate the
  // skills cache only — files and agents are unaffected.
  subscribe('skills:changed', () => {
    invalidateSkillsCache()
  })
  // Global Subagent Profile directory changes (outside the workspace)
  // invalidate the agents cache only.
  subscribe('agents:changed', () => {
    invalidateAgentsCache()
  })
  rootSubActive = true
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

async function getAgents(): Promise<AgentDescriptor[]> {
  if (agentsLoaded) return agentsCache
  try {
    agentsCache = await listAgents()
    agentsLoaded = true
  } catch {
    agentsCache = []
  }
  return agentsCache
}

async function getFiles(): Promise<{ entries: FileEntry[]; root: string | null }> {
  const rootPath = await resolveCompletionRoot()
  if (!rootPath) return { entries: [], root: null }
  if (filesLoaded && filesCacheRoot === rootPath) return { entries: filesCache, root: rootPath }
  if (Date.now() - filesFetchFailedAt < FILES_FAILURE_COOLDOWN_MS) return { entries: [], root: rootPath }
  try {
    const entries = await listDirectory(rootPath, true)
    filesCache = entries
    // Cache only non-empty listings. An empty result may be a transient
    // failure — the workspace directory not yet materialized (No Project
    // sessions create it lazily), or an invalid RPC payload degraded to []
    // by the api guard. Caching it would suppress @-completions until the
    // next workspace:tree_changed event or an app restart (the reported
    // "hints stop appearing until restart" bug). Retrying on the next
    // completion trigger self-heals once the directory exists.
    filesLoaded = entries.length > 0
    filesCacheRoot = rootPath
    filesFetchFailedAt = 0
    return { entries, root: rootPath }
  } catch (err) {
    filesCache = []
    filesFetchFailedAt = Date.now()
    // Never silent: a completion root the backend keeps rejecting is exactly
    // the "hints are dead until restart" failure mode, and without a log it
    // is undiagnosable which side of the desync produced it.
    logger.error(`completion: ListDirectory failed for root ${rootPath}`, err)
    return { entries: [], root: rootPath }
  }
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

async function agentSource(ctx: CompletionContext): Promise<CompletionResult | null> {
  // Scan backward for '#' trigger.
  const line = ctx.state.doc.lineAt(ctx.pos)
  const textBefore = line.text.slice(0, ctx.pos - line.from)

  let triggerIdx = -1
  for (let i = textBefore.length - 1; i >= 0; i--) {
    const ch = textBefore[i]
    if (ch === ' ' || ch === '\t') break
    if (ch === '#') {
      if (i === 0 || textBefore[i - 1] === ' ' || textBefore[i - 1] === '\t') {
        triggerIdx = i
      }
      break
    }
  }

  if (triggerIdx === -1) return null

  const from = line.from + triggerIdx + 1
  const query = textBefore.slice(triggerIdx + 1)

  const agents = await getAgents()
  const filtered = fuzzyFilter(query, agents, (a) => a.name)
  if (filtered.length === 0) return null

  return {
    from,
    filter: false,
    options: filtered.map((a) => ({
      label: a.name,
      detail: a.description,
      type: 'keyword',
      apply: a.name + ' ',
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

  const { entries, root: rootPath } = await getFiles()
  if (entries.length === 0) return null

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
    override: [skillSource, agentSource, fileSource],
    closeOnBlur: true,
    activateOnTyping: true,
    icons: false,
    optionClass: (completion) =>
      completion.type === 'keyword' ? 'skill-item' : 'file-item',
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
