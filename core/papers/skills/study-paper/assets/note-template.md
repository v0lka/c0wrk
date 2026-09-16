# Reading Note — `<paper title>`

> **Usage.** One note per paper. Record a reading in any mode — fill only the
> sections that mode needs, leave the rest blank.
> - **Skim** → §1–3, §7, §8, §9 (keep it to about one screen).
> - **Review** → all sections; the §5 matrix and §7 critical layer carry the weight.
> - **Implement** → §1, §4, §5, §6, §7, plus the reproduction checklist in §11.
> - **Teach** → §1, §3, §6, §7, §8; hand the terms in §6 to `flashcards.md`.
>
> **Three-voices discipline.** Keep one voice per statement and never blur them:
> **[Paper]** = what the authors state/report — always anchored; **[Analyst]** =
> your inference or judgment — labeled as yours; **[Unknown]** = what the paper
> does not establish. **Anchor every [Paper] item** (section / figure / table /
> equation / algorithm / page). If you cannot anchor it, label it [Analyst] or
> mark it [Unknown]. Never present an unanchored guess as the paper's content.

## 1. Identification

| Field | Value |
| --- | --- |
| Title | |
| Authors | |
| Year / venue / peer-review status | |
| Identifier (arXiv / DOI / URL) | |
| Version / date read | |
| Source form (full text / abstract-only / partial / OCR) | |
| Mode used (Skim / Review / Implement / Teach) | |

## 2. Scope & provenance

- What the paper sets out to do — [Paper], anchored:
- Fidelity / extraction notes (missing figures, garbled equations, two-column artifacts, partial parse):

## 3. Structured map (one line each, anchored)

| Dimension | One line — [Paper] | Anchor |
| --- | --- | --- |
| Motivation | | |
| Problem statement | | |
| Core idea / approach | | |
| Headline result | | |

## 4. Contributions (as the authors frame them — do not upgrade them)

| # | Contribution — [Paper] | Anchor |
| --- | --- | --- |
| C1 | | |
| C2 | | |
| C3 | | |

## 5. Contribution → evidence matrix

One row per contribution. **The Anchor column is mandatory** — point to the exact
section, figure, table, equation, algorithm, or page that carries the evidence.
If a row has no anchor, the evidence is absent for this note; say so rather than
leaving it blank.

| Contribution | Claim the evidence is meant to support | Evidence offered (experiment / result / proof) | **Anchor** (section / figure / table / equation / page) | Evidence strength (present / weak / absent) | [Analyst] verdict — does the evidence support the claim? |
| --- | --- | --- | --- | --- | --- |
| C1 | | | | | |
| C2 | | | | | |
| C3 | | | | | |

## 6. Method reconstruction (pipeline)

| Stage | Responsibility | Inputs → outputs | Anchor |
| --- | --- | --- | --- |
| | | | |

- Key symbols and what they mean — [Paper], anchored:
- Stated assumptions / dependencies — [Paper], anchored:
- Data: sources, splits, preprocessing, availability / licensing — [Paper], anchored:

## 7. Critical layer (required in every mode)

- **Claim → evidence check** (load-bearing claim): claim [anchor] → evidence
  offered → evidence is **present / weak / absent**.
- **Red flags (≤3, ranked by severity):**
  1. … — why it matters (one line).
  2. …
  3. …
  *(If there are more, write "and others" — do not flood the note.)*
- **Uncertainty flags** — [Unknown] items (unspecified, unverifiable from the
  available source, or assumption-dependent):
  - …

## 8. TL;DR (2–4 sentences a busy reader could act on)

…

## 9. Verdict

| Field | Value |
| --- | --- |
| Reading (read in full / read selectively / skip) | |
| One-line reason | |
| If selective, read exactly | |
| Verdict (accepted / rejected / uncertain) | |
| Confidence in this verdict (low / medium / high) | |

## 10. Open questions & follow-ups

- Questions the paper leaves open:
- Predecessors it builds on / known citing work:
- What to read or try next:

## 11. Reproduction checklist (fill when the mode is Implement)

- [ ] Method decomposed into an ordered pipeline, each stage's responsibility clear.
- [ ] Every equation / algorithm transcribed with symbols and anchors; unparseable ones flagged.
- [ ] Hyperparameters and constants tabulated (anchored); missing values listed.
- [ ] Data sources, splits, and preprocessing specified (anchored).
- [ ] Evaluation protocol (metrics + procedure) specified.
- [ ] Gotchas and assumed defaults listed, each labeled as an assumption.
- [ ] Claims the reproduction would actually test, named.
