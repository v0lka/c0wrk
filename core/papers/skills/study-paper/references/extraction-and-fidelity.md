# Extraction & Fidelity

> **Usage.** Load this whenever a source enters the workflow — before you read,
> and again before you report. It tells you how to turn any form of a paper (PDF,
> scanned image, HTML, LaTeX source, or a pasted abstract) into a structured view
> — sections, figures, tables, equations — using generic means, and how to flag
> honestly when that view is degraded.
>
> **Prime rule: never silently repair.** Do not "fix" a garbled equation, guess a
> missing number, or paraphrase an unreadable table cell as if it were legible.
> Reconstruct what the source provides; **mark** what it does not; lower
> confidence on anything affected. A clean-looking output built on silent guesses
> is worse than an obviously partial one.

## Why fidelity is a first-class concern

Every downstream claim inherits the quality of the text it came from. If the
extraction reorders two-column text, drops a minus sign, or blurs a decimal
point, the analyst can report the *opposite* of what the paper says — and the
error is invisible unless it is flagged. Fidelity handling is therefore not
housekeeping; it is part of the evidence trail, alongside the source anchors.

Two outputs always travel together:

1. The **structured view** — the paper as sections, figures, tables, equations,
   and reference anchors.
2. The **fidelity note** — what was extracted cleanly, what was reconstructed or
   inferred, what is uncertain, and how that limits any conclusion.

## Step 1 — Identify the source form

Establish what you actually have before extracting from it. The form caps the
achievable fidelity.

| Source form | Typical fidelity | Notes |
| --- | --- | --- |
| LaTeX / e-print source | **Highest** | Equations and structure are explicit; the ground truth. |
| Publisher HTML / full-text | **High** | Semantic structure (headings, MathML) is usually preserved. |
| Digital PDF with a text layer | **Medium** | Layout survives; reading order and math can break. |
| Scanned / image-only PDF | **Low** | Needs OCR; characters and symbols are lost. |
| Pasted abstract / partial text | **Very low** | No method, figures, tables, or references. |
| Citation / metadata only | **None for content** | Enough to locate the paper, not to read it. |

State the form in one line, and if the request needs content the form cannot
supply (e.g. "explain the method" from an abstract), say so and offer to obtain a
fuller source.

## Step 2 — Prefer the cleanest available representation

Extraction quality is decided mostly by *which* representation you start from, so
escalate whenever a cleaner one is reachable:

- For a preprint, the **author LaTeX source or the HTML full text** beats the PDF:
  equations survive as markup instead of being re-parsed from glyphs.
- For a published paper, the **publisher HTML** usually beats the PDF for
  structure, while the PDF remains the authority for exact layout and pagination.
- If only a PDF exists, confirm it has a **text layer** before extracting. If not,
  it is an image problem (Step 4, OCR) — not a text problem.

Record which representation you used; it is the first line of the fidelity note.

## Step 3 — Reconstruct structure generically

You do not need a specific utility to impose structure; you need consistent
signals. Identify each element by its **cue**, then anchor it.

### Sections and headings
Heading cues: numbered or named headings (`1`, `1.1`, `Introduction`, `Methods`,
`§3.2`), typographic emphasis (larger/bolded lines), and vertical whitespace.
Build a section tree and use the paper's own numbering as the anchor target.

### Figures
A figure is **caption + image + in-figure text**. The caption (`Figure N:`) is the
reliable anchor and the usual carrier of meaning; the image itself is opaque to
text extraction. Treat any text that lives *inside* a figure (axis labels,
legend keys, annotations) as needing OCR or visual reading — never assume the
caption conveys it.

### Tables
Tables are the most layout-dependent element. Cue on captions (`Table N:`) and on
grid structure (aligned columns, rules). Reconstruct as a labelled grid, not as a
stream of words. Watch for: cells that wrap across lines, multi-row/multi-column
headers, units declared in the caption or header, spans, and footnotes attached by
symbols (`*`, `†`).

### Equations
Display equations appear as their own lines, numbered at the margin (`(1)`).
Distinguish **display** equations (structural, numbered, anchorable) from
**inline** math (embedded in a sentence). Preserve the equation number as the
anchor. See Step 4 for what breaks.

### References, footnotes, appendices
Reference lists anchor the predecessor search (see
[literature-context.md](literature-context.md)); footnotes carry caveats and
definitions; appendices often hold the details the main text abbreviates. Preserve
their numbering so they can be cited.

## Step 4 — Named extraction hazards

### Two-column layouts
Most conference papers set body text in two columns, but headings, wide figures,
and wide tables often span both. A naive line-by-line read interleaves the columns
and produces nonsense.
- **Do:** reconstruct reading order **column-first** — finish the left column of a
  page before starting the right.
- **Watch:** elements that span the full width interrupt that flow; a line that
  jumps topic is usually a column-boundary or a spanning block, not a missing
  sentence.
- **Symptom of failure:** grammatical text that abruptly changes subject
  mid-sentence, or a sentence that continues on a different page column.

