---
name: research-hypothesis
description: >-
  Create, update, and manage hypothesis cards within an research
  project. Maintains the hypothesis graph (Mermaid diagram) and the catalog
  table. Handles parent-child relationships, status transitions, and
  result recording. Use when formulating a new hypothesis, recording
  experiment results, updating hypothesis status, or managing the hypothesis
  graph.
metadata:
  domain: engineering
  methodology: iterative-engineering-research
---

# Hypothesis Management

Create, update, and manage hypothesis cards and the hypothesis graph for an
research project.

## Context

The hypothesis is the atomic unit of work in the methodology. Hypotheses are
organized as a directed acyclic graph (DAG) where edges represent the
relationship "result of X led to formulation of Y." The graph grows as the
research progresses — only initial hypotheses are known at the start.

Each hypothesis has a card (a dedicated `.md` file) and an entry in the
graph page (`hypotheses/graph.md`).

## Hypothesis Card Fields

Every hypothesis card contains these fields:

| Field | Description |
|---|---|
| **Identifier** | Unique ID within the research, e.g., `H-001` |
| **Short title** | Concise label for graph nodes and catalog entries |
| **Statement** | What is being tested, phrased as a falsifiable assertion |
| **Verification criterion** | Specific condition for confirmation (working PoC, metric threshold, counterexample) |
| **Timebox** | Maximum time allocated for verification (ISO 8601 duration or calendar dates) |
| **Parents** | Links to hypothesis cards whose results led to this one (empty for root hypotheses) |
| **Status** | `open` / `in-progress` / `confirmed` / `refuted` / `cancelled` |
| **Created** | Date the hypothesis was formulated (ISO 8601) |
| **Completed** | Date the experiment concluded (filled upon completion) |
| **Decision** | The iteration decision applied after the result: `continue` / `pivot` / `kill` / `fork` (filled by `research-decision`) |
| **Experiment notes** | Free-form observations, intermediate findings, and methodology notes recorded during the experiment |
| **Result** | Filled upon completion: findings, link to prototype or proof, list of artifacts |

## Operations

### Create a New Hypothesis

#### Step 1 — Gather information

Collect from the researcher:

1. **Statement** — a clear, falsifiable assertion. Good: "Static analysis of
   webpack bundles can extract >= 90% of API endpoints." Bad: "Look at
   webpack bundles."
2. **Verification criterion** — what constitutes confirmation. Must be
   specific and measurable.
3. **Timebox** — maximum time for this hypothesis. Push back if the timebox
   seems too generous (encourages scope creep) or too tight (guarantees
   inconclusive results).
4. **Parents** — which existing hypothesis results motivated this one. For
   root hypotheses (formulated during Exploration), parents are empty.

If the statement is vague, help the researcher sharpen it. A hypothesis must
be an assertion that can be confirmed or refuted, not a topic or direction.

#### Step 2 — Assign identifier

Read `hypotheses/graph.md` to find the highest existing `H-NNN`. Assign
`H-(N+1)`, zero-padded to three digits.

#### Step 3 — Create the card file

Create `hypotheses/H-NNN.md` using the template at
[assets/hypothesis-card-template.md](assets/hypothesis-card-template.md).
Fill in all collected fields. Set status to `open`.

#### Step 4 — Update the graph

Update `hypotheses/graph.md` in two places:

1. **Mermaid diagram**: Add a new node with the appropriate CSS class
   (`open` for new hypotheses). If the hypothesis has parents, add edges
   from each parent node to the new node.

2. **Catalog table**: Add a row with ID (linked to the card file), statement
   summary, status, decision (empty for new), and parent IDs.

### Update Hypothesis Status

When a hypothesis transitions between statuses, update:

1. **The card file** (`H-NNN.md`): change the Status field.
2. **The graph diagram**: change the CSS class of the node.
3. **The catalog table**: update the Status column.

Valid transitions:

```
open → in-progress → confirmed
                   → refuted
                   → cancelled
open → cancelled
```

A hypothesis cannot transition backwards. If rework is needed, create a
new hypothesis with the original as parent.

### Record Results

When an experiment completes (see `research-experiment` skill), update
the hypothesis card:

1. Set **Status** to `confirmed`, `refuted`, or leave as `in-progress` if
   the result is inconclusive.
2. Fill the **Result** section with:
   - What was discovered.
   - Link to the prototype, proof, or experiment artifacts.
   - If inconclusive: why, and what would be needed for a definitive result.
3. Update the graph diagram and catalog table accordingly.

### Fork Hypotheses

When a `fork` decision is made (see `research-decision` skill), create
multiple child hypotheses from the same parent. Each competing approach
becomes its own hypothesis with its own timebox and verification criteria.
Document in each card that it is part of a fork and which hypotheses are
its siblings.

## Mermaid Diagram Conventions

Use this node styling:

```mermaid
classDef confirmed fill:#4CAF50,color:#fff
classDef refuted fill:#F44336,color:#fff
classDef in_progress fill:#FF9800,color:#fff
classDef open fill:#2196F3,color:#fff
classDef cancelled fill:#9E9E9E,color:#fff
```

Node IDs use the pattern `H001`, `H002`, etc. (no hyphen, for Mermaid
compatibility). Display labels include the hyphen: `H-001: Short title`.

Edges represent "result led to formulation":

```mermaid
H001 --> H003
H002 --> H003
```

## Important Notes

- **One hypothesis at a time** is the default. Fork (parallel hypotheses)
  is the exception, used only when competing approaches are discovered and
  resources allow parallel investigation.
- **Timebox is a hard ceiling**: if the timebox expires without a result,
  that is itself a result (hypothesis not confirmed in allocated time) and
  a signal for `kill` or `pivot`.
- Keep hypothesis statements atomic. If a statement contains "and," consider
  splitting into two hypotheses.
- The graph is the primary navigation tool for the research. Keep it accurate
  and up to date at all times.

## Relation to Other Skills

- Invoked by `research-init` to create the first hypothesis.
- Works closely with `research-experiment` (records results) and
  `research-decision` (determines next hypotheses after a result).
- `research-status` reads the graph to generate overviews.
- `research-synthesis` uses the graph to produce the final report.
