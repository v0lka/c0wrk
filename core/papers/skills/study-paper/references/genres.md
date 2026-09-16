# Reading by Genre — Order, Evidence Norms, Reviewer Focus

> **Load this when** the paper's genre changes how you read it — before you commit
> to a reading order or decide what will count as evidence. The genre sets the
> default order of attack and the bar a claim must clear. It never relaxes the
> anchor discipline, the three-voices rule, or the critical layer — it only tells
> you *where* to look and *what* to weigh.
>
> Companion: `appraisal.md`, in this `references/` directory, holds the
> genre-independent claim→evidence rubric that every genre feeds into.

---

## Step 0 — Identify the genre before you read

Read the signals, not the venue. The genre follows the paper's **central claim**.

| Signals in the paper | Genre | The claim at its core |
| --- | --- | --- |
| Theorems, lemmas, proofs, assumptions, bounds, "we prove", "we show that" | **Theoretical** | "Under these assumptions, X holds." |
| Datasets, baselines, metrics, ablations, error bars, seeds, "state of the art" | **Empirical / ML** | "Method M improves result R on task T by Δ." |
| A built system, workloads, latency / throughput / cost / energy, an artifact | **Systems & engineering** | "System S achieves performance P that prior systems could not." |
| A new benchmark, metric, or dataset; a measurement study | **Measurement / benchmark** | "Measure M validly captures capability C." |
| Organises prior work; search strategy; taxonomy; "gaps and open problems" | **Survey / review** | "The field stands at S; here are the gaps." |

If the signals conflict, name the **primary** genre (of the central claim) and the
**secondary** one, then say which reading order you are following and why. Genres
blend freely — an ML paper may introduce a benchmark (measurement); a systems
paper may prove a bound (theory).

For every genre below: **Reading order** = the sequence to read in;
**What counts as evidence** = the bar a claim must clear in that genre, and what is
*not* evidence; **What a reviewer looks for** = the checks a careful referee runs;
**Reference frames** = the recognised standards that apply, named generically.

---

## 1. Empirical / ML

Quantitative experiments: a method is proposed and compared against baselines on
shared data and metrics.

**Reading order**

1. **Title and abstract** — record the headline claim verbatim. Treat it as a
   hypothesis to test, not a finding.
2. **Main results figure or table** — jump straight to the actual numbers before
   the narrative shapes your expectations.
3. **Introduction** — motivation, problem, and the contributions *as the authors
   frame them*.
4. **Method** — just enough of the pipeline to know what was varied and what was
   held fixed.
5. **Experimental setup** — datasets and splits, baselines, metrics,
   hyperparameters, and compute budget.
6. **Results in full** — main comparison, ablations, per-group breakdowns, and any
   error bars or seed spread.
7. **Limitations / broader impact** — note what the authors concede.
8. **Appendix** — full hyperparameters, extra experiments, significance tests; the
   load-bearing detail often lives here.
9. **Abstract again** — re-read last, comparing each clause to the results you saw.

**What counts as evidence**

- A controlled comparison on **shared data and splits** against a **fairly tuned**
  baseline, ideally compute-matched, with the difference reported as a
  **distribution across independent runs** (seeds) rather than one number.
- **Ablations** that isolate the contribution of each claimed component.
- Evaluation on **held-out data** the method never saw and that is not part of any
  pre-training corpus.
- *Not evidence:* a single run; a baseline left untuned; test-set or benchmark
  tuning; a metric with no variance; a gain within noise; an unablated
  "kitchen-sink" system.

**What a reviewer looks for**

- [ ] Is the comparison fair — baseline tuned to its best, same data and splits,
      compute-matched?
- [ ] Are seeds, variance, and significance reported, and is the reported gain
      beyond run-to-run noise?
- [ ] Is the evaluation protocol standard, and is the test set uncontaminated (no
      leakage from pre-training or benchmark tuning)?
- [ ] Does the headline number reflect the typical case or the best case — are
      medians and full distributions shown?
- [ ] Do the ablations actually isolate the claimed mechanism?
- [ ] Is it reproducible — complete hyperparameters, released code, described data?

**Reference frames (generic):** ML reproducibility checklists (e.g. the
NeurIPS / McGill *ML Reproducibility Checklist*) and code-completeness checklists;
artifact review and badging. Consult the current official versions.

---

## 2. Systems & engineering

A system is built and its performance, cost, or reliability is measured against
prior systems and specified workloads.

