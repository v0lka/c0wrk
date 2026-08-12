---
name: research-init
description: >-
  Initialize a new research project following the Iterative Engineering
  Research Methodology. Creates the file catalog structure (brief, hypothesis
  graph, prior art page) and populates the research brief through structured
  dialogue with the researcher. Use when starting a new research, setting up a
  new investigation, or creating a sub-research.
metadata:
  domain: engineering
  methodology: iterative-engineering-research
---

# Research Project Initialization

Initialize a new research project with the standardized file catalog
and a populated research brief.

## Context

Every research project requires a standardized file catalog and a research
brief as its entry point. The brief is a living document that evolves
throughout the research but must be created at the start with enough detail
to guide the first hypotheses.

## File Catalog Structure

All methodology artifacts live in a file catalog of Markdown files. The layout
for a single research project is:

```
{research-root}/
├── index.md                          # Research index (all projects)
└── R-NNN-short-name/                 # One research project
    ├── brief.md                      # Research brief (entry point)
    ├── hypotheses/                   # Hypothesis graph container
    │   └── graph.md                  # Mermaid diagram + hypothesis catalog
    ├── prior-art.md                  # Prior art and references
    └── report.md                     # Final report (created at synthesis only)
```

`{research-root}` is a directory the user specifies. It may already contain
other research projects. If `index.md` exists, this is an existing root; if
not, create it.

## Procedure

### Step 1 — Determine the research root

Ask the user which directory to use as the research root. If the directory
already contains `index.md`, treat it as an existing research root. Otherwise,
create the directory and seed `index.md` with an empty research index heading.

### Step 2 — Assign a research identifier

Read `index.md` to find the highest existing `R-NNN` identifier. Assign
`R-(N+1)`, zero-padded to three digits. If no researches exist yet, start
with `R-001`.

### Step 3 — Gather brief information

Conduct a structured dialogue with the researcher. Collect every field listed
below. All fields are required unless marked optional.

1. **Research title** — a concise, human-readable title for the project.
   Used in the brief heading, the index, and graph references.
2. **Problem domain** — which segment is involved (web application
   security, cryptographic protocols, analysis tooling, etc.).
3. **Research question** — what we are trying to discover or create, phrased
   as a question answerable with "yes/no" or "here is how."
4. **Success criteria** — how we know the research achieved its goal.
   Quantitative (coverage >= X %, detection time <= Y) or qualitative
   (working PoC, proof of impossibility).
5. **Scope boundaries** — what is inside vs. outside the research.
6. **Known constraints** — technical, time, and resource limitations.
7. **Prior art (summary)** — known existing work, tools, publications.
   A short overview here; the detailed catalog lives in `prior-art.md`.
8. **Related researches** *(optional)* — links to parent or sibling research
   projects when this is a sub-research or part of a decomposed effort.
9. **Implementation plan** — high-level description of how results will be
   integrated into an existing product or become the basis for a new one.
   At this stage: target product, expected integration format, affected
   components.
10. **Ethical boundaries** — target systems used, whether approval is required
    and obtained, responsible-disclosure plan if applicable.
11. **Quarter** — the calendar quarter this research belongs to (e.g.,
    2025-Q2).
12. **Researcher(s)** — names or identifiers of the people conducting the
    research.

Status is set to **Active** automatically.

### Step 4 — Validate the 3-month constraint

Ask whether the research can realistically be completed within one quarter
(3 months). If not, suggest decomposition into sub-researches. Common
decomposition strategies:

- **By stages**: exploration + primary hypotheses → deep experiments →
  finalization + prototypes.
- **By independent directions**: each major branch of the hypothesis graph
  becomes its own sub-research.
- **By abstraction level**: theoretical analysis → tool implementation.

If decomposition is chosen, recursively initialize each sub-research as its
own project and cross-link them via the "Related researches" field.

### Step 5 — Create the file structure

Create directory `R-NNN-short-name/` where `short-name` is a kebab-case slug
derived from the research question (max 50 chars).

Populate files using the templates in [assets/](assets/):

| File | Template | Notes |
|---|---|---|
| `brief.md` | [brief-template.md](assets/brief-template.md) | Fill with collected info |
| `hypotheses/graph.md` | [graph-template.md](assets/graph-template.md) | Empty graph placeholder |
| `prior-art.md` | [prior-art-template.md](assets/prior-art-template.md) | Empty catalog |

**Do NOT create `report.md`** — it is created only during the synthesis phase
(see `research-synthesis` skill).

### Step 6 — Update the research index

Append an entry to `{research-root}/index.md` with the new research's
metadata: identifier, title, status, problem domain, quarter, researcher(s),
and a relative link to `brief.md`.

### Step 7 — Prompt for first hypothesis

After initialization, suggest that the researcher formulate their first
hypothesis. If they are ready, hand off to the `research-hypothesis` skill
to create `H-001`.

## Important Notes

- The brief is a **living document**. Research question and success criteria
  will be refined as the research progresses. This is expected.
- All dates use ISO 8601 format (YYYY-MM-DD).
- File and directory names use kebab-case.
- Hypothesis cards are NOT created during init — they are managed by the
  `research-hypothesis` skill.
- The research root may contain many projects side by side.
