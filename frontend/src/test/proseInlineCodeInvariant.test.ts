// @vitest-environment node
//
// Markdown-prose color invariant — inline code, strong, and ol markers
// use --color-foreground. Mapping them onto semantic UI tokens
// (--color-destructive / --color-highlight / --color-warning) turns a
// review full of backticks into a rainbow; custom themes cannot override
// .prose selectors. Syntax highlighting stays on pre > code (hljs).
// See specs/domains/frontend/rendering.md § Markdown Element Handling.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'

const SRC_DIR = fileURLToPath(new URL('..', import.meta.url))
const CSS_PATH = join(SRC_DIR, 'index.css')

function proseTokenBlock(): string {
  const css = readFileSync(CSS_PATH, 'utf8')
  const match = css.match(/\.prose\s*\{[^}]+\}/)
  if (!match) {
    throw new Error('.prose token block missing in index.css')
  }
  return match[0]
}

describe('markdown prose color invariant', () => {
  const prose = proseTokenBlock()

  it('paints inline code, strong, and ol markers with foreground', () => {
    expect(prose).toMatch(/--tw-prose-code:\s*var\(--color-foreground\)/)
    expect(prose).toMatch(/--tw-prose-bold:\s*var\(--color-foreground\)/)
    expect(prose).toMatch(/--tw-prose-counters:\s*var\(--color-foreground\)/)
  })

  it('keeps links as the info affordance and fenced code on foreground', () => {
    expect(prose).toMatch(/--tw-prose-links:\s*var\(--color-info\)/)
    expect(prose).toMatch(/--tw-prose-pre-code:\s*var\(--color-foreground\)/)
  })

  it('does not map prose chrome onto semantic UI colors', () => {
    expect(prose).not.toMatch(/--tw-prose-code:\s*var\(--color-destructive\)/)
    expect(prose).not.toMatch(/--tw-prose-bold:\s*var\(--color-highlight\)/)
    expect(prose).not.toMatch(/--tw-prose-counters:\s*var\(--color-warning\)/)
  })
})

describe('markdown prose size invariant', () => {
  it('uses default prose (14px root), not prose-sm (~12px)', () => {
    const src = readFileSync(join(SRC_DIR, 'lib/markdownConfig.tsx'), 'utf8')
    expect(src).toMatch(/cn\('prose max-w-none'/)
    expect(src).not.toMatch(/prose-sm/)
  })

  it('keeps inline code at 1em so chips match body size', () => {
    const css = readFileSync(CSS_PATH, 'utf8')
    expect(css).toMatch(
      /\.prose :where\(code\):not\(:where\(pre \*\)\) \{[^}]*font-size:\s*1em;/,
    )
  })

  it('does not shrink thought markdown via compact/prose-xs', () => {
    for (const rel of ['components/chat/ThoughtBlock.tsx', 'components/chat/ThoughtGroupBlock.tsx']) {
      const src = readFileSync(join(SRC_DIR, rel), 'utf8')
      expect(src, rel).not.toMatch(/<Markdown\b[^>]*\bcompact\b/)
    }
  })
})
