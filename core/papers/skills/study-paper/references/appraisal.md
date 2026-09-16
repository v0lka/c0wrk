# Critical Appraisal — Claim, Evidence, and Validity

> **Load this when** you appraise any paper (the **Review** mode). This is the
> genre-independent rubric: it turns a paper's claims into a **claim→evidence
> map**, tests each claim against the evidence actually offered, and holds apart
> what the paper *shows* from what it *asserts*. Genre only changes *where* you
> look — see the companion `genres.md` in this `references/` directory; this
> rubric does not change.
>
> Keep the anchor discipline and the three-voices rule running throughout: mark
> **[Paper]** (stated by the authors, anchored), **[Analyst]** (your judgment),
> and **[Unknown]** (not established). Record the result with the bundled
> appraisal template (`assets/appraisal-template.md` at the package root).

---

## 1. The one question

**Does the evidence offered actually support the claim made?** Everything below
serves that single question. Two failure modes dominate and should both be named
when found:

1. **Wrong evidence** — the evidence does not bear on the claim (proxies that
   decouple, wrong population, wrong metric).
2. **Overreach** — the claim is broader than the evidence (scope inflation,
   hedge removal, causal language on associational data).

---

## 2. Procedure (run order)

1. **Establish provenance** — venue, peer-review status, version and date, and any
   revisions, corrections, or retractions.
2. **Extract the claims** in the paper's own terms. Keep three things apart:
   the **claim** (what is asserted), the **contribution** (what is new), and the
   **result** (what was measured or shown).
3. **Map claim → evidence** (§3), one row per load-bearing claim.
4. **Test validity** (§4) and **hunt unstated assumptions** (§6).
5. **Detect overclaiming** by comparing the abstract against the results (§5).
6. **Check causal language against causal design** (§7).
7. **Rank the red flags** — at most three, by severity — and record uncertainty
   flags.
8. **Deliver a verdict** with an explicit confidence level and the evidence that
   would change it.

---

## 3. Claim → evidence mapping method

This is the core of the appraisal. Do it claim by claim.

1. **List the load-bearing claims** — the few on which the paper's value rests,
   not every sentence. State each **exactly as the authors frame it** ([Paper]),
   each with an **anchor** (section / figure / table / equation / page).
2. **Locate the evidence** meant to support each claim — the single strongest
   experiment, result, proof, or measurement — and its anchor.
3. **Grade the evidence** on a three-way scale:
   - **Present** — a controlled, appropriate test that isolates the claim on the
     relevant population/data.
   - **Weak** — suggestive but confounded, underpowered, indirect, or measuring
     the wrong thing.
   - **Absent** — asserted with no experiment, proof, or measurement behind it.
4. **Give a per-claim verdict** ([Analyst]): *supported* / *partially supported* /
   *not supported* / *cannot tell*.
5. **State what evidence would move the verdict** — the experiment or analysis
   that would upgrade or overturn your rating.

| # | Claim [Paper], anchored | Evidence offered + anchor | Present / weak / absent | [Analyst] verdict — supported? | What would change it |
| --- | --- | --- | --- | --- | --- |
| L1 | | | | | |
| L2 | | | | | |

*Calibration note.* Grade against the claim's own scope, not against an ideal
paper. A performance claim is only *present* if it was measured on comparable
hardware with a baseline tuned to its best; a generality claim is only *present*
if the method was tested beyond the one setting named.

---

## 4. Threats to validity (checklist)

Validity frames distinguish **internal** (is the relation real *within* the
study?), **external** (does it hold *beyond*?), **construct** (do the measures
capture the intended concepts?), and **conclusion / statistical** (are the
inferences from the data sound?). This four-way split traces to the
Campbell-and-Stanley / Cook-and-Campbell tradition and is standard in empirical
work; qualitative studies add **reliability**. Use them as lenses, not labels.

- [ ] **Internal validity** — confounding, selection, measurement error, attrition,
      instrumentation, maturation, history, regression to the mean: is the
      comparison clean enough to license the claim?
- [ ] **Construct validity** — do the variables and metrics measure what the claim
      names, or a proxy that can silently decouple from it?
- [ ] **External validity** — sample, setting, time, and population: does the claim
      claim more scope than was actually tested?
- [ ] **Conclusion / statistical validity** — statistical power, multiple
      comparisons, error rates, violated assumptions, and significance conflated
      with effect size.
- [ ] **Reliability** — would the result reproduce across runs, annotators,
      implementations, or seeds?
- [ ] **Ecological validity** — does the controlled setting resemble the deployment
      the claim is about?
- [ ] **Authors' own handling** — have the authors named these threats, or is their
      threats section boilerplate that never touches the load-bearing claim?

---

## 5. Overclaiming detection (abstract versus results)

The abstract is a *claim*; the results are the *evidence*. Read the abstract, then
locate the exact result behind each clause and compare **strength** and **scope**.

- [ ] **Hedge removed** — "may / might / in our setting" in the body becomes an
      unqualified claim in the abstract.
- [ ] **Scope inflated** — one dataset, task, model, or language becomes "general",
      "models", or the field.
- [ ] **Best case as typical** — the headline equals the best seed, best benchmark,
      or best subgroup; medians and the full distribution tell another story.
- [ ] **"Significantly" / "outperforms"** with no test, no effect size, or a
      difference inside the noise.
- [ ] **Correlation phrased causally** — "improves", "causes", "leads to" on an
      associational result.
- [ ] **"We solve X"** when the result is partial (reduces error, improves a
      correlation, wins on one slice).
- [ ] **Novelty overreach** — the claimed advance exceeds what actually differs from
      prior work.
- [ ] **Statistical conflated with practical significance.**
- [ ] **Caveats dropped** — the abstract omits the paper's own limitations
      (contamination, licensing, narrow scope) stated in the body.