### OCR / scanned text
When there is no text layer, OCR is the only route, and it is lossy.
- **Common confusions:** `l`/`1`/`I`, `O`/`0`, `rn`/`m`, `cl`/`d`, `fi`/`fl`
  ligatures, hyphen vs en-dash vs minus, Greek letters (`α`↔`a`, `β`↔`B`,
  `μ`↔`u`), and lost **superscripts/subscripts** (exponents small and misplaced).
- **Do:** verify every **number** that carries a claim against a second read of
  the image; treat units, decimals, `±`, and signs as high-risk.
- **Flag:** state that OCR was used; do not present OCR text as the paper's exact
  wording.

### Broken or garbled mathematics
Math is where extraction fails most quietly and most damagingly.
- **Typical damage:** dropped operators/subscripts, flattened exponents (`x2` for
  `x²`), linearized fractions and integrals, scrambled matrices, lost Greek and
  relation symbols (`≤`↔`<`), and mis-paired brackets.
- **Recovery order:** (1) prefer the LaTeX source; (2) if only a PDF, re-read the
  region as an image and transcribe **exactly**, keeping uncertainty markers;
  (3) if it cannot be read, reproduce what is legible and mark the rest
  `[equation not cleanly parsed]` — never invent the missing part.
- **Flag:** note which equations are trustworthy and which are partial, and
  whether any argument depends on the partial ones.

### Tables beyond simple grids
Merged cells, wrapped text, and multi-line rows flatten into the wrong cells when
lined up naively. Re-check that row and column counts are consistent, that totals
match their parts, and that a header's units are attached to the right column.

### Figures with embedded text
Axis labels, ticks, and legends live in the image. Extract them only if
legible/OCR-able, and cite the figure caption otherwise. Do not read a trend off a
plot from memory of similar plots.

### Line-break, hyphenation, and ligature artifacts
- **De-hyphenate** words broken across lines (`distri- bution` → `distribution`) —
  but *not* genuine hyphens in compounds.
- **Rejoin** sentences split across a column or page.
- **Normalize** ligatures and smart quotes only for readability; keep quoted
  material verbatim in the original characters where quoting exactly.

## Fidelity tiers

| Tier | Source | Supports | Does not support |
| --- | --- | --- | --- |
| **High** | LaTeX source, publisher HTML | Equations, exact numbers, structure, exact quotes | — |
| **Medium** | Digital PDF, text layer | Structure and prose; **verify** math and tables | Confident equation transcription without checking |
| **Low** | OCR of a scan | Gist, coarse structure | Exact numbers, symbols, fine typography |
| **Very low** | Abstract / partial text | Motivation, headline claim | Method detail, evidence, tables, reproducibility |

Lower the confidence of any claim drawn from a lower tier, and say so.

## How to flag reduced fidelity

Whenever extraction is imperfect, emit an **explicit fidelity note** and propagate
it into the mode's output. The note answers four questions:

1. **Source form** — what you started from (and the representation chosen).
2. **Method** — how the structure was obtained (text layer, OCR, LaTeX, manual
   re-read).
3. **Specific losses** — exactly what is missing or unreliable (which figures,
   tables, or equations; OCR vs parsed; column artifacts).
4. **Confidence impact** — which conclusions are weakened, and what a fuller
   source would resolve.

Then, inside the analysis:

- Mark unreadable material inline: `[figure illegible]`, `[table cell unclear]`,
  `[equation not cleanly parsed]`, `[text truncated]`.
- Keep the **[Unknown] voice** for anything the source does not establish — never
  promote a reconstructed guess into a [Paper] statement.
- State uncertainty positively: "the minus sign in Eq. 3 could not be verified" is
  a finding, not a gap to hide.

### Fidelity-note template

```
Fidelity note
- Source form:      <PDF text layer | scanned PDF | HTML | LaTeX source | abstract>
- Representation:   <which form was extracted>
- Method:           <text layer | OCR | manual image re-read | markup parse>
- Tier:             <High | Medium | Low | Very low>
- Losses:           <specific figures / tables / equations / regions affected>
- Confidence impact:<which claims are weakened; what would resolve it>
- Markers used:     [unclear] [illegible] [equation not cleanly parsed] ...
```

## Consistency checks before you rely on a parse

Cheap internal checks catch many extraction errors:

- **Numbers agree across views** — a value in the abstract matches a table cell,
  matches a figure axis.
- **Counts reconcile** — table columns/rows match the caption; totals equal their
  parts.
- **Cross-references resolve** — "as shown in Figure 3" points at a real Figure 3.
- **Section flow is monotone** — numbers ascend; text does not jump subjects.
- **Symbols are consistent** — the same variable means the same thing throughout.

If a check fails, suspect the extraction **first**, and record the discrepancy
rather than picking the reading you prefer.

## Cross-references

- Turning the structured view into a claim → evidence audit:
  [appraisal.md](appraisal.md).
- Genre-specific notes on what a full read normally contains:
  [genres.md](genres.md).
- Locating and verifying the source itself: [literature-context.md](literature-context.md).
