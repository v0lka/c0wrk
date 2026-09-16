// Tests for lib/paperAnchors.ts — E1 anchor resolution. The point of these is
// the DEGRADATION contract: an anchor that cannot be located confidently must
// resolve to null (the UI shows "не найдено") rather than jumping to a
// plausible-looking wrong line.

import { describe, it, expect } from 'vitest'
import type { PaperAnchor } from '@/api/papers'
import { classifyAnchor, resolveAnchor, sectionHeadingMatches } from './paperAnchors'

const SOURCE = `# Attention Is All You Need

## Abstract

The dominant sequence transduction models are complex.

## 1 Introduction

Recurrent models have been the standard.

## 3 Model Architecture

Most competitive neural sequence models have this structure.

### 3.1 Encoder and Decoder Stacks

The encoder is composed of a stack of identical layers.

## 4 Why Self-Attention

We compare self-attention to recurrent layers.

Figure 2: The Transformer architecture.

Table 2: BLEU scores on WMT14.

Equation (1): the scaled dot-product attention.
`

function anchor(label: string, ref: string, note = ''): PaperAnchor {
  return { label, ref, note }
}

describe('classifyAnchor', () => {
  it('recognises sections, figures, tables and equations', () => {
    expect(classifyAnchor('§3')).toEqual({ kind: 'section', token: '3' })
    expect(classifyAnchor('Sec. 3.2')).toEqual({ kind: 'section', token: '3.2' })
    expect(classifyAnchor('Section 4')).toEqual({ kind: 'section', token: '4' })
    expect(classifyAnchor('Fig 2')).toEqual({ kind: 'figure', token: '2' })
    expect(classifyAnchor('Figure 2')).toEqual({ kind: 'figure', token: '2' })
    expect(classifyAnchor('Tab. 3')).toEqual({ kind: 'table', token: '3' })
    expect(classifyAnchor('Table 3')).toEqual({ kind: 'table', token: '3' })
    expect(classifyAnchor('Eq 5')).toEqual({ kind: 'equation', token: '5' })
    expect(classifyAnchor('Equation (5)')).toEqual({ kind: 'equation', token: '5' })
  })

  it('refuses ambiguous references (no false jumps)', () => {
    expect(classifyAnchor('3')).toBeNull()
    expect(classifyAnchor('3.2')).toBeNull()
    expect(classifyAnchor('p. 12')).toBeNull()
    expect(classifyAnchor('Table')).toBeNull()
    expect(classifyAnchor('Figure')).toBeNull()
    expect(classifyAnchor('')).toBeNull()
  })

  it('treats a free-form string as a verbatim quote', () => {
    expect(classifyAnchor('scaled dot-product attention')).toEqual({
      kind: 'text',
      token: 'scaled dot-product attention',
    })
    // A word that merely STARTS with a keyword is not a structural anchor.
    expect(classifyAnchor('securely results')).toEqual({
      kind: 'text',
      token: 'securely results',
    })
  })
})

describe('classifyAnchor — sub-labels and algorithms (Issues 41 / 100)', () => {
  it('keeps a figure/table sub-label suffix as part of the token', () => {
    expect(classifyAnchor('Fig. 2a')).toEqual({ kind: 'figure', token: '2a' })
    expect(classifyAnchor('Figure 2a')).toEqual({ kind: 'figure', token: '2a' })
    expect(classifyAnchor('Tab. 3b')).toEqual({ kind: 'table', token: '3b' })
    expect(classifyAnchor('Table 3b')).toEqual({ kind: 'table', token: '3b' })
  })

  it('recognises an algorithm anchor (Alg. N / Algorithm N)', () => {
    expect(classifyAnchor('Alg. 2')).toEqual({ kind: 'algorithm', token: '2' })
    expect(classifyAnchor('Alg 2')).toEqual({ kind: 'algorithm', token: '2' })
    expect(classifyAnchor('Algorithm 2')).toEqual({ kind: 'algorithm', token: '2' })
  })

  it('refuses a bare algorithm keyword (no false jump)', () => {
    expect(classifyAnchor('Algorithm')).toBeNull()
    // "algorithmic" merely STARTS with the keyword.
    expect(classifyAnchor('algorithmic complexity')).toEqual({
      kind: 'text',
      token: 'algorithmic complexity',
    })
  })
})

