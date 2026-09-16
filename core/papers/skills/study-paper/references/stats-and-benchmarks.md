# Statistics & Benchmark Red Flags

> **Usage.** Load this when a mode touches numbers — the Review statistics /
> reproducibility audit, an Implement evaluation protocol, or any Skim whose
> headline claim is a measured result. Work down the catalog, keep the flags that
> apply, and rank them by severity. A red flag is a **question to check against
> the source**, not a verdict: say which flags apply, anchor each to the paper,
> and calibrate confidence to how plainly the paper reports — or omits — the
> detail.
>
> **Anchor every finding** (section / table / figure / equation / page). If the
> paper does not report what a check needs, that *absence is itself the finding* —
> mark it [Unknown] rather than assuming the favourable reading.

The catalog falls into two families that catch different failure modes:

- **Statistical & design flags (1–9)** — is a reported difference real,
  reproducible, and correctly tested?
- **Benchmark & protocol flags (10–15)** — do the measured numbers mean what the
  paper claims they mean?

Two habits run through all of it:

- **Report a point estimate *with* its uncertainty.** A number without an
  interval is a claim without a range.
- **Separate the number from the claim.** The paper usually reports numbers; you
  judge whether those numbers license the words ("outperforms", "state of the
  art", "efficient", "generalizes").

---

## Statistical & design red flags

### 1. Weak, outdated, or self-tuned baselines
**What to look for:** baselines that are undersized, under-trained, given fewer
epochs/tuning trials, or reproduced sloppily — while the proposed method is
tuned hard; or comparisons only against old methods when stronger recent ones
exist.
**Why it misleads:** the reported "gain" may be an artifact of unequal tuning or
capacity rather than the contribution the paper claims; a strawman baseline
manufactures an improvement.
**How to check:** are baselines from the same era and tuned with the same budget?
Are baseline numbers reproduced by the authors or quoted from other papers? Is
the *simplest* strong baseline included?

### 2. Missing or self-serving ablations
**What to look for:** no ablations at all, ablations that skip the component under
dispute, or ablations that remove parts from a configuration different from the
headline model.
**Why it misleads:** without removing one part at a time from a *comparable*
configuration, the improvement cannot be attributed to the claimed design choice
instead of to some correlated change.
**How to check:** is each novel component ablated from the same base? Are
interactions and the "kitchen-sink" vs "core" settings compared?

### 3. Point estimates without confidence intervals
**What to look for:** a single number per (method, dataset) with no error bars,
standard error, or interval.
**Why it misleads:** a bare number hides run-to-run variance, so a "win" may sit
entirely inside the noise band and not survive a re-run.
**How to check:** are confidence intervals, standard errors, or error bars
reported? Do the intervals for the top methods overlap?

### 4. Underpowered studies (no sample-size or power analysis)
**What to look for:** conclusions drawn from a handful of runs, examples, or
subjects, with no power analysis and no minimum-detectable-effect estimate.
**Why it misleads:** a small sample can neither reliably detect a real effect nor
rule out a spurious one, so noise masquerades as signal in both directions.
**How to check:** how many runs / seeds / subjects / test items? Was a power or
minimum-detectable-effect calculation done? Is n large enough for the claimed
effect size?

### 5. Undisclosed or cherry-picked seeding
**What to look for:** seeding not stated at all; or many seeds run but only the
best reported; or "we report the best of N runs" without the others.
**Why it misleads:** an unseeded single run is a sample of size one and may be a
lucky draw; hiding the spread makes an irreproducible result look stable.
**How to check:** are seeds listed and the number of runs stated? Is
seed-to-seed variance reported? Are results averaged over seeds with an interval?

### 6. Multiple comparisons without correction
**What to look for:** many datasets, metrics, tasks, or hyperparameter settings
tested, the best reported, and no correction for multiplicity.
**Why it misleads:** testing enough combinations manufactures a
significant-looking winner by chance (family-wise error); the reported best is
partly the luckiest draw.
**How to check:** how many comparisons were made? Is there any correction
(Bonferroni, Holm, FDR) or nested cross-validation? Is the winner separated from
the field by more than the field's own spread?

### 7. Effect size ignored (significance-only reporting)
**What to look for:** p-values or "statistically significant" claims with no
magnitude, no interval, and no practical interpretation.
**Why it misleads:** statistical significance is not practical importance — with a
large n a trivial difference is "significant", and a real but tiny effect can be
oversold as a breakthrough.
**How to check:** is the effect size reported *with* its interval? Is the effect
interpreted against a meaningful threshold, not only against zero?

### 8. Statistical tests misapplied
**What to look for:** parametric tests on skewed, ordinal, or heavy-tailed data;
paired designs analysed as independent; repeated measures ignored; means compared
when distributions or medians are what matter.
**Why it misleads:** violated assumptions produce invalid p-values, and ignoring
pairing or non-independence throws away real signal or invents it.
**How to check:** does the test match the design (paired vs independent,
distributional assumptions, repeated measures)? Are error bars within- or
between-subject?

### 9. Compute- or budget-unmatched comparisons
**What to look for:** the new method receives more compute, more tuning trials,
more data, or a larger model than its baselines.
**Why it misleads:** performance can be bought with resources, so "better" may
mean "bigger budget" rather than a better method — and efficiency claims in
particular collapse if budgets differ.
**How to check:** are parameters, FLOPs, wall-clock, and tuning budget reported
and matched across methods? Is any efficiency claim normalized for compute?

---

## Benchmark & protocol red flags

### 10. Test-set tuning / no held-out validation
**What to look for:** hyperparameters, model selection, early stopping, or prompt
choice made on the test set, with no separate validation split.
**Why it misleads:** selecting on the test set folds it into training, so the
reported number is optimistic and no longer estimates generalization.
**How to check:** is there a distinct validation set? How were hyperparameters
and stopping criteria chosen? Was the test set touched during development?

### 11. Cherry-picking / selective reporting
**What to look for:** only the best dataset, metric, checkpoint, seed, or subset
is shown; failed settings, negative results, or regressions are omitted.
**Why it misleads:** a curated subset overstates typical performance — the full
distribution the reader would meet in practice is worse.
**How to check:** are *all* datasets and metrics shown, or only winners? Are
failures and negative results reported? Do the tables match the text?

### 12. Benchmark leakage & data contamination
**What to look for:** train/test overlap, test items derived from training
sources, near-duplicates across splits, or a large pretraining corpus that may
already contain the test set.
**Why it misleads:** leaked data inflates scores and destroys the very claim the
benchmark is meant to support — that the method generalizes to unseen data.
**How to check:** how were splits built? Was any deduplication or near-duplicate
check done? Could the pretraining or retrieval corpus hold the evaluation data?

### 13. Saturated benchmarks / ceiling & floor effects
**What to look for:** scores near the maximum (or near chance) where the top
methods differ by a fraction of a point.
**Why it misleads:** on a saturated benchmark the remaining gap is comparable to
run-to-run noise, so a "+0.3%" "new state of the art" may be measurement
jitter rather than progress.
**How to check:** how much headroom remains? Is the claimed gain larger than the
seed variance and the metric's granularity? Is there a harder, unsaturated
benchmark?

### 14. Metric misuse / incomparable numbers
**What to look for:** different preprocessing, splits, tokenization, metric
variant, or averaging scheme than the prior work, yet compared head-to-head; or a
metric chosen to flatter the method.
**Why it misleads:** numbers measured under different protocols are not
comparable — a higher value may reflect an easier setup, not a better method.
**How to check:** is the evaluation protocol identical to the baselines'? Are
metric definitions and any preprocessing stated? Are per-class or worst-case
numbers shown, not only the aggregate?

### 15. Publication & reporting bias (context flag)
**What to look for:** the claimed state of the art rests on comparing against
published results only, where failures and nulls rarely appear.
**Why it misleads:** the visible literature over-represents successes, so the
benchmark landscape looks more solved, and simpler, than it is.
**How to check:** is there a registered protocol or held-out evaluation run by a
third party? Are negative or non-reproduced results acknowledged? Does a survey,
benchmark, or reproduction study report conflicting numbers?

---

## What good reporting looks like

Use this as the target when you state what the paper *should* have shown:

- Every load-bearing number carries an **interval or variance**, not a bare point.
- Baselines are **recent, strong, and budget-matched**; the simplest strong one is
  included.
- Ablations remove each claimed component from a **comparable base**.
- **Seeds and run counts** are stated, with results **averaged over seeds**.
- Multiplicity is **acknowledged or corrected**; the winner clears the field's
  spread.
- **Effect sizes and intervals**, not only p-values, are interpreted.
- Splits are **leakage-checked**; test data is untouched during development.
- **All datasets and metrics** are reported, including regressions.
- **Compute/params/budget** are reported and matched for fairness claims.
- Code and data availability, and a **reproduction recipe**, are stated.

## Minimum statistics & reproducibility checklist

Apply this to the Review mode's statistics audit; mark each item reported,
partially reported, or absent (absent → a red flag above):

| Check | Reported? | Anchor |
| --- | --- | --- |
| Number of runs / seeds, and variance across them | | |
| Confidence interval or error bars on headline numbers | | |
| Baselines present, recent, and budget-matched | | |
| Ablations from a comparable base configuration | | |
| Multiplicity handled across datasets / metrics / configs | | |
| Effect size, not p-value alone | | |
| Statistical test appropriate to the design | | |
| Compute / parameters / tuning budget disclosed | | |
| Separate validation set; test set untouched | | |
| Splits deduplicated / contamination checked | | |
| All datasets and metrics shown, not a curated subset | | |
| Code / data / reproduction recipe available | | |

## Cross-references

- Claim → evidence mapping and threats-to-validity: [appraisal.md](appraisal.md).
- Genre-specific evidence norms (what counts as proof in each field):
  [genres.md](genres.md).
- Turning an audit into an implementable evaluation protocol: the **Implement**
  mode in the package's main instructions (Mode 3).
