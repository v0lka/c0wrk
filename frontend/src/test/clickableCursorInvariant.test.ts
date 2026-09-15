// @vitest-environment node
//
// Cursor policy invariant — project-wide guard.
//
// Tailwind v4's Preflight leaves every element at the UA default cursor
// (`default` arrow for buttons/links, text caret for selects), so an
// unstyled clickable element gives no pointer affordance at all. The app's
// cursor policy therefore lives as ONE base-layer rule in index.css:
// enabled interactive elements → `cursor: pointer`, disabled →
// `cursor: not-allowed` (see "Cursor Policy" there). Utilities still win in
// the cascade, which is exactly how intentional exceptions (resize handles,
// drag canvases) opt out.
//
// This guard has two halves:
//
// 1. The policy rule must exist in index.css. A refactor that drops or
//    splits the rule (e.g. renames the layer, narrows the selector list)
//    fails here instead of silently reverting every button to the arrow.
//
// 2. `cursor-default` must not appear in component sources. It was the
//    pre-policy idiom inherited from shadcn (menu items etc.) and — as a
//    utility — it BEATS the base-layer pointer rule, so a single stray
//    re-introduction turns a clickable element back into an arrow without
//    any other signal. The only sanctioned occurrences are the tooltip
//    hover rows allowlisted below (elements that show a tooltip but have no
//    click behaviour — a pointer there would promise interaction).

import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, relative } from 'node:path'

const SRC_DIR = fileURLToPath(new URL('..', import.meta.url))
const INDEX_CSS = join(SRC_DIR, 'index.css')

/** Files allowed to contain `cursor-default`, with the EXACT occurrence
 *  count they must contain. Exact counts keep the list self-maintaining:
 *  removing an allowlisted row (or adding a new one) fails the guard, so
 *  the entry has to be consciously updated.
 *
 *  - BlackboardPanel.tsx: StepTooltip/StepResultTooltip hover rows — they
 *    display a richer tooltip on hover but have NO click behaviour, so the
 *    arrow cursor is the honest affordance there. */
const CURSOR_DEFAULT_ALLOWLIST: Record<string, number> = {
  'components/chat/BlackboardPanel.tsx': 4,
}

/** Recursively collect `.ts`/`.tsx` sources, excluding test files. */
function collectSources(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...collectSources(full))
    } else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      out.push(full)
    }
  }
  return out
}

/** Count `needle` occurrences in `haystack`. */
function countOccurrences(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1
}

describe('cursor policy invariant', () => {
  it('index.css carries the base-layer pointer policy for interactive elements', () => {
    const css = readFileSync(INDEX_CSS, 'utf8')

    // The rule lives in @layer base — anything else lets utilities that
    // should NOT win (e.g. a plain `cursor-pointer`-less reset elsewhere)
    // reorder unexpectedly.
    expect(css).toContain('@layer base')

    // Core selector families: native buttons, ARIA widgets, form controls.
    // Checking a representative subset of each family is enough to detect a
    // narrowed/renamed rule; the full list is reviewed in the rule itself.
    for (const selector of [
      'button:not(:disabled)',
      'select:not(:disabled)',
      'label:not(:disabled)',
      '[role="button"]:not([aria-disabled="true"])',
      '[role="menuitem"]:not([aria-disabled="true"])',
      '[role="tab"]:not([aria-disabled="true"])',
      '[role="option"]:not([aria-disabled="true"])',
      '[role="switch"]:not([aria-disabled="true"])',
    ]) {
      expect(css, `missing selector: ${selector}`).toContain(selector)
    }

    expect(css).toContain('cursor: pointer')
  })

  it('index.css carries the not-allowed policy for disabled controls', () => {
    const css = readFileSync(INDEX_CSS, 'utf8')

    for (const selector of ['button:disabled', 'input:disabled', 'select:disabled', '[aria-disabled="true"]']) {
      expect(css, `missing disabled selector: ${selector}`).toContain(selector)
    }
    expect(css).toContain('cursor: not-allowed')
  })

  it('no `cursor-default` outside the allowlist (it defeats the pointer base rule)', () => {
    const sources = collectSources(SRC_DIR)
    expect(sources.length).toBeGreaterThan(0)

    const violations: string[] = []
    const seenAllowlist = new Set<string>()

    for (const file of sources) {
      const rel = relative(SRC_DIR, file)
      const count = countOccurrences(readFileSync(file, 'utf8'), 'cursor-default')
      if (count === 0) continue

      const allowed = CURSOR_DEFAULT_ALLOWLIST[rel]
      if (allowed === undefined) {
        violations.push(`${rel}: ${count} occurrence(s), not allowlisted`)
      } else {
        seenAllowlist.add(rel)
        if (count !== allowed) {
          violations.push(
            `${rel}: expected exactly ${allowed} occurrence(s) per allowlist, found ${count}` +
              (count > allowed ? ' — new cursor-default must be justified and allowlisted' : ' — stale allowlist entry, update the count'),
          )
        }
      }
    }

    const stale = Object.keys(CURSOR_DEFAULT_ALLOWLIST).filter((k) => !seenAllowlist.has(k))
    for (const rel of stale) {
      violations.push(`${rel}: allowlisted but contains no cursor-default — remove the stale entry`)
    }

    expect(violations, violations.join('\n')).toEqual([])
  })
})