describe('resolveAnchor — sub-labels, sub-numbers and algorithms (Issues 41 / 100 / 106)', () => {
  it('resolves a sub-labelled figure/table to its own caption', () => {
    expect(resolveAnchor('Figure 2a: Overview\n\nFigure 2: Main', anchor('', 'Fig. 2a'))!.line).toBe(0)
    expect(resolveAnchor('Table 3b: Results.', anchor('', 'Table 3b'))!.line).toBe(0)
  })

  it('does not jump from Fig. 2 to a sub-numbered float (Issue 106)', () => {
    expect(resolveAnchor('Figure 2.1: sub one\n\nFigure 2: main', anchor('', 'Fig. 2'))!.line).toBe(2)
    expect(resolveAnchor('Table 3.1: a\n\nTable 3: b', anchor('', 'Tab. 3'))!.line).toBe(2)
    expect(resolveAnchor('Equation (1.2): a\n\nEquation (1): b', anchor('', 'Eq. 1'))!.line).toBe(2)
  })

  it('does not alias a bare Fig. 2 to a sub-labelled Figure 2a', () => {
    expect(resolveAnchor('Figure 2a: Overview', anchor('', 'Fig. 2'))).toBeNull()
  })

  it('resolves an algorithm anchor and refuses a bare algorithm keyword', () => {
    const src = '# Paper\n\nAlgorithm 2: Training loop\n\nAlgorithm 1: Inference\n\nFigure 3: Results\n'
    expect(resolveAnchor(src, anchor('', 'Alg. 2'))!.line).toBe(2)
    expect(resolveAnchor(src, anchor('', 'Algorithm 1'))!.line).toBe(4)
    expect(resolveAnchor(src, anchor('', 'Algorithm'))).toBeNull()
  })
})

describe('sectionHeadingMatches', () => {
  it('matches a section number but not its subsections', () => {
    expect(sectionHeadingMatches('3. Method', '3')).toBe(true)
    expect(sectionHeadingMatches('3 Introduction', '3')).toBe(true)
    expect(sectionHeadingMatches('3', '3')).toBe(true)
    expect(sectionHeadingMatches('3.1 Encoder', '3')).toBe(false)
    expect(sectionHeadingMatches('30 Things', '3')).toBe(false)
  })

  it('matches a multi-level number exactly', () => {
    expect(sectionHeadingMatches('3.1 Encoder', '3.1')).toBe(true)
    expect(sectionHeadingMatches('3.10 Later', '3.1')).toBe(false)
  })
})

describe('resolveAnchor', () => {
  it('resolves a section anchor to its heading line', () => {
    const hit = resolveAnchor(SOURCE, anchor('sec3', '§3'))
    expect(hit).not.toBeNull()
    expect(hit!.kind).toBe('section')
    expect(SOURCE.split('\n')[hit!.line]).toBe('## 3 Model Architecture')
  })

  it('resolves a subsection without matching the parent section', () => {
    const hit = resolveAnchor(SOURCE, anchor('sec3.1', '§3.1'))
    expect(SOURCE.split('\n')[hit!.line]).toBe('### 3.1 Encoder and Decoder Stacks')
  })

  it('resolves a figure and a table caption', () => {
    const fig = resolveAnchor(SOURCE, anchor('fig2', 'Figure 2'))
    expect(SOURCE.split('\n')[fig!.line]).toBe('Figure 2: The Transformer architecture.')
    const tab = resolveAnchor(SOURCE, anchor('tab2', 'Table 2'))
    expect(SOURCE.split('\n')[tab!.line]).toBe('Table 2: BLEU scores on WMT14.')
  })

  it('resolves an equation reference', () => {
    const hit = resolveAnchor(SOURCE, anchor('eq1', 'Eq. 1'))
    expect(SOURCE.split('\n')[hit!.line]).toBe('Equation (1): the scaled dot-product attention.')
  })

  it('resolves a verbatim quote', () => {
    const hit = resolveAnchor(SOURCE, anchor('', 'scaled dot-product attention'))
    expect(hit!.kind).toBe('text')
  })

  it('uses the label when the ref does not resolve', () => {
    const hit = resolveAnchor(SOURCE, anchor('sec4', '§9'))
    expect(SOURCE.split('\n')[hit!.line]).toBe('## 4 Why Self-Attention')
  })

  it('degrades honestly — no hit rather than a wrong jump', () => {
    // A missing section: the source has no section 9.
    expect(resolveAnchor(SOURCE, anchor('sec9', '§9'))).toBeNull()
    // A page pointer has no target in the extracted text.
    expect(resolveAnchor(SOURCE, anchor('p12', 'p. 12'))).toBeNull()
    // A bare number is too ambiguous.
    expect(resolveAnchor(SOURCE, anchor('sec', '7'))).toBeNull()
    // A too-short quote is refused.
    expect(resolveAnchor(SOURCE, anchor('', 'ab'))).toBeNull()
    // A quote that simply is not there.
    expect(resolveAnchor(SOURCE, anchor('', 'nonexistent quotation here'))).toBeNull()
  })

  it('never uses the free-form note as a locator', () => {
    expect(resolveAnchor(SOURCE, anchor('', '', '§3'))).toBeNull()
  })

  it('returns null for an empty source or a fully empty anchor', () => {
    expect(resolveAnchor('', anchor('sec3', '§3'))).toBeNull()
    expect(resolveAnchor(SOURCE, anchor('', ''))).toBeNull()
  })
})
