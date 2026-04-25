import hljs from 'highlight.js/lib/core'

import markdown from 'highlight.js/lib/languages/markdown'
import go from 'highlight.js/lib/languages/go'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import python from 'highlight.js/lib/languages/python'
import rust from 'highlight.js/lib/languages/rust'
import ruby from 'highlight.js/lib/languages/ruby'
import bash from 'highlight.js/lib/languages/bash'
import css from 'highlight.js/lib/languages/css'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'
import json from 'highlight.js/lib/languages/json'
import sql from 'highlight.js/lib/languages/sql'
import diff from 'highlight.js/lib/languages/diff'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import plaintext from 'highlight.js/lib/languages/plaintext'

let registered = false

/**
 * registerLanguages registers all highlight.js languages used by the app.
 * Must be called once at app startup before any highlighting is performed.
 */
export function registerLanguages(): void {
  if (registered) return
  registered = true

  hljs.registerLanguage('markdown', markdown)
  hljs.registerLanguage('go', go)
  hljs.registerLanguage('javascript', javascript)
  hljs.registerLanguage('typescript', typescript)
  hljs.registerLanguage('python', python)
  hljs.registerLanguage('rust', rust)
  hljs.registerLanguage('ruby', ruby)
  hljs.registerLanguage('bash', bash)
  hljs.registerLanguage('css', css)
  hljs.registerLanguage('xml', xml)
  hljs.registerLanguage('yaml', yaml)
  hljs.registerLanguage('json', json)
  hljs.registerLanguage('sql', sql)
  hljs.registerLanguage('diff', diff)
  hljs.registerLanguage('dockerfile', dockerfile)
  hljs.registerLanguage('plaintext', plaintext)
}


