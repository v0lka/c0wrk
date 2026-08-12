---
name: research-experiment
description: >-
  Guide experiment design, execution tracking, and result recording for an
  research hypothesis. Helps set up reproducible environments, plan
  minimal experiments, track progress against the timebox, and formally
  record outcomes. Use when starting an experiment for a hypothesis, setting
  up a test environment, tracking experiment progress, or documenting
  experiment results.
metadata:
  domain: engineering
  methodology: iterative-engineering-research
---

# Experiment Execution and Recording

Guide the design, execution tracking, and result recording of experiments
that test research hypotheses.

## Context

Each iteration in the methodology tests one hypothesis through an experiment.
The experiment must produce a clear signal: the hypothesis is confirmed,
refuted, or the result is inconclusive. The emphasis is on minimality — the
goal is to get an answer, not to build a product.

## Experiment Design Principles

1. **Minimality**: create the smallest prototype or analysis that can
   confirm or refute the hypothesis. Resist scope creep.
2. **Reproducibility**: key experiments (those that feed into the final
   report) must be reproducible by other engineers without the author's
   involvement. This means: documented environment, launch instructions,
   and setup scripts.
3. **Timebox awareness**: the experiment runs within the hypothesis timebox.
   If the timebox is about to expire and the result is unclear, that itself
   is a result — do not extend without a deliberate decision.

## Procedure

### Step 1 — Read the hypothesis card

Locate and read the hypothesis card (`hypotheses/H-NNN.md`). Extract:

- The statement being tested.
- The verification criterion (what counts as confirmation).
- The timebox (how long is allocated).
- The current status (must be `open` or `in-progress`).

If the status is not `open` or `in-progress`, stop and inform the researcher
that this hypothesis is not in an experimentable state.

### Step 2 — Plan the experiment

Help the researcher design a minimal experiment:

1. **Approach**: what will be built, analyzed, or measured? Suggest the
   simplest viable approach.
2. **Environment**: what OS, language versions, tools, and dependencies are
   needed? Capture these now — they form the reproducibility requirements.
3. **Expected output**: what artifact will demonstrate the result (PoC,
   benchmark data, log output, counterexample)?
4. **Completion signals**: specifically, what outcome confirms the hypothesis
   and what outcome refutes it? Map the verification criterion to concrete
   observables.

If the verification criterion in the card is ambiguous, propose a more
precise operationalization and update the card with the researcher's approval.

### Step 3 — Set up the environment

Assist with environment setup:

- Suggest Dockerfile, Makefile, shell scripts, or other setup automation
  as appropriate for the experiment's complexity.
- For trivial experiments (single script, no unusual dependencies), a note
  in the card's Experiment Notes section is sufficient.
- For complex setups, recommend creating a dedicated directory in the
  project's prototype/tool area and versioning it.

Document the environment in the hypothesis card's Experiment Notes section.

### Step 4 — Track progress

During the experiment, help the researcher track progress:

- Periodically check timebox status. If more than 75% of the timebox has
  elapsed without a clear signal, alert the researcher and suggest:
  - Simplifying the experiment scope.
  - Identifying what is blocking progress.
  - Considering whether the timebox should trigger a `kill` or `pivot`
    decision.
- Record intermediate findings in the Experiment Notes section of the
  hypothesis card. These notes are free-form but should capture key
  observations, dead ends, and unexpected discoveries.

### Step 5 — Record the result

When the experiment concludes (by result or by timebox expiry):

1. **Determine the outcome**:
   - **Confirmed** — the verification criterion is met.
   - **Refuted** — the verification criterion is demonstrably not met.
   - **Inconclusive** — neither confirmed nor refuted within the timebox.
     Document what would be needed for a definitive result.

2. **Update the hypothesis card** via the `research-hypothesis` skill:
   - Set Status to the appropriate value.
   - Fill the Result section with: findings, link to prototype/proof,
     artifact list.
   - Set the Completed date.

3. **Document reproducibility** (for experiments that will enter the final
   report):
   - Environment description (OS, language versions, dependencies).
   - Launch instructions (step-by-step).
   - Setup scripts or configuration files.

### Step 6 — Hand off to decision

After recording the result, inform the researcher that the iteration is
ready for the decision step. Suggest invoking the `research-decision` skill
to determine the next action (continue / pivot / kill / fork).

## Reproducibility Checklist

For experiments that are candidates for the final report, verify:

- [ ] Environment versions documented (OS, runtime, key libraries).
- [ ] Dependencies listed (package file, requirements.txt, go.mod, etc.).
- [ ] Launch instructions written and tested.
- [ ] Setup automation exists if the environment is non-trivial.
- [ ] Output artifacts are saved and linked from the hypothesis card.

Intermediate experiments that were discarded do not need to meet this bar.

## Relation to Other Skills

- Follows `research-hypothesis` (a hypothesis card must exist before an
  experiment can begin).
- Results feed into `research-decision` for next-action determination.
- Reproducibility artifacts are inventoried by `research-synthesis`.
