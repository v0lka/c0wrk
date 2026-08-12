---
name: research-synthesis
description: >-
  Execute the synthesis phase of an research project. Analyzes the
  hypothesis graph, determines report verbosity (simple vs. complex path),
  generates the final report, creates a prototype inventory, and finalizes
  the implementation plan in the brief. Use when concluding a research
  project, writing the final report, or when the research question has been
  answered or the budget is exhausted.
metadata:
  domain: engineering
  methodology: iterative-engineering-research
---

# Research Synthesis

Execute the synthesis phase: analyze results, generate the final report,
consolidate prototypes, and finalize the implementation plan.

## Context

Synthesis is the final phase of a research project. It begins when one of
these conditions is met:

1. **Answer obtained** — the research question received a definitive answer
   (positive, negative, or refined).
2. **Budget exhausted** — allocated time or resources have run out.
3. **External decision** — stakeholder or research director decides to
   conclude the research (e.g., after a quarterly review).

## Procedure

### Step 1 — Confirm synthesis readiness

Read the research brief (`brief.md`) and hypothesis graph (`hypotheses/graph.md`)
to determine which trigger condition applies. Present the researcher with a
summary:

- Trigger: why synthesis is starting (answer obtained / budget / external).
- Research question: original formulation and any refinements.
- Graph stats: total hypotheses, confirmed, refuted, cancelled, still open.
- Open fronts: any hypotheses still in `open` or `in-progress` status.

If there are open hypotheses, ask the researcher whether to:
- Cancel them (they will be documented as "not pursued").
- Complete them before synthesis (defer synthesis).

### Step 2 — Determine report mode

The methodology defines two report modes based on the complexity of the
path taken:

**Simple path** — hypotheses mostly confirmed, few or no pivots/dead ends.
Produces a minimal report (1–2 pages equivalent).

**Complex path** — significant pivots, dead ends, unexpected discoveries.
Produces an extended report with graph analysis and decision documentation.

To determine the mode, evaluate:

- Ratio of refuted/cancelled hypotheses to total.
- Number of pivots (nodes where a branch was abandoned for a new direction).
- Whether any major forks occurred.
- Whether the final answer differs significantly from initial assumptions.

**Heuristic**: if more than ~30% of hypotheses were refuted or cancelled,
or if there were 2+ pivots, use the complex report mode.

Present the assessment to the researcher and let them confirm or override
the mode choice.

### Step 3 — Analyze the hypothesis graph

Walk the hypothesis graph and identify:

1. **The critical path** — the sequence of confirmed hypotheses that forms
   the backbone of the answer.
2. **Key decision points** — where continue/pivot/kill/fork decisions
   significantly shaped the research direction.
3. **Notable dead ends** — refuted or killed branches that produced valuable
   negative knowledge.
4. **Unexpected discoveries** — findings outside the original scope that
   emerged from experiments.

### Step 4 — Inventory prototypes

Scan all hypothesis cards for links to prototypes, PoCs, tools, scripts,
and other artifacts. For each prototype:

- **Name / description**: what it is.
- **Location**: where the code/artifact lives (repository, branch, directory).
- **Maturity level**: minimal PoC / functional prototype / near-production.
- **How to run**: brief launch instructions (or reference to detailed docs).
- **Which hypothesis it validates**: link back to the card.

Determine which prototypes should be consolidated into a unified solution
(if applicable) and which are standalone artifacts.

### Step 5 — Finalize the implementation plan

Update the "Implementation plan" section of `brief.md` to contain:

1. **Target product**: which existing product receives the results, or
   whether a new product is being created.
2. **Implementation model**: one of:
   - **Development team integration** — results are exhaustive, no pivots
     expected during integration. A working group (research expert +
     developers) implements in the standard development process.
   - **Research team integration** — integration may involve uncertainty
     and pivots. A working group (development expert + researchers)
     proceeds using the research methodology.
   - **New product (MVP)** — results form a new product. Researchers build
     the MVP; then hand off to development.
3. **Calendar plan**: estimated timeline in development sprints or equivalent
   units.
4. **Effort estimate**: order-of-magnitude effort in person-sprints.
5. **Key risks**: what could go wrong during implementation.

The choice of model depends on the level of uncertainty: if pivots are
expected during integration, use the research methodology; if the path is
predictable, use the standard development process.

### Step 6 — Generate the final report

Create `report.md` in the research project directory using the appropriate
template:

- Simple path: [assets/report-simple-template.md](assets/report-simple-template.md)
- Complex path: [assets/report-complex-template.md](assets/report-complex-template.md)

#### Simple report contains:

1. Problem statement (1 paragraph).
2. Approach description (1 paragraph).
3. Key results.
4. Prototype inventory (what it is, where it lives, how to run it).
5. Implementation plan summary with chosen model.

Expected length: 1–2 pages equivalent.

#### Complex report additionally contains:

6. Hypothesis graph with results (embedded Mermaid or link to graph.md).
7. Description of key decision points and their rationale.
8. Description of notable dead ends worth knowing about.
9. Unexpected discoveries and their potential implications.
10. Open questions that remain unanswered and may warrant future research.

Length: as much as needed to transfer all knowledge gained.

**Guiding principle**: write more only when there is something to explain.
A successful straight path does not need justification.

### Step 7 — Update the brief

1. Set the Status field in `brief.md` to "Completed" (or "Completed —
   budget exhausted" / "Completed — external decision" as appropriate).
2. Set a completion date.
3. Ensure the implementation plan is finalized (Step 5).

### Step 8 — Update the research index

Update the entry in `{research-root}/index.md` to reflect the completed
status and add a link to the final report.

## Quarterly Review Preparation

If synthesis coincides with a quarterly review/demo, prepare a presentation
covering:

- Brief state (including changes since last review).
- Hypothesis graph with "front line" highlighted.
- Key findings and intermediate results.
- Prototype demonstrations (if applicable).
- Plans for the next period (or closure summary).

This is not a separate artifact — it is a summary derived from the report
and graph.

## Relation to Other Skills

- Typically the last skill invoked in a research project lifecycle.
- Depends heavily on `research-hypothesis` (graph data) and
  `research-experiment` (prototype inventory).
- `research-status` can provide the starting point for synthesis analysis.
- `research-decision` may trigger synthesis when it determines the research
  question is answered or budget is exhausted.
