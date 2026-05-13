A plan execution completed but some steps failed. Revise the plan to address the failures.

Original plan:
ORIGINAL-PLAN

Completed steps with results:
COMPLETED-STEPS

Failed step: FAILED-STEP

CURRENT-REFLECTION

PREVIOUS-SESSION-REFLECTIONS

Available skills:
AVAILABLE-SKILLS

WORKSPACE-PATH

Create an updated plan following these rules:

## Tree of Thoughts Reasoning Framework

You reason using the Tree of Thoughts (ToT) framework. Do NOT skip or abbreviate the reasoning steps — explicit branching and evaluation improves your final output.

### How ToT Applies to Replanning

- BRANCH: Generate 2-4 alternative fixes for the failed step (different approaches, tool choices, step boundaries)
- EVALUATE: Score each fix against the failure root cause and the reflection's action plan
- SELECT: Pick the fix that addresses the root cause with minimal disruption to successful steps
- BACKTRACK: If a fix introduces new dependencies on failed steps, promote an alternative

Reason through your replan using the ToT loop above, then output ONLY the JSON plan object.

1. PRESERVE successful steps: reuse their exact step IDs (e.g., "step_1", "step_2") unchanged in the new plan. Their outputs will be reused automatically.
2. Only ADD or REPLACE steps that directly address the failures.
3. New steps MUST use IDs that don't conflict with existing step IDs (continue numbering from the highest existing ID).
4. Keep the plan as close to the original as possible — minimal targeted changes, not a complete rewrite. However, EVALUATE whether minimal changes actually address the root cause; BACKTRACK if they don't.
5. If two sequential steps failed for related reasons, consider merging them into a single step.
6. If previous session reflections show a repeating failure pattern with the same root cause across 2+ attempts, consider a broader structural change.

## Git Policy

Replanned steps MUST NOT include git commands that modify repository state (commit, push, merge, rebase, reset, etc.). Never plan committing or pushing unless the user's original request explicitly asks for it.

Respond with a JSON Plan object:
{"steps": [{"id": "step_1", "summary": "Short 5-7 word label", "description": "What: ...\nHow: ...\nWhere: ...\nAcceptance Criteria:\n- ...", "depends_on": [], "parallelizable": true, "estimated_tools": ["tool1"]}]}
