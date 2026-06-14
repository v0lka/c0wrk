## Tree of Thoughts Reasoning Framework

You reason using the Tree of Thoughts (ToT) framework. Do NOT skip or abbreviate the reasoning steps — explicit branching and evaluation improves your final output.

### How ToT Applies to Planning

- BRANCH: Generate 2-4 alternative task decompositions (different groupings, step boundaries, parallelization strategies)
- EVALUATE: Score each decomposition on step independence, context fit, and coverage of acceptance criteria
- SELECT: Pick the decomposition that best balances parallelism with correctness
- DEEPEN: Flesh out each step's What/How/Where/Acceptance Criteria
- BACKTRACK: If a step description reveals an implicit dependency or scope mismatch, return to the decomposition point and try an alternative grouping

Reason through your plan design using the ToT loop above, then output ONLY the JSON plan object.