**Reading order**

1. **Abstract and introduction** — the problem, why current systems fall short,
   and the claimed gain.
2. **Design overview / architecture figure** — components, interfaces, and
   data/control flow.
3. **Design details of the novel mechanism(s)** — the part the contribution rests
   on.
4. **Implementation** — what is built versus simulated; languages, hardware,
   scale; any known shortcuts.
5. **Evaluation methodology** — workloads/benchmarks, baselines, metrics (latency,
   throughput, cost, energy, reliability), and the measurement procedure.
6. **Results** — then the tail (p99) and scaling behaviour, not just the mean.
7. **Threats to validity / limitations**, then **related work** for positioning.

**What counts as evidence**

- Performance measured on **realistic, representative workloads** under a stated
  procedure, against **strong baselines configured to their best**, on real
  hardware where the claim concerns real hardware.
- **Scaling behaviour and tail latency**, plus a decomposition showing which
  component yields the gain.
- **Reproduction artifacts** sufficient for an independent party to re-run the
  evaluation.
- *Not evidence:* a microbenchmark the design was tuned for; baselines left
  mis-configured or outdated; simulation presented as measurement; a single lucky
  run; mean-only reporting that hides the tail.

**What a reviewer looks for**

- [ ] Are the workloads representative, or strawmen chosen to flatter the design?
- [ ] Are baselines current and fairly configured, and is the comparison
      compute/resource-matched?
- [ ] Is the measurement methodology sound — warmups, repetitions, percentiles,
      variance, and isolation against interference?
- [ ] What is the cost/complexity price of the gain, and what breaks at larger
      scale?
- [ ] Is there an artifact, and how far does it go — available, evaluated, or
      replicated?

**Reference frames (generic):** ACM-style artifact review and badging; community
empirical standards for engineering research (e.g. the ACM SIGSOFT *Empirical
Standards*), which judge conclusion, construct, internal, and external validity
plus reproducibility. Consult the current official versions.

---

## 3. Theoretical

The contribution is a theorem, bound, or algorithm with a guarantee; the proof is
the artefact.

**Reading order**

1. **Abstract and introduction** — the main result stated informally, and why it
   matters.
2. **Theorem statements first** — read every assumption and the conclusion; skip
   the proof bodies on this pass.
3. **Proof strategy / sketch** — the intuition for why it works.
4. **Key lemmas** — the technical core the main proof leans on.
5. **Proof bodies** — verify the crux step and any place a gap could hide; you need
   not re-derive every line.
6. **Examples, counterexamples, and tightness** — matching bounds or instances that
   delimit the result.
7. **Open problems and the limits of the assumptions.**

**What counts as evidence**

- A **correct proof**, from *explicitly stated* assumptions, to the stated
  conclusion — the proof *is* the evidence.
- **Tightness**: a matching upper and lower bound, or a worked instance showing the
  bound is achieved.
- **Delimitation by counterexample**: what the result does *not* cover.
- **Empirical agreement**, where the paper claims its theory predicts observed
  behaviour.
- *Not evidence:* a proof gap ("it can be shown", "clearly"); an unstated
  regularity or smoothness condition; a special case stated as a general result;
  simulation standing in for proof.

**What a reviewer looks for**

- [ ] Are all assumptions stated, and are they necessary and plausible — or so
      strong that the theorem is nearly vacuous?
- [ ] Is the proof correct at its crux — any circularity, unhandled edge case, or
      hidden assumption?
- [ ] Is the bound tight, or is there a gap between the upper and lower bounds?
- [ ] Does the result generalise beyond the toy setting, and can the assumptions be
      relaxed?
- [ ] Does the theory connect to, and survive, the empirical evidence?

**Reference frames (generic):** correctness is the bar; no single reporting
checklist dominates this genre. Useful generic aids: an **assumption audit**, a
**counterexample search**, and — where the result is machine-checkable —
mechanised proof assistance. Consult the relevant formal-methods practice.

---

## 4. Measurement / benchmark

The contribution is a benchmark, metric, or dataset, or a study that measures
existing systems.

**Reading order**

1. **Abstract and introduction** — the construct being measured, and why it
   matters.
2. **Definition of the benchmark or metric** — tasks, data construction, and
   annotation protocol; or the measurement design.
3. **Construct validity** — the case that the metric actually captures the
   intended capability.
4. **Data** — collection, sampling frame, provenance, licensing/consent, and
   contamination/leakage controls.
