---
name: research-decision
description: >-
  Analyze hypothesis results and the current state of the hypothesis graph to
  recommend continue, pivot, kill, or fork decisions for an research
  iteration. Considers timebox constraints, budget, research question alignment,
  and graph topology. Use after an experiment completes, when the researcher
  needs help deciding next steps, or when reviewing the overall research
  direction.
metadata:
  domain: engineering
  methodology: iterative-engineering-research
---

# Iteration Decision Support

Analyze the state of a completed hypothesis experiment and the overall
hypothesis graph to recommend the next action.

## Context

After each experiment, the researcher makes one of four decisions:

- **Continue** — hypothesis confirmed, deepen this direction, formulate
  child hypotheses.
- **Pivot** — result points to a more promising direction different from
  the current one. Formulate new hypotheses, change vector.
- **Kill** — branch is a dead end (refuted, unstable, impractical). Prune
  the branch in the graph, document the reason.
- **Fork** — competing approaches discovered that are worth investigating
  in parallel. Create multiple child hypotheses for simultaneous exploration.

The decision is the researcher's, but this skill provides structured analysis
and a recommendation to support it.

## Procedure

### Step 1 — Read current state

Gather the following information:

1. **The completed hypothesis card** — read `hypotheses/H-NNN.md` for the
   statement, verification criterion, result, and status.
2. **The hypothesis graph** — read `hypotheses/graph.md` to understand the
   full DAG: which branches are alive, which are pruned, where pivots
   occurred, and what the current "front line" looks like.
3. **The research brief** — read `brief.md` for the research question,
   success criteria, scope boundaries, known constraints, and time budget.
4. **Sibling/related hypotheses** — if the completed hypothesis is part of
   a fork, read its sibling hypotheses to compare results.

### Step 2 — Analyze the result

Evaluate the experiment outcome against multiple dimensions:

#### 2a. Verification criterion alignment

- Was the verification criterion met, partially met, or not met?
- Is the result definitive or are there caveats?
- If inconclusive: is the inconclusiveness due to the approach, the
  timebox, or an external factor?

#### 2b. Research question proximity

- How much closer does this result bring us to answering the research
  question from the brief?
- Does the result open new pathways toward the answer?
- Does the result suggest the research question itself needs refinement?

#### 2c. Resource and timebox assessment

- How much of the overall research time budget has been consumed?
- How many active branches remain in the graph?
- Is there sufficient budget to pursue new directions, or should the
  research begin converging?

#### 2d. Surprise and serendipity

- Did the experiment reveal unexpected findings outside the original scope?
- Are there serendipitous discoveries worth pursuing (potential fork)?

### Step 3 — Generate recommendation

Based on the analysis, recommend one of the four decisions with
justification:

#### Continue — recommend when:
- The hypothesis was confirmed.
- The result directly advances the research question.
- There are clear, specific follow-up questions.
- Budget allows further depth.

When recommending Continue, propose 1–3 specific child hypotheses with
draft statements and verification criteria.

#### Pivot — recommend when:
- The result (positive or negative) reveals a more promising direction.
- The current branch has diminishing returns.
- A related but different approach looks more viable.

When recommending Pivot, clearly articulate:
- What the new direction is and why it is more promising.
- What is being left behind and why.
- Proposed hypothesis(es) for the new direction.

#### Kill — recommend when:
- The hypothesis was definitively refuted.
- The timebox expired without meaningful progress.
- The branch is technically viable but impractical (too complex, too slow,
  too fragile for the intended use case).
- Continuing this branch will not help answer the research question.

When recommending Kill, document:
- The specific reason for termination.
- Any useful knowledge gained that should be preserved.
- Whether this dead end has implications for other branches.

#### Fork — recommend when:
- Two or more competing approaches are identified.
- It is unclear which approach is superior without trying both.
- Resources are available for parallel investigation.

When recommending Fork, propose:
- The competing hypotheses (2–3 maximum).
- Individual timeboxes for each.
- Selection criteria: how will we choose the winner (or discard both)?

### Step 4 — Present to the researcher

Present the analysis and recommendation in a structured format:

1. **Result summary**: one paragraph on what the experiment showed.
2. **Graph context**: where this result fits in the overall research.
3. **Recommendation**: the decision (Continue / Pivot / Kill / Fork) with
   reasoning.
4. **Proposed next hypotheses**: if applicable, draft cards for the next
   hypotheses.
5. **Risks and alternatives**: what could go wrong with the recommendation,
   and what the alternative decision would be.

### Step 5 — Execute the decision

Once the researcher confirms (or modifies) the decision:

1. Update the completed hypothesis card with the decision in the
   "Decision" field.
2. If the decision is **Continue** or **Fork**: invoke `research-hypothesis`
   to create the new child hypothesis cards.
3. If the decision is **Pivot**: invoke `research-hypothesis` to create new
   hypotheses. Mark the abandoned branch hypotheses as `cancelled` if they
   are still open.
4. If the decision is **Kill**: ensure the hypothesis status is `refuted`
   or `cancelled` as appropriate. No new hypotheses are created on this
   branch.

## Decision Heuristics

Some rules of thumb encoded in the methodology:

- **Timebox expiry + inconclusive = kill or pivot**, not extension. The
  methodology explicitly discourages extending timeboxes.
- **Budget pressure → convergence**: when more than ~70% of the overall
  research budget is consumed, prefer Continue (deepening) over Fork
  (broadening). Begin thinking about synthesis.
- **Fork is the exception**: parallel investigation is allowed but should
  be justified. The default is sequential hypothesis testing.
- **Preserve knowledge from dead ends**: killed branches still produced
  knowledge. Ensure the "what we learned" section of the card is thorough.

## Relation to Other Skills

- Invoked after `research-experiment` records a result.
- Creates new hypotheses via `research-hypothesis`.
- Feeds into `research-synthesis` when the research question is answered
  or budget is exhausted (triggers Phase 3).
- `research-status` provides the graph overview that informs decisions.
