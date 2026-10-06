import { EditorView } from '@codemirror/view'
import { syntaxHighlighting } from '@codemirror/language'
import type { Extension } from '@codemirror/state'
import { getCSSVar, createOneDarkHighlightStyle } from './cmTheme'

/**
 * Editable One Dark theme for the chat input editor.
 * Visible cursor, no gutters, chat-appropriate font sizing.
 *
 * Colors are resolved from CSS custom properties at call time; callers must
 * re-create (via a Compartment reconfigure) on theme change. `isDark` is
 * forwarded to CodeMirror so the autocomplete tooltip and selection layer use
 * palette-appropriate defaults.
 */
export function createChatEditorTheme(isDark: boolean = true): Extension {
  const fg = getCSSVar('--color-foreground')
  const caret = getCSSVar('--color-info')
  const selection = getCSSVar('--color-muted')

  const theme = EditorView.theme({
    '&': {
      backgroundColor: 'transparent',
      color: fg,
    },
    '.cm-content': {
      caretColor: 'transparent',
      fontSize: 'var(--text-sm)',
      lineHeight: '1.5',
      padding: '0.25rem 0',
    },
    '.cm-cursor, .cm-dropCursor': {
      borderLeftColor: caret,
      borderLeftWidth: '1.5px',
    },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
      backgroundColor: selection,
    },
    '.cm-activeLine': {
      backgroundColor: 'transparent',
    },
    '.cm-gutters': {
      display: 'none',
    },
    '.cm-scroller': {
      overflow: 'auto',
      // The UI font follows the `--font-sans` custom property (the @theme
      // token that fontStore's inline override on <html> repoints), so a
      // font change needs no theme rebuild — the cascade re-resolves the
      // var. The useChatEditor reconfigure on font change exists only to
      // force CodeMirror's lazy height re-measurement.
      fontFamily: 'var(--font-sans)',
    },
    '&.cm-focused': {
      outline: 'none',
    },

    // --- Autocomplete tooltip (/ skills, @ files, # agents) -----------------
    //
    // The tooltip renders in a body-level wrapper (tooltips({parent}) in
    // cmChatExtensions.ts), and CodeMirror mirrors THIS editor's style
    // classes onto that wrapper, so theme entries reach it. They must reach
    // it through here: @codemirror/autocomplete's baseTheme styles the same
    // surface with editor-scoped selectors (`.ͼbase .cm-tooltip.
    // cm-tooltip-autocomplete > ul`, specificity (0,3,1)) that beat any
    // global index.css rule. A plain `.cm-tooltip-autocomplete ul` in
    // index.css loses the cascade, and the hints silently render in CM's
    // `font-family: monospace` — the bug that moved this block here. Theme
    // entries carry the same specificity as baseTheme and win on mount
    // order (themes mount at editor creation, after the import-time base
    // theme) — the same mechanism that lets `.cm-scroller` above override
    // the base `font-family: monospace`. Guarded by
    // test/cmTooltipCascade.test.ts, which resolves the live cascade
    // winner. Keep contested tooltip styling in THIS theme: the same
    // selector in index.css is dead code.
    '.cm-tooltip.cm-tooltip-autocomplete': {
      backgroundColor: 'var(--color-popover)',
      border: '1px solid var(--color-border)',
      borderRadius: '0.375rem',
      // Tailwind shadow-md — the same elevation every project dropdown carries
      // (DropdownMenuContent, the toolbar combobox portals), not a custom one.
      boxShadow: '0 4px 6px -1px rgb(0 0 0 / 0.1), 0 2px 4px -2px rgb(0 0 0 / 0.1)',
      overflow: 'hidden',
    },
    '.cm-tooltip.cm-tooltip-autocomplete > ul': {
      // Suggestions are UI chrome, not code: they ride the interface font
      // chain, exactly like the rest of the chat UI — a mono pick in
      // Settings must not leak into this list (and CM's base `monospace`
      // literal must not either). The nerd-font item icons keep their own
      // --font-icon (index.css, uncontested).
      fontFamily: 'var(--font-sans)',
      fontSize: 'var(--text-xs)',
      // Both the / and @ lists open upward over the chat history, so the list
      // gets room to read instead of squeezing against the input's bottom edge.
      maxHeight: '480px',
    },
    '.cm-tooltip.cm-tooltip-autocomplete > ul > li': {
      padding: 'calc(0.25rem + 2px) calc(0.5rem + 2px)',
      // Explicit color so suggestion text never blends into the popover
      // background; without it CM's dark/light base theme text color can
      // collide with --color-popover.
      color: 'var(--color-popover-foreground)',
    },
    '.cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]': {
      backgroundColor: 'var(--color-muted)',
      color: 'var(--color-foreground)',
    },
    // Pointer hover mirrors DropdownMenuItem's focus:bg-muted/50 tint, but is
    // excluded from the keyboard-selected row so the selection stays visually
    // stronger (full --color-muted above).
    '.cm-tooltip.cm-tooltip-autocomplete > ul > li:not([aria-selected]):hover': {
      backgroundColor: 'color-mix(in srgb, var(--color-muted) 50%, transparent)',
    },
    // The truncation ellipsis must read as part of the description. The CM base
    // theme paints a row's text-overflow ellipsis in the li's own color, so the
    // li carries the 30% popover-foreground description tint for the three
    // description-carrying kinds; the label rules in index.css re-pin their
    // full-strength colors, so only the ellipsis and detail dim.
    '.cm-tooltip.cm-tooltip-autocomplete > ul > li.agent-item': {
      color: 'color-mix(in srgb, var(--color-popover-foreground) 30%, transparent)',
    },
    '.cm-tooltip.cm-tooltip-autocomplete > ul > li.skill-item': {
      color: 'color-mix(in srgb, var(--color-popover-foreground) 30%, transparent)',
    },
    '.cm-tooltip.cm-tooltip-autocomplete > ul > li.mcp-item': {
      color: 'color-mix(in srgb, var(--color-popover-foreground) 30%, transparent)',
    },
    '.cm-completionMatchedText': {
      textDecoration: 'none',
      backgroundColor: 'color-mix(in srgb, var(--color-foreground) 25%, transparent)',
      borderRadius: '2px',
    },
    '.cm-completionDetail': {
      fontStyle: 'normal',
      marginLeft: '0.5rem',
    },
  }, { dark: isDark })

  return [theme, syntaxHighlighting(createOneDarkHighlightStyle())]
}