---

## 6. Unstated-assumptions hunt

Ask, for each load-bearing claim: **what must be true for it to hold that the paper
does not say?**

- **Data** — sampling frame, distributional and independence assumptions,
  stationarity, representativeness, absence of leakage.
- **Measurement** — the proxy is valid and stable; the unit of analysis matches the
  unit the claim speaks about.
- **Comparison** — baselines fairly tuned and matched on data/compute; nothing else
  changed (the *ceteris paribus* clause).
- **Analysis** — the statistical model's assumptions hold and the test measures what
  it claims.
- **Scope** — the tested population and setting match the population and setting of
  the claim.

Then ask: **where would each implicit assumption break, and does the evidence
survive if it does?** List implicit assumptions as [Analyst] or [Unknown], with the
impact if they fail — an assumption the paper never states is where overclaiming is
easiest to hide.

---

## 7. Correlation is not causation

- [ ] **Design first** — is the design experimental (assignment or manipulation of
      the cause) or observational? Only the former licenses causal language without
      further machinery.
- [ ] **Confounders** — if observational, are plausible confounders enumerated and
      controlled: matching, adjustment, DAG-informed covariate choice, instrumental
      variables, difference-in-differences, regression discontinuity?
- [ ] **Temporal order** — does the proposed cause precede the effect?
- [ ] **Mechanism** — is there a plausible causal pathway, or only an association?
- [ ] **Gradient or discontinuity** — is there a dose–response relationship or a
      sharp jump that supports a causal reading?
- [ ] **Alternatives ruled out** — reverse causation and a common cause (a third
      variable driving both) addressed?
- [ ] **Group structure** — could the association flip within subgroups
      (Simpson's paradox)?
- [ ] **Language test** — flag every causal verb applied to an associational result,
      and note whether the paper hedges it. The design, not the verb, decides.

---

## 8. Recognised reference frames (named generically)

These are named so you can reach the accepted frame for a given study type. Their
criteria are **not** reproduced here, and **none of them guarantees validity** —
most appraise reporting completeness or risk of bias, not truth. Consult the
current official statement before relying on any of them.

| Study / evidence type | Frame(s) — reach for the official version | What it covers, in general terms |
| --- | --- | --- |
| Randomised experiment (incl. controlled A/B tests) | **CONSORT** (reporting); **CASP** RCT checklist (appraisal); **Cochrane RoB 2** (risk of bias) | A structured reporting checklist with a participant-flow diagram; screening then validity questions; five bias domains, judged low / some concerns / high |
| Non-randomised intervention study | **ROBINS-I** (bias); **STROBE** (reporting) | Bias domains that add confounding and selection; a reporting checklist for observational designs |
| Observational / epidemiological | **STROBE** (reporting); Newcastle–Ottawa-style scales (bias) | Reporting of setting, participants, variables, and statistical methods for cohort, case–control, and cross-sectional studies |
| Diagnostic / accuracy study | **STARD** (reporting); **QUADAS-2** (bias) | Participants, index test, reference standard, and flow; bias domains for test-accuracy reviews |
| Systematic review / meta-analysis | **PRISMA 2020** (reporting), **PRISMA-P** (protocols), **PRISMA-S** (searching); **MOOSE** (observational syntheses); **AMSTAR-2** (review quality); **GRADE** (certainty) | A structured checklist with a four-phase selection flow diagram; search transparency; the quality of the review itself; the certainty of the body of evidence |
| Qualitative study / review | **CASP** qualitative checklist; **COREQ** / **SRQR** (reporting); **GRADE-CERQual** (confidence) | Aims and methodology, recruitment, data collection and analysis, reflexivity, saturation; confidence in synthesised qualitative findings |
| Prediction model | **TRIPOD** (reporting); **PROBAST** (bias) | Development and validation reporting; risk-of-bias domains for predictive models |
| Trial protocol | **SPIRIT** | Reporting of a trial's protocol before results |
| Animal / fundamental research | **ARRIVE** | Reporting of experimental design, animals, and procedures |
| Case report | **CARE** | Reporting of a single clinical case |
| Empirical software engineering (any method) | **ACM SIGSOFT Empirical Standards** | The community's evidence standards; general quality criteria span conclusion, construct, internal, and external validity plus reproducibility |
| Machine-learning experiment | **ML reproducibility checklists** (e.g. the NeurIPS / McGill *ML Reproducibility Checklist*); **ML Code Completeness Checklist** | Architecture and training procedure, hyperparameters and stopping criteria, compute, code and data availability, dataset descriptions, baselines, ablations, error bars |
| Computational artifact (system, code, data) | **Artifact review and badging** (available / evaluated / replicated) | Whether an artifact exists, was independently evaluated, and whether its results replicate |
| Model or dataset release | *Model Cards*; *Datasheets for Datasets* | Intended use, limitations, provenance, composition, and licensing |

**How to use a frame.** Match a frame to the *study type*, not to the paper's topic.
Prefer the frame the authors themselves followed; if they followed none, say so and
say which one *should* have applied. Two cautions worth stating plainly:

- **Reporting ≠ validity.** CONSORT, PRISMA, and STROBE improve transparency; a
  paper can comply fully and still be wrong.
- **Frames are versioned and vary by study design.** Many of these are revised (and
  CASP-style checklists deliberately assign no total score). Always consult the
  current official statement rather than a remembered summary.

---

## 9. Output

Record the appraisal with the bundled template (`assets/appraisal-template.md`): the
**claim→evidence table** and the **critical layer** carry the weight; keep the three
voices separate and anchor every [Paper] item. The genre-specific reading order that
gets you to the evidence lives in the companion `genres.md`.
