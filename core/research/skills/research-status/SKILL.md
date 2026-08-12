---
name: research-status
description: >-
  Generate a comprehensive status overview of an research project.
  Reads the brief, hypothesis graph, and hypothesis cards to produce a
  summary of progress, active fronts, time and budget consumption, and key
  findings. Use when the researcher wants a progress check, when preparing
  for a quarterly review, or when someone new needs to understand the
  current state of a research project.
metadata:
  domain: engineering
  methodology: iterative-engineering-research
---

# Research Status Overview

Generate a comprehensive status snapshot of an research project.

## Context

At any point during a research project, stakeholders need to understand:
where the research stands, what has been learned, what the active fronts
are, and how much budget remains. This skill reads all project artifacts
and produces a structured overview.

This is especially useful for quarterly review preparation, onboarding
new team members, and periodic self-assessment by the researcher.

## Procedure

### Step 1 — Locate the research project

Ask the user to identify the research project (by ID, name, or directory
path). Read the brief (`brief.md`) to confirm the correct project.

### Step 2 — Gather data

Read the following files:

1. `brief.md` — research question, success criteria, scope, status,
   constraints, implementation plan.
2. `hypotheses/graph.md` — the Mermaid diagram and catalog table.
3. All hypothesis cards (`hypotheses/H-*.md`) — to get detailed status
   and results.
4. `prior-art.md` — to count cataloged sources.

### Step 3 — Compute metrics

Calculate:

- **Total hypotheses**: count of all `H-NNN.md` files.
- **By status**: how many are open, in-progress, confirmed, refuted,
  cancelled.
- **Confirmation rate**: confirmed / (confirmed + refuted), excluding
  cancelled and open.
- **Graph depth**: longest path from a root hypothesis to a leaf.
- **Graph breadth**: maximum number of active (non-terminal) branches.
- **Active front**: hypotheses that are `open` or `in-progress`.
- **Prior art entries**: count of entries in `prior-art.md`.

If the brief contains time budget information (start date, expected
end date), calculate:

- **Elapsed time**: from start to today.
- **Remaining time**: from today to expected end.
- **Time consumption**: elapsed / total as a percentage.

### Step 4 — Identify the current front line

The "front line" is the set of hypotheses at the leading edge of the
research — those that are currently open or in-progress, and represent
the active areas of investigation. For each front-line hypothesis:

- ID and statement.
- Timebox status (how much time remains).
- Parent chain: trace back to root to show the reasoning path.

### Step 5 — Summarize key findings

From confirmed and refuted hypotheses, extract the most important
findings — things that answer (partially or fully) the research question,
or that significantly shaped the research direction.

List the top 3–5 findings with one sentence each.

### Step 6 — Identify risks and concerns

Flag potential issues:

- **Timebox overruns**: hypotheses where the timebox is close to or past
  expiry without a result.
- **Stalled branches**: hypotheses in `in-progress` with no recent updates.
- **Budget pressure**: if overall time consumption exceeds hypothesis
  completion rate, flag convergence risk.
- **Scope drift**: if the active hypotheses have diverged significantly
  from the original research question.

### Step 7 — Generate the overview

Present the status in a structured format:

```
# Research Status: [R-NNN] Title

## Summary
- Status: Active / Completed / Paused
- Research question: ...
- Time: X of Y days elapsed (Z%)
- Hypotheses: A total (B confirmed, C refuted, D cancelled, E open/in-progress)

## Current Front Line
- H-NNN: [statement] — [timebox status]
- H-NNN: [statement] — [timebox status]

## Key Findings So Far
1. [finding]
2. [finding]
3. [finding]

## Hypothesis Graph
[Mermaid diagram from graph.md]

## Risks and Concerns
- [risk]
- [risk]

## Prior Art
- X sources cataloged (Y high relevance, Z medium, W low)

## Implementation Plan Status
[Current state of the implementation plan from brief.md]
```

### Step 8 — Suggest next actions

Based on the overview, suggest what the researcher should focus on next:

- Which hypothesis to experiment on next (if multiple are open).
- Whether any branch should be killed due to resource pressure.
- Whether synthesis should begin (if the research question is answered
  or budget is nearly exhausted).
- Whether additional prior art research would be valuable.

## Quarterly Review Mode

When invoked with a quarterly review context, additionally prepare:

- **Changes since last review**: compare current state with the state at
  the start of the quarter (or last review date if provided).
- **Demo-ready prototypes**: list prototypes that can be demonstrated.
- **Next quarter plan**: proposed focus areas and hypotheses for the
  next period.

## Relation to Other Skills

- Can be invoked at any time during the research lifecycle.
- Feeds into `research-decision` (informs graph-level decisions).
- Provides the starting data for `research-synthesis`.
- Useful before invoking `research-prior-art` (identifies knowledge gaps).
