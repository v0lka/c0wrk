import { resolveChatWorkspaceRoot } from '@/lib/chatWorkspaceRoot'
import { isAbsolutePath, normalizePath } from '@/lib/localFileLink'

/**
 * Extracts a string `path` argument from a tool call's parsed args (falling
 * back to parsing the raw JSON args), or undefined when absent. Tool bodies
 * use it as the base directory their result entries are relative to
 * (list_directory's listed dir, glob's search root).
 */
export function extractToolPathArg(
  parsedArgs?: Record<string, unknown>,
  args?: string,
): string | undefined {
  let parsed = parsedArgs
  if (!parsed && args) {
    try { parsed = JSON.parse(args) } catch { return undefined }
  }
  const path = parsed?.path
  return typeof path === 'string' && path !== '' ? path : undefined
}

/**
 * Builds the async path resolver FileLink accepts for tool-emitted paths.
 * Absolute inputs — POSIX (`/...`), Windows drive (`C:\...`) and UNC
 * (`\\srv\share\...`) — pass through normalized (never joined onto any
 * base). Relative paths are joined onto `baseDir` first when the tool call
 * carries one (e.g. list_directory's `path` or glob's search root: result
 * entries are relative to THAT directory, not to the workspace), and the
 * result joins the chat workspace root. A transient missing root returns
 * the (joined) input unchanged, matching the historical open-raw behavior.
 */
export function makeWorkspacePathResolver(
  baseDir?: string,
): (path: string) => Promise<string | null> {
  return async (path: string) => {
    if (isAbsolutePath(path)) return normalizePath('', path)
    const root = await resolveChatWorkspaceRoot()
    const joined = baseDir ? `${baseDir.replace(/\\/g, '/')}/${path}` : path
    return root ? normalizePath(root, joined) : joined
  }
}