5. **Results** — how existing systems score, and the analysis behind the ranking.
6. **Threats to validity** — contamination, saturation, construct drift, and
   gameability / leaderboard overfitting.
7. **Limitations and intended use.**

**What counts as evidence**

- **Construct validity**: a reasoned, ideally validated argument that the metric
  measures the intended construct, not a convenient proxy.
- **Reliability**: consistent scores under repeat runs and re-annotation, with
  reported agreement.
- **Discriminative power**: the benchmark separates known-strong from known-weak
  systems, and is neither saturated nor trivially gameable.
- For a measurement study: a **sound sampling frame**, a **representative sample**,
  and an analysis robust to alternative explanations.
- *Not evidence:* a metric with no validity argument; a benchmark that leaks into
  pre-training; score differences smaller than run-to-run variation; annotation
  with unmeasured reliability.

**What a reviewer looks for**

- [ ] Is the construct defined, and is the metric a faithful proxy for it?
- [ ] What is the data provenance — sampling, licensing, consent — and is
      train/test contamination controlled?
- [ ] Is reliability reported (inter-annotator agreement, seed/run variance), and
      is the metric stable under benign perturbations?
- [ ] Is the benchmark saturated or already solved, or gameable by a shortcut that
      ignores the construct?
- [ ] Is the cross-system comparison fair — same protocol, prompts, and budget?

**Reference frames (generic):** psychometric notions of **construct validity** and
**reliability**; benchmark contamination / leakage guidance; the observation
captured by **Goodhart's law** (a measure that becomes a target stops being a good
measure); documentation frames such as *Datasheets for Datasets* and *Model Cards*.
Consult the current official sources.

---

## 5. Survey / review

The contribution organises and synthesises a body of prior work.

**Reading order**

1. **Abstract and introduction** — scope, research questions, and what kind of
   review this is.
2. **Methodology** — search databases, date range, search strings,
   inclusion/exclusion criteria, screening, and a selection flow diagram (if
   systematic).
3. **Organising framework / taxonomy** — how the authors carve up the field.
4. **Findings by theme** — the synthesis and any evidence tables.
5. **Quality appraisal of the included work** — risk of bias, or the applicable
   appraisal frame.
6. **Gaps and open problems** — and the authors' claims about the state of the
   field.
7. **Appendices** — full search strings, the list of included studies, and data
   extraction.

**What counts as evidence**

- A **systematic review**'s evidence is its **protocol**: a pre-defined,
  reproducible search, explicit inclusion criteria applied consistently, an
  appraisal of each included study, and a synthesis that respects study quality and
  heterogeneity.
- A **narrative review**'s evidence is the reasoning over a chosen set; selection
  may be illustrative rather than exhaustive, and should be read as such.
- A synthesis is only as strong as what it includes — it **inherits the weaknesses**
  of the included work and can amplify its biases (e.g. publication bias).
- *Not evidence:* no stated protocol; an unreproducible search; selective citation
  that omits contradicting work; no quality appraisal; naive vote-counting that
  ignores study size and quality.

**What a reviewer looks for**

- [ ] Is the search reproducible and comprehensive — databases, dates, and strings
      all reported?
- [ ] Are inclusion/exclusion criteria explicit and applied consistently, with
      screening agreement?
- [ ] Is the quality / risk of bias of the included studies assessed, and does it
      inform the conclusions?
- [ ] Is the synthesis appropriate — heterogeneity and publication bias addressed,
      not ignored?
- [ ] Are "the field has converged / the state of the art is…" claims supported by
      the included body, and not only by the authors' own prior work?

**Reference frames (generic):** **PRISMA** (reporting), **PRISMA-P** (protocols),
and **PRISMA-S** (searching) for systematic reviews; **MOOSE** for syntheses of
observational studies; **AMSTAR-2** for review quality; **GRADE** for certainty of
the evidence; the Cochrane handbook for synthesis methods; the **CASP**
systematic-review checklist for appraisal. Consult the current official versions.

---

## Cross-genre notes

- **Genres blend, and the bar compounds.** A survey of weak studies is weak
  evidence; a benchmark built on a contaminated dataset undermines every paper
  that reports on it. Read the central claim's genre first, then the secondary.
- **Genre changes *where* you look, never *whether* you are critical.** Whatever the
  genre, the reading merges back into the same rubric — the claim→evidence map,
  the threats-to-validity checks, and the overclaiming screen in `appraisal.md`.
