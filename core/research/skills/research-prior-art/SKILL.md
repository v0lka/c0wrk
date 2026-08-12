---
name: research-prior-art
description: >-
  Search, evaluate, and catalog prior art for an research project.
  Finds and annotates academic papers, CVEs, tools, conference talks, and
  source code repositories. Maintains the prior-art.md catalog with relevance
  assessments. Use when investigating existing work, reviewing literature,
  searching for CVEs, or when the researcher mentions prior art, references,
  or related work.
metadata:
  domain: engineering
  methodology: iterative-engineering-research
---

# Prior Art Research and Cataloging

Search for, evaluate, and catalog prior art relevant to an research
project.

## Context

Prior art research is primarily performed during the Exploration phase but
can happen at any point — including mid-iteration when a hypothesis demands
additional context. The goal is to build a curated catalog of existing work
so the researcher can stand on the shoulders of others rather than reinvent
the wheel or miss known pitfalls.

## Source Types

Search across these categories, prioritized by typical relevance to
research:

1. **Academic papers and preprints** — search arXiv, IEEE, ACM, USENIX
   proceedings. Focus on security, software engineering, and PL venues.
2. **CVE databases and vulnerability advisories** — NVD, MITRE CVE, vendor
   advisories. Especially relevant when researching specific vulnerability
   classes.
3. **Security conference talks** — Black Hat, DEF CON, OffensiveCon, HITB,
   REcon, CCC. Check published slides and video archives.
4. **Open-source tools and projects** — GitHub, GitLab. Look for existing
   implementations addressing the same or adjacent problem.
5. **Blog posts and write-ups** — Project Zero, security research blogs,
   vendor engineering blogs. Often contain practical insights missing from
   papers.
6. **Standards and specifications** — RFCs, OWASP guides, NIST publications.
   Relevant when the research touches protocol or compliance boundaries.

## Procedure

### Step 1 — Identify the research project

Locate the research project directory and read `brief.md` to understand the
research question, problem domain, scope boundaries, and any prior art
already noted in the brief.

### Step 2 — Derive search queries

From the brief, generate a set of search queries. Use multiple query
strategies:

- **Direct queries**: keywords from the research question itself.
- **Synonym expansion**: alternative terminology for the same concepts
  (e.g., "fuzzing" / "fuzz testing" / "generative testing").
- **Problem-adjacent queries**: related problems that may have transferable
  solutions (e.g., when researching API endpoint discovery, also search for
  "web crawler coverage," "JavaScript static analysis," "SPA route extraction").
- **Author/group tracking**: if a known research group works in the area,
  search for their recent publications.

Present the generated queries to the researcher for validation and adjustment
before executing searches.

### Step 3 — Execute searches

Search across the source types listed above. For each result:

1. Retrieve the title, authors/creators, publication date, and source URL.
2. Read or skim the abstract/summary/README.
3. Write a 1–2 sentence annotation summarizing what it contributes to the
   research question.
4. Assign a relevance rating:
   - **High** — directly addresses the research question or a core hypothesis.
   - **Medium** — relevant to the problem domain but does not directly answer
     the question.
   - **Low** — tangentially related; useful for background context only.

### Step 4 — Detect duplicates

Before adding entries, check `prior-art.md` for existing entries with the
same URL or substantially similar title. Skip duplicates; update annotations
if the existing entry can be improved.

### Step 5 — Update the catalog

Append new entries to the table in `prior-art.md`. Maintain the sequential
numbering. Each entry must contain:

| Field | Description |
|---|---|
| # | Sequential number |
| Source | Clickable title linking to the URL |
| Type | Paper / CVE / Tool / Talk / Blog / Standard / Other |
| Annotation | 1–2 sentences on what this source contributes |
| Relevance | High / Medium / Low |

### Step 6 — Summarize findings

After cataloging, provide the researcher with a brief summary:

- Total new entries added, broken down by type.
- Top 3–5 most relevant sources with a sentence each on why they matter.
- Any notable gaps: areas where prior art is surprisingly scarce or absent,
  which may indicate unexplored territory or indicate the need for different
  search terms.
- Suggested adjustments to the research question or hypotheses based on
  what was found.

### Step 7 — Update the brief (if needed)

If the prior art search reveals information that should update the brief's
"Prior art (summary)" section or suggests changes to the research question
or scope, propose specific edits to `brief.md` for the researcher's approval.

## Tips for Effective Prior Art Search

- **Recency bias**: Domain moves fast. Prioritize sources from the last 3–5
  years, but don't ignore foundational work.
- **Negative results matter**: papers or posts describing failed approaches
  are valuable — they help avoid known dead ends.
- **Implementation vs. theory**: for each paper, note whether a reference
  implementation exists. A paper with code is significantly more useful for
  hypothesis formulation.
- **Citation chains**: when a highly relevant paper is found, check its
  references and who cites it for additional leads.

## Relation to Other Skills

- Typically invoked after `research-init` during the Exploration phase.
- Findings often feed directly into `research-hypothesis` (first hypotheses
  are informed by gaps in prior art).
- Can be re-invoked at any point during iterations when additional context
  is needed.
