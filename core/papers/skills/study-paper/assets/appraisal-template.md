# Critical Appraisal — `<paper title>`

> **Usage.** The **Review** mode write-up (full appraisal). Work top to bottom;
> every paper-attributed statement carries an **anchor** (section / figure /
> table / equation / page). Hold the three voices apart: mark what the paper
> **[Paper]** states, what you **[Analyst]** judge, and what stays **[Unknown]**.
> The claim → evidence table (§4) and the critical layer (§7) are the core — do
> not skip them. Keep the review honest: never overstate the evidence, and say
> plainly when the source cannot support a conclusion.

## 1. Provenance & scope

| Field | Value |
| --- | --- |
| Title / authors / year | |
| Venue / peer-review status | |
| Version / date | |
| Identifier (arXiv / DOI / URL) | |
| What the paper sets out to do — [Paper], anchored | |
| Provenance notes (preprint vs. published, revisions, retractions) | |

## 2. The precise claims (in the paper's own terms)

Restate each load-bearing claim exactly as the authors make it — [Paper],
anchored. Do not upgrade, soften, or merge them.

| # | Claim [Paper] | Anchor |
| --- | --- | --- |
| L1 | | |
| L2 | | |

## 3. Method reconstruction

- Pipeline, end to end (inputs → outputs), each stage's responsibility:
- Key mechanism the claims depend on:
- Symbols and their meanings — [Paper], anchored:
- Assumptions the method relies on — [Paper], anchored:

## 4. Claim → evidence

One row per load-bearing claim. Anchor the claim; name the experiment or result
offered; give your verdict on whether it supports the claim. If no evidence is
offered, record "absent".

| Claim (anchored) | Experiment / result offered | Evidence present / weak / absent | [Analyst] verdict — does it support the claim? |
| --- | --- | --- | --- |
| | | | |

## 5. Experimental design appraisal

| Aspect | What the paper does — [Paper], anchored | [Analyst] assessment (fair / unfair / insufficient) |
| --- | --- | --- |
| Datasets & splits | | |
| Baselines | | |
| Metrics | | |
| Ablations | | |
| Controls | | |
| Fairness of comparison (compute, tuning, data budget) | | |

## 6. Statistics & reproducibility audit

| Check | Reported — [Paper], anchored | [Analyst] note |
| --- | --- | --- |
| Significance / confidence intervals | | |
| Effect size | | |
| Sample size / statistical power | | |
| Seeds & variance across runs | | |
| Compute-matched comparison | | |
| Code / data availability | | |

## 7. Critical layer

- **Claim → evidence check** (the load-bearing claim the paper's value rests on):
  claim [anchor] → evidence offered → **present / weak / absent**.
- **Red flags (≤3, ranked by severity):**
  1. … — one-line why.
  2. …
  3. …
- **Uncertainty flags** — [Unknown]: what is unspecified, unverifiable from the
  source, or assumption-dependent.
  - …

## 8. Threats to validity & unstated assumptions

- What the design cannot show:
- Abstract vs. results — where the framing overclaims beyond the numbers:
- Correlation presented as causation (if any):

## 9. Review verdict

- **Summary** (a few sentences):
- **Strengths:**
  - …
- **Weaknesses:**
  - …
- **Red flags** (carry down from §7):
  - …
- **Verdict:** accept / weak accept / borderline / weak reject / reject — or, for
  a software / system paper: use / use with caveats / avoid.
- **Confidence in this verdict** (low / medium / high) — and why:
- **What evidence would change the verdict:**
